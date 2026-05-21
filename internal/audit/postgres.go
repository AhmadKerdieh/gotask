package audit

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresAuditor writes to and reads from the audit_log table created
// by migration 000003. Constructed once at startup and shared; safe for
// concurrent use because *pgxpool.Pool is.
type PostgresAuditor struct {
	pool *pgxpool.Pool
}

// NewPostgres returns the production Auditor.
func NewPostgres(pool *pgxpool.Pool) *PostgresAuditor {
	return &PostgresAuditor{pool: pool}
}

const auditColumns = `id, occurred_at, actor_sub, action, target_kind, target_id, outcome, detail, request_id`

func (a *PostgresAuditor) Record(ctx context.Context, e Event) error {
	detail, err := marshalDetail(e.Detail)
	if err != nil {
		// A malformed detail should not silently drop the audit row.
		// Wrap so the caller knows it was a serialisation issue, not a
		// database one.
		return fmt.Errorf("audit: marshal detail: %w", err)
	}

	const q = `
		INSERT INTO audit_log
			(actor_sub, action, target_kind, target_id, outcome, detail, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err = a.pool.Exec(ctx, q,
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

func (a *PostgresAuditor) List(ctx context.Context, q Query) ([]Entry, error) {
	var (
		conds []string
		args  []any
	)
	next := func() int { return len(args) + 1 }

	if q.Actor != [16]byte{} { // uuid.Nil
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
	if q.Target.ID != [16]byte{} {
		conds = append(conds, fmt.Sprintf("target_id = $%d", next()))
		args = append(args, q.Target.ID)
	}

	sql := `SELECT ` + auditColumns + ` FROM audit_log`
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

// nullableString returns nil for empty strings so the column stores SQL
// NULL rather than ''. Symmetric with how the repository handles
// nullable columns; keeps querying with "request_id IS NULL" honest.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
