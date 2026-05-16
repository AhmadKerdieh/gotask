package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gotask/internal/domain"
)

// projectPostgres is the Postgres-backed ProjectRepository. It is
// unexported: callers receive it as the ProjectRepository interface from
// NewProjectRepository and never name the concrete type. This keeps the
// storage technology a private implementation detail.
type projectPostgres struct {
	pool *pgxpool.Pool
}

// NewProjectRepository returns a Postgres-backed ProjectRepository. The
// return type is the interface, not *projectPostgres — the compiler
// enforces that callers cannot depend on Postgres-specific behaviour.
func NewProjectRepository(pool *pgxpool.Pool) ProjectRepository {
	return &projectPostgres{pool: pool}
}

// projectColumns is the canonical column list, declared once so the
// SELECT list and the Scan order can never drift apart. Every query that
// returns a project selects exactly these, in this order, and scanProject
// reads them in the same order.
const projectColumns = `id, key, name, description, owner_id, created_at, updated_at`

// scanProject reads one row of projectColumns into a domain.Project. A
// pgx.Row (single row) and pgx.Rows (iteration) both satisfy the narrow
// interface this needs, so one function serves Get and List.
func scanProject(row pgx.Row) (domain.Project, error) {
	var p domain.Project
	err := row.Scan(
		&p.ID,
		&p.Key,
		&p.Name,
		&p.Description,
		&p.OwnerID,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	return p, err
}

func (r *projectPostgres) Create(ctx context.Context, in domain.NewProjectInput) (domain.Project, error) {
	// The database owns identity: we do NOT send an id. RETURNING brings
	// back the generated id and timestamps in the same round-trip, so
	// there is no second SELECT to read what we just wrote.
	const q = `
		INSERT INTO projects (key, name, description, owner_id)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + projectColumns

	row := r.pool.QueryRow(ctx, q,
		in.Key,
		in.Name,
		in.Description,
		in.OwnerID,
	)

	p, err := scanProject(row)
	if err != nil {
		return domain.Project{}, translateError(err, "project")
	}
	return p, nil
}

func (r *projectPostgres) GetByID(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	const q = `SELECT ` + projectColumns + ` FROM projects WHERE id = $1`

	p, err := scanProject(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return domain.Project{}, translateError(err, "project")
	}
	return p, nil
}

func (r *projectPostgres) List(ctx context.Context) ([]domain.Project, error) {
	const q = `SELECT ` + projectColumns + ` FROM projects ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, translateError(err, "project")
	}
	defer rows.Close()

	// Initialise to an empty (non-nil) slice so the JSON encoder emits
	// [] rather than null for "no projects". A null where the client
	// expects an array is a recurring source of frontend bugs.
	projects := make([]domain.Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, translateError(err, "project")
		}
		projects = append(projects, p)
	}
	// rows.Err() surfaces an error that aborted iteration partway (e.g.
	// the connection dropped, or the context was cancelled mid-scan).
	// Without this check a truncated result set looks like a complete
	// short one.
	if err := rows.Err(); err != nil {
		return nil, translateError(err, "project")
	}
	return projects, nil
}

func (r *projectPostgres) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM projects WHERE id = $1`

	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return translateError(err, "project")
	}
	// Exec does not error when zero rows match — DELETE of a
	// non-existent id is "successful" at the SQL level. We want the API
	// to return 404 in that case, so we inspect the rows-affected count
	// and synthesise a not-found.
	if tag.RowsAffected() == 0 {
		return translateError(pgx.ErrNoRows, "project")
	}
	return nil
}
