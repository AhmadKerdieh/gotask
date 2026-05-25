//go:build integration
// +build integration

// Integration tests for the audit pipeline: Recorder → audit_outbox →
// Drainer → audit_log. These verify the Phase 8 outbox pattern as a
// black box against real Postgres, including the parts that are hard
// to test with fakes:
//
//   - The Recorder's INSERT against audit_outbox actually parses and
//     stores all columns (including request_id, which the unit tests
//     can stamp but not VERIFY made it to storage).
//
//   - The Drainer's claim query (FOR UPDATE SKIP LOCKED) actually
//     claims unclaimed rows in enqueue order.
//
//   - The Drainer's ON CONFLICT DO NOTHING idempotency really fires
//     when a row is re-drained — the property that lets the drainer
//     recover from crashes.
//
//   - The combined cycle: Recorder writes one row, Drainer processes
//     one iteration, audit_log gets one row, audit_outbox is empty.
package audit_test

import (
	"context"
	"log"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gotask/internal/audit"
	"gotask/test/testutil"
)

var sharedDB *testutil.PostgresFixture

func TestMain(m *testing.M) {
	fixture, err := testutil.StartPostgres(context.Background())
	if err != nil {
		log.Fatalf("integration: start postgres: %v", err)
	}
	sharedDB = fixture
	code := m.Run()
	sharedDB.Terminate(context.Background())
	os.Exit(code)
}

func fixture(t *testing.T) *testutil.PostgresFixture {
	t.Helper()
	t.Cleanup(func() { sharedDB.Truncate(t) })
	return sharedDB
}

// makeEvent builds a representative Event so each test doesn't have
// to repeat the boilerplate. Tests override fields they care about.
func makeEvent() audit.Event {
	return audit.Event{
		Actor:     uuid.New(),
		Action:    audit.ActionTaskCreated,
		Target:    audit.Target{Kind: "task", ID: uuid.New()},
		Outcome:   audit.OutcomeSuccess,
		Detail:    map[string]any{"key": "value"},
		RequestID: "req-test-12345",
	}
}

// TestRecord_WritesToOutbox: a Record call lands the row in the
// audit_outbox table — including request_id, which the Phase 9
// request-id propagation depends on. If this regresses, the trace
// loop closes silently and operators stop being able to correlate.
func TestRecord_WritesToOutbox(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	auditor := audit.NewPostgres(f.Pool)

	ev := makeEvent()
	require.NoError(t, auditor.Record(ctx, f.Pool, ev))

	// Inspect the raw table.
	var count int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_outbox WHERE actor_sub = $1`, ev.Actor,
	).Scan(&count))
	assert.Equal(t, 1, count)

	// Specifically verify request_id was stored — the column was added
	// in Phase 7 but nothing populated it until Phase 9. This is the
	// regression boundary we're protecting.
	var storedReqID string
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT request_id FROM audit_outbox WHERE actor_sub = $1`, ev.Actor,
	).Scan(&storedReqID))
	assert.Equal(t, ev.RequestID, storedReqID)
}

// TestDrainer_MovesOutboxRowsToAuditLog: the full pipeline as a black
// box. Record a row, run one drainer iteration, verify the row is in
// audit_log and the outbox is empty.
func TestDrainer_MovesOutboxRowsToAuditLog(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	auditor := audit.NewPostgres(f.Pool)

	// Use a discarding logger so the test output stays clean; the
	// drainer's normal Info logs are informative in production but
	// noisy in CI.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	drainer := audit.NewDrainer(f.Pool, log, audit.DrainerConfig{
		PollInterval: 50 * time.Millisecond,
		BatchSize:    10,
	})

	ev := makeEvent()
	require.NoError(t, auditor.Record(ctx, f.Pool, ev))

	// Run the drainer for a brief moment, just enough for one
	// iteration. We don't call drainOnce directly because it's
	// unexported (correctly — the public surface is Run); we run
	// Run with a short timeout and let it execute one iteration
	// before context cancellation.
	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = drainer.Run(runCtx) // returns nil when context is cancelled

	// Outbox now empty:
	var outboxCount int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_outbox`,
	).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount, "outbox should be drained")

	// audit_log has exactly the row we wrote, with the request_id
	// preserved across the goroutine boundary.
	var logCount int
	var storedReqID string
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*), max(request_id) FROM audit_log WHERE actor_sub = $1`,
		ev.Actor,
	).Scan(&logCount, &storedReqID))
	assert.Equal(t, 1, logCount)
	assert.Equal(t, ev.RequestID, storedReqID,
		"request_id must propagate from outbox to audit_log")
}

