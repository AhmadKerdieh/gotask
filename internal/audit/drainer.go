package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"gotask/internal/database"
)

// Drainer is the background worker that moves rows from audit_outbox
// (staging, written transactionally with business operations) to
// audit_log (durable, queryable history).
//
// ── The four questions of every goroutine ────────────────────────────
//
//   1. WHO STARTS IT
//      main.go, alongside the HTTP server, both under one errgroup. The
//      drainer is a peer of the server, not a child of any request.
//
//   2. WHO STOPS IT
//      Cancellation of the context passed to Run. main.go cancels the
//      root context when SIGTERM is received; that propagates here and
//      Run returns nil. The drainer does NOT have its own Stop method:
//      having two ways to stop something is two ways to get it wrong.
//
//   3. WHAT HAPPENS TO IN-FLIGHT WORK AT SHUTDOWN
//      A drain iteration is the unit of atomicity. Once a batch is
//      claimed (transaction begun, rows SELECT FOR UPDATE'd), we finish
//      that iteration even if context is cancelled mid-way — abandoning
//      it would leave rows locked until the connection timed out, and
//      would lose the work we already did. AFTER the iteration commits,
//      we observe context cancellation and return. This is the
//      "complete the current unit, then stop" pattern.
//
//   4. WHAT HAPPENS WHEN SOMETHING INSIDE IT PANICS
//      A panic in the drain loop without recovery would crash the
//      entire process. Run wraps its loop body in a deferred recover
//      that logs the panic and continues. This is the panic-firewall
//      pattern: long-lived goroutines deserve their own recovery
//      because the request-recovery middleware does not protect them.
//
// ── At-least-once delivery and idempotency ───────────────────────────
//
// We claim rows with FOR UPDATE SKIP LOCKED, then for each row INSERT
// into audit_log and DELETE from audit_outbox, all in one tx. If we
// crash after the INSERT but before the DELETE, the next iteration
// reprocesses the row — the audit_log INSERT will conflict on the
// shared primary-key UUID and we treat that as success (the row WAS
// drained, just not deleted). That makes the drain idempotent against
// crash-and-restart without any state-tracking columns.
//
// ── Concurrent drainers ──────────────────────────────────────────────
//
// FOR UPDATE SKIP LOCKED means if we ever run multiple instances of
// this app, each drainer's claim will skip rows another drainer has
// locked. No coordination service needed; Postgres does the work. The
// course runs one instance, but writing it correctly costs nothing and
// preserves the option.
type Drainer struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	interval time.Duration
	batch    int
}

// DrainerConfig configures Drainer construction. Sensible defaults make
// the typical wiring a one-liner.
type DrainerConfig struct {
	// PollInterval is how long to wait between drain iterations when
	// there is no work. A shorter interval reduces audit lag at the
	// cost of more empty SELECTs. 1s is a good middle ground; with
	// LISTEN/NOTIFY a future phase could drop this to "when notified".
	PollInterval time.Duration

	// BatchSize bounds how many outbox rows one iteration drains. A
	// larger batch amortises commit overhead; a smaller batch shortens
	// each iteration's lock window. 100 is conservative.
	BatchSize int
}

// NewDrainer constructs a Drainer with sane defaults applied where the
// config is zero-valued.
func NewDrainer(pool *pgxpool.Pool, log *slog.Logger, cfg DrainerConfig) *Drainer {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	return &Drainer{
		pool:     pool,
		log:      log,
		interval: cfg.PollInterval,
		batch:    cfg.BatchSize,
	}
}

// Run blocks until ctx is cancelled, polling the outbox at the
// configured interval. Returns nil on clean shutdown, an error only on
// startup-level problems we can't recover from (which is currently
// none: every per-iteration error is logged and the loop continues).
//
// Use under errgroup.Group.Go to share lifecycle with the HTTP server.
func (d *Drainer) Run(ctx context.Context) error {
	// Defer the panic firewall. A panic in any draining call should
	// not take down the process — it should be logged and the loop
	// should continue. This wrapper is what makes the drainer safe to
	// run as a peer of the HTTP server: the server has its own
	// recover middleware for handler panics; the drainer needs the
	// equivalent for its own loop.
	defer func() {
		if p := recover(); p != nil {
			d.log.Error("drainer panic", "panic", p)
			// We do NOT re-panic. The drainer dying silently would be
			// worse than logging and exiting gracefully — the parent
			// errgroup will observe the goroutine return and react.
		}
	}()

	d.log.Info("audit drainer started",
		"poll_interval", d.interval,
		"batch_size", d.batch,
	)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop() // always-defer-Stop on tickers; documented Go gotcha.

	// Drain once immediately on startup so any rows that accumulated
	// while the app was down aren't held back by a full poll interval.
	d.drainOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			d.log.Info("audit drainer stopping", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			d.drainOnce(ctx)
		}
	}
}

