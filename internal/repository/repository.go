// Package repository is the boundary between domain types and database
// rows. Nothing outside this package writes SQL.
//
// The most important thing in this file is that callers depend on the
// INTERFACES (TaskRepository, ProjectRepository), never on the concrete
// Postgres implementations. That single indirection is what lets the
// Phase 4 service layer be unit-tested against an in-memory fake in
// microseconds, with no database running. The Postgres types
// (taskPostgres, projectPostgres) are unexported; only the constructors
// and interfaces are visible to the rest of the application.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"gotask/internal/domain"
)

// TaskFilter narrows a List query. Zero-value fields mean "no filter on
// this dimension": an empty ProjectID does not constrain by project, an
// empty Status does not constrain by status, and so on. This keeps the
// common case (list everything) free of special-casing while allowing
// precise queries without a combinatorial explosion of methods.
type TaskFilter struct {
	ProjectID  uuid.UUID
	AssigneeID uuid.UUID
	Status     domain.Status

	// Limit / Offset implement simple pagination. A Limit of 0 is
	// treated by the implementation as "the default page size", never
	// as "return zero rows" — returning nothing for an unset limit is a
	// footgun.
	Limit  int
	Offset int
}

// UpdateTaskInput carries the mutable fields of a task. Pointer fields
// distinguish "not provided, leave unchanged" (nil) from "provided, set
// to this value" (non-nil, including a deliberate zero value). This is
// the one place pointers-as-optional is the right tool: a PATCH must be
// able to express "clear the assignee" (set to nil UUID) distinctly from
// "don't touch the assignee".
type UpdateTaskInput struct {
	Title       *string
	Description *string
	Status      *domain.Status
	Priority    *domain.Priority
	AssigneeID  *uuid.UUID // points to uuid.Nil to explicitly unassign
	DueDate     *time.Time // points to zero time to explicitly clear
}

// TaskRepository is the persistence contract for tasks. The service layer
// depends on this interface. The Postgres implementation is one possible
// satisfier; an in-memory fake used in tests is another.
type TaskRepository interface {
	Create(ctx context.Context, in domain.NewTaskInput) (domain.Task, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Task, error)
	List(ctx context.Context, f TaskFilter) ([]domain.Task, error)
	Update(ctx context.Context, id uuid.UUID, in UpdateTaskInput) (domain.Task, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// ProjectRepository is the persistence contract for projects. Tasks
// reference projects by foreign key, so this is needed before tasks can
// do anything meaningful.
type ProjectRepository interface {
	Create(ctx context.Context, in domain.NewProjectInput) (domain.Project, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Project, error)
	List(ctx context.Context) ([]domain.Project, error)
	Delete(ctx context.Context, id uuid.UUID) error
}