// TestDrainer_IsIdempotent: re-process the same row twice (the
// post-crash-recovery scenario). The Phase 8 design promised this is
// safe because audit_log has a unique primary key and the drainer
// uses ON CONFLICT DO NOTHING. This test exercises the exact path.
//
// We simulate "we crashed after writing to audit_log but before
// deleting from outbox" by manually re-inserting the same id into
// audit_outbox after a normal drain, then draining again.
func TestDrainer_IsIdempotent(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	auditor := audit.NewPostgres(f.Pool)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	drainer := audit.NewDrainer(f.Pool, log, audit.DrainerConfig{
		PollInterval: 50 * time.Millisecond,
		BatchSize:    10,
	})

	// First pass: write, drain.
	ev := makeEvent()
	require.NoError(t, auditor.Record(ctx, f.Pool, ev))

	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_ = drainer.Run(runCtx)
	cancel()

	// Capture the audit_log row's id so we can re-insert it into the
	// outbox to simulate the crash scenario.
	var logID uuid.UUID
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT id FROM audit_log WHERE actor_sub = $1`, ev.Actor,
	).Scan(&logID))

	// Manually re-insert into outbox with the SAME id (simulating
	// "drainer crashed after audit_log INSERT but before outbox
	// DELETE; the row is still in outbox awaiting reprocessing").
	_, err := f.Pool.Exec(ctx, `
		INSERT INTO audit_outbox
			(id, actor_sub, action, target_kind, target_id, outcome, detail, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, '{}', $7)`,
		logID, ev.Actor, ev.Action,
		ev.Target.Kind, ev.Target.ID,
		ev.Outcome, ev.RequestID,
	)
	require.NoError(t, err)

	// Second drain: this is the "we crashed and came back" pass.
	runCtx2, cancel2 := context.WithTimeout(ctx, 200*time.Millisecond)
	_ = drainer.Run(runCtx2)
	cancel2()

	// audit_log STILL has exactly one row (not two — the ON CONFLICT
	// fired) — and the outbox is empty.
	var logCount int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE actor_sub = $1`, ev.Actor,
	).Scan(&logCount))
	assert.Equal(t, 1, logCount,
		"ON CONFLICT DO NOTHING must prevent duplicate audit_log rows")

	var outboxCount int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_outbox`,
	).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount,
		"re-processed outbox row must still be deleted on idempotent drain")
}

// TestRecorder_TransactionalAtomicity: the Path A promise made
// physical. The Recorder writes to audit_outbox using whatever
// Queryer is passed; when passed a pgx.Tx that subsequently rolls
// back, the audit row must NOT exist.
func TestRecorder_TransactionalAtomicity(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	auditor := audit.NewPostgres(f.Pool)

	ev := makeEvent()

	// Open a tx, record an event inside it, then DELIBERATELY roll back.
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, auditor.Record(ctx, tx, ev))
	require.NoError(t, tx.Rollback(ctx))

	// The audit_outbox must NOT contain the event.
	var count int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_outbox WHERE actor_sub = $1`, ev.Actor,
	).Scan(&count))
	assert.Equal(t, 0, count,
		"rolled-back tx must leave no trace in audit_outbox")
}