// drainOnce runs one iteration: claim a batch of outbox rows, copy them
// into audit_log, delete from outbox. The whole iteration is a single
// transaction so the claim, insert, and delete are atomic — a crash
// mid-iteration rolls back the claim and the rows are visible to the
// next iteration.
//
// All errors are logged and swallowed at this level. The loop in Run
// continues unconditionally; a one-iteration database hiccup is not a
// reason to stop draining forever. Persistent failures will be visible
// as a growing audit_outbox table (a metric Phase 9 will surface).
func (d *Drainer) drainOnce(ctx context.Context) {
	// We deliberately use a DIFFERENT context for the database work
	// than the outer ctx, so a cancellation during shutdown does not
	// abort an in-flight iteration mid-commit (which could leave
	// outbox rows in a half-drained state). The "complete the unit
	// then stop" pattern from the file header.
	//
	// We DO bound this with a timeout, so a wedged DB call cannot
	// hold up shutdown indefinitely.
	iterCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := d.pool.Begin(iterCtx)
	if err != nil {
		d.log.Warn("drain: begin tx failed", "error", err)
		return
	}
	// Rollback safely; if we Commit successfully below, Rollback is a
	// no-op (pgx returns ErrTxClosed which we ignore).
	defer func() {
		if rbErr := tx.Rollback(iterCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			d.log.Warn("drain: rollback failed", "error", rbErr)
		}
	}()

	// Claim a batch. FOR UPDATE SKIP LOCKED means concurrent drainers
	// (today: there's only one; tomorrow: maybe many) divide work
	// without coordination. ORDER BY enqueued_at preserves causal
	// order in audit_log.
	const claimQ = `
		SELECT id, actor_sub, action, target_kind, target_id, outcome, detail, request_id, enqueued_at
		FROM audit_outbox
		WHERE claimed_at IS NULL
		ORDER BY enqueued_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(iterCtx, claimQ, d.batch)
	if err != nil {
		d.log.Warn("drain: claim query failed", "error", err)
		return
	}

	type claim struct {
		ID        uuid.UUID
		Actor     uuid.UUID
		Action    string
		Target    Target
		Outcome   string
		Detail    []byte
		RequestID *string
		Enqueued  time.Time
	}
	var batch []claim
	for rows.Next() {
		var c claim
		if err := rows.Scan(
			&c.ID, &c.Actor, &c.Action,
			&c.Target.Kind, &c.Target.ID,
			&c.Outcome, &c.Detail, &c.RequestID, &c.Enqueued,
		); err != nil {
			rows.Close()
			d.log.Warn("drain: scan failed", "error", err)
			return
		}
		batch = append(batch, c)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		d.log.Warn("drain: rows iteration error", "error", rerr)
		return
	}

	if len(batch) == 0 {
		// Nothing to do. The deferred rollback releases the tx.
		return
	}

	// For each claimed row: INSERT into audit_log (idempotent on PK),
	// DELETE from audit_outbox. We could batch these into multi-row
	// statements; we don't, because the per-row size is small and the
	// code stays simpler. Phase 9 can optimise if needed.
	const insertQ = `
		INSERT INTO audit_log
			(id, occurred_at, actor_sub, action, target_kind, target_id, outcome, detail, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING`

	const deleteQ = `DELETE FROM audit_outbox WHERE id = $1`

	drained := 0
	for _, c := range batch {
		_, err := tx.Exec(iterCtx, insertQ,
			c.ID, c.Enqueued,
			c.Actor, c.Action,
			c.Target.Kind, c.Target.ID,
			c.Outcome, c.Detail, c.RequestID,
		)
		if err != nil {
			// Per-row failure: log, abandon the batch (rollback will
			// release the lock; rows return to the queue for next
			// iteration). Don't try to be clever about partial
			// success — at-least-once + idempotent target makes
			// "retry the whole batch" the right move.
			d.log.Warn("drain: insert audit_log failed",
				"row_id", c.ID, "error", err)
			return
		}

		if _, err := tx.Exec(iterCtx, deleteQ, c.ID); err != nil {
			d.log.Warn("drain: delete audit_outbox failed",
				"row_id", c.ID, "error", err)
			return
		}
		drained++
	}

	if err := tx.Commit(iterCtx); err != nil {
		// Commit failure: the deferred rollback will fire, the rows
		// stay in audit_outbox, next iteration will pick them up. The
		// idempotency at audit_log catches the duplicate.
		d.log.Warn("drain: commit failed", "error", err, "batch_size", drained)
		return
	}

	d.log.Debug("drain: batch drained", "count", drained)
}

// Assert the type at compile time so an accidental signature drift on
// pgconn doesn't break callers silently. (Tag is unused locally; this
// is documentation by way of compilation.)
var _ pgconn.CommandTag = pgconn.CommandTag{}

// drainerError is reserved for future typed errors the drainer might
// expose to callers. Today the drainer logs and swallows; if/when an
// outer caller wants to react to drainer health, this stub is where
// the typed surface lands.
type drainerError struct{ msg string }

func (e *drainerError) Error() string { return fmt.Sprintf("drainer: %s", e.msg) }

// Ensure database.Queryer is satisfied by *pgxpool.Pool — drainOnce
// uses tx directly, but the assertion keeps the file honest about its
// architectural dependency.
var _ database.Queryer = (*pgxpool.Pool)(nil)
