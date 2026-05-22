package audit

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"gotask/internal/database"
)

// PostgresAuditor satisfies both Recorder and Reader.
//
// Record writes to audit_outbox using whatever Queryer the caller
// provides (typically a pgx.Tx the service obtained via
// database.WithTx). That's how the outbox row gets committed atomically
// with the business write.
//
// List reads from audit_log using the connection pool — list calls
// are not part of any business transaction, they're plain queries
// against the durable, drained table.
//
// The split is deliberate: a single struct, two responsibilities at
// two different lifecycles. The Recorder responsibility is per-request
// (scoped to a transaction); the Reader responsibility is per-request
// too but as a fresh pool query. They share the marshal/unmarshal
// helpers and nothing else.
type PostgresAuditor struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a PostgresAuditor. The pool is used by List;
// Record uses whatever Queryer is passed to it on each call.
func NewPostgres(pool *pgxpool.Pool) *PostgresAuditor {
	return &PostgresAuditor{pool: pool}
}

// Record writes the event into the audit_outbox table using the
// provided Queryer. Callers are expected to pass the same pgx.Tx that
// is running their business write, so the outbox row is part of the
// same transaction; rolling back the business write rolls back the
// outbox row automatically.
//
// The id is generated here (not by the database). Reusing this id as
// the eventual audit_log primary key is what makes the drainer
// idempotent: a duplicate drain is a primary-key conflict, which the
// drainer treats as success.
func (a *PostgresAuditor) Record(ctx context.Context, runner database.Queryer, e Event) error {
	detail, err := marshalDetail(e.Detail)
	if err != nil {
		return fmt.Errorf("audit: marshal detail: %w", err)
	}

	const q = `
		INSERT INTO audit_outbox
			(id, actor_sub, action, target_kind, target_id, outcome, detail, request_id)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err = runner.Exec(ctx, q,
		uuid.New(),
		e.Actor,
		e.Action,
		e.Target.Kind,
		e.Target.ID,
		e.Outcome,
		detail,
		nullableString(e.RequestID),
	)
	return err
}

const auditLogColumns = `id, occurred_at, actor_sub, action, target_kind, target_id, outcome, detail, request_id`

// List reads from audit_log directly. This table is populated by the
// Drainer; the read path does not touch audit_outbox.
func (a *PostgresAuditor) List(ctx context.Context, q Query) ([]Entry, error) {
	var (
		conds []string
		args  []any
	)
	next := func() int { return len(args) + 1 }

	if q.Actor != uuid.Nil {
		conds = append(conds, fmt.Sprintf("actor_sub = $%d", next()))
		args = append(args, q.Actor)
	}
	if q.Action != "" {
		conds = append(conds, fmt.Sprintf("action = $%d", next()))
		args = append(args, q.Action)
	}
	if q.Target.Kind != "" {
		conds = append(conds, fmt.Sprintf("target_kind = $%d", next()))
		args = append(args, q.Target.Kind)
	}
	if q.Target.ID != uuid.Nil {
		conds = append(conds, fmt.Sprintf("target_id = $%d", next()))
		args = append(args, q.Target.ID)
	}

	sql := `SELECT ` + auditLogColumns + ` FROM audit_log`
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	sql += " ORDER BY occurred_at DESC"

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	sql += fmt.Sprintf(" LIMIT $%d", next())
	args = append(args, limit)

	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Entry, 0)
	for rows.Next() {
		var (
			e         Entry
			detailRaw []byte
			reqID     *string
		)
		if err := rows.Scan(
			&e.ID, &e.OccurredAt,
			&e.Actor, &e.Action,
			&e.Target.Kind, &e.Target.ID,
			&e.Outcome, &detailRaw, &reqID,
		); err != nil {
			return nil, err
		}
		if reqID != nil {
			e.RequestID = *reqID
		}
		e.Detail, err = unmarshalDetail(detailRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
