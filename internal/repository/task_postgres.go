package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gotask/internal/domain"
)

// taskPostgres is the Postgres-backed TaskRepository. Unexported for the
// same reason as projectPostgres: the storage technology is a private
// detail behind the TaskRepository interface.
type taskPostgres struct {
	pool *pgxpool.Pool
}

// NewTaskRepository returns a Postgres-backed TaskRepository as the
// interface type.
func NewTaskRepository(pool *pgxpool.Pool) TaskRepository {
	return &taskPostgres{pool: pool}
}

const taskColumns = `id, project_id, title, description, status, priority,
	assignee_id, reporter_id, due_date, created_at, updated_at`

// scanTask maps one row into a domain.Task, translating SQL NULL into the
// domain's zero-value convention:
//
//   - assignee_id NULL  → uuid.Nil   ("unassigned")
//   - due_date    NULL  → time.Time{} ("no deadline")
//
// The domain model deliberately uses zero values rather than pointers for
// these (see domain/task.go); the NULL <-> zero translation is the
// repository's job and is contained entirely here. pgx scans a nullable
// uuid/timestamp into the pointer types below; we then collapse nil to
// the zero value.
func scanTask(row pgx.Row) (domain.Task, error) {
	var (
		t          domain.Task
		assigneeID *uuid.UUID
		dueDate    *time.Time
	)
	err := row.Scan(
		&t.ID,
		&t.ProjectID,
		&t.Title,
		&t.Description,
		&t.Status,
		&t.Priority,
		&assigneeID,
		&t.ReporterID,
		&dueDate,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return domain.Task{}, err
	}
	if assigneeID != nil {
		t.AssigneeID = *assigneeID
	}
	if dueDate != nil {
		t.DueDate = *dueDate
	}
	return t, nil
}

// nilUUIDToNull / zeroTimeToNull convert the domain's zero-value
// "absence" convention back into SQL NULL for writes. Passing uuid.Nil
// directly would store the all-zeros UUID as a real value and break the
// assignee_id foreign key; passing a zero time.Time would store
// 0001-01-01 instead of NULL.
func nilUUIDToNull(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func zeroTimeToNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (r *taskPostgres) Create(ctx context.Context, in domain.NewTaskInput) (domain.Task, error) {
	const q = `
		INSERT INTO tasks
			(project_id, title, description, status, priority,
			 assignee_id, reporter_id, due_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + taskColumns

	row := r.pool.QueryRow(ctx, q,
		in.ProjectID,
		in.Title,
		in.Description,
		in.Status,
		in.Priority,
		nilUUIDToNull(in.AssigneeID),
		in.ReporterID,
		zeroTimeToNull(in.DueDate),
	)

	t, err := scanTask(row)
	if err != nil {
		return domain.Task{}, translateError(err, "task")
	}
	return t, nil
}

func (r *taskPostgres) GetByID(ctx context.Context, id uuid.UUID) (domain.Task, error) {
	const q = `SELECT ` + taskColumns + ` FROM tasks WHERE id = $1`

	t, err := scanTask(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return domain.Task{}, translateError(err, "task")
	}
	return t, nil
}

func (r *taskPostgres) List(ctx context.Context, f TaskFilter) ([]domain.Task, error) {
	// The query is built incrementally so each filter is optional without
	// needing a separate method per combination. $N placeholders are
	// numbered as arguments are appended; values go into args in the same
	// order. This is fully parameterised — values are never concatenated
	// into the SQL string, so it is not an injection vector.
	var (
		conds []string
		args  []any
	)
	// next returns the placeholder index for the argument about to be
	// appended (1-based, matching Postgres $N numbering).
	next := func() int { return len(args) + 1 }

	if f.ProjectID != uuid.Nil {
		conds = append(conds, fmt.Sprintf("project_id = $%d", next()))
		args = append(args, f.ProjectID)
	}
	if f.AssigneeID != uuid.Nil {
		conds = append(conds, fmt.Sprintf("assignee_id = $%d", next()))
		args = append(args, f.AssigneeID)
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("status = $%d", next()))
		args = append(args, f.Status)
	}

	q := `SELECT ` + taskColumns + ` FROM tasks`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY created_at DESC"

	// A zero Limit means "default page size", never "zero rows".
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT $%d", next())
	args = append(args, limit)

	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET $%d", next())
		args = append(args, f.Offset)
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, translateError(err, "task")
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, translateError(err, "task")
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError(err, "task")
	}
	return tasks, nil
}

func (r *taskPostgres) Update(ctx context.Context, id uuid.UUID, in UpdateTaskInput) (domain.Task, error) {
	// Build a partial UPDATE: only the fields the caller actually set
	// (non-nil pointers) are written. A PATCH that touches only the title
	// must not overwrite status with a zero value — that is the whole
	// reason UpdateTaskInput uses pointers.
	var (
		sets []string
		args []any
		n    = 1
	)
	set := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, n))
		args = append(args, val)
		n++
	}

	if in.Title != nil {
		set("title", *in.Title)
	}
	if in.Description != nil {
		set("description", *in.Description)
	}
	if in.Status != nil {
		set("status", *in.Status)
	}
	if in.Priority != nil {
		set("priority", *in.Priority)
	}
	if in.AssigneeID != nil {
		// A non-nil pointer to uuid.Nil is the explicit "unassign"
		// signal; collapse it to SQL NULL.
		set("assignee_id", nilUUIDToNull(*in.AssigneeID))
	}
	if in.DueDate != nil {
		set("due_date", zeroTimeToNull(*in.DueDate))
	}

	// Nothing to update: rather than issue a no-op UPDATE (which would
	// still bump updated_at via the trigger and lie about a change), just
	// return the current row.
	if len(sets) == 0 {
		return r.GetByID(ctx, id)
	}

	// updated_at is maintained by the database trigger, so it is
	// deliberately NOT in the SET list. The id placeholder is the last
	// argument.
	q := fmt.Sprintf(
		`UPDATE tasks SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(sets, ", "), n, taskColumns,
	)
	args = append(args, id)

	t, err := scanTask(r.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.Task{}, translateError(err, "task")
	}
	return t, nil
}

func (r *taskPostgres) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM tasks WHERE id = $1`

	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return translateError(err, "task")
	}
	if tag.RowsAffected() == 0 {
		return translateError(pgx.ErrNoRows, "task")
	}
	return nil
}
