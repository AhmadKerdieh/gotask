// Package domain holds the application's core data types.
//
// Types in this package are deliberately "anaemic": they carry data, not
// behaviour. They have no JSON tags, no database tags, no validation tags,
// and no methods that perform I/O.
//
// The reasoning:
//
//   - The same Task value flows between the handler, service, and
//     repository layers. If we tagged it with json:"task_id" the service
//     would be implicitly coupled to that wire shape; if we tagged it with
//     db:"task_uuid" the handler would be coupled to schema details.
//   - HTTP-edge types ("DTOs") live in internal/handler/dto and convert
//     between the wire shape and these domain types.
//   - Database-edge types (or struct tags, depending on the driver) live
//     alongside the repository in Phase 3.
//
// Methods on domain types are okay when they're pure functions over the
// fields — IsTerminal, IsOverdue, AllowedTransitions. Anything that needs
// a database or HTTP client belongs in a service, not here.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of a Task.
//
// The Go constants below are the *known* statuses — they exist for IDE
// autocomplete, compile-time field separation from Priority, and method
// attachment (see IsTerminal). They are NOT the authoritative list of
// legal statuses. That list lives in workflow.yaml and is enforced at
// validation time via internal/config.Workflow.
//
// In other words: the constants are convenient names; the YAML is the law.
// If an admin removes "blocked" from workflow.yaml, the constant
// StatusBlocked still compiles, but the validator rejects any request
// that uses it.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusInReview   Status = "in_review"
	StatusDone       Status = "done"
	StatusCancelled  Status = "cancelled"
)

// String lets Status satisfy fmt.Stringer and lets log/slog format it
// naturally without the type prefix.
func (s Status) String() string { return string(s) }

// Priority is how urgent a Task is.
//
// Same rules as Status: the constants are convenience, workflow.yaml is
// authority.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

func (p Priority) String() string { return string(p) }

// Task is the central entity of the application.
//
// Fields with zero values that are semantically meaningful (e.g.
// AssigneeID = uuid.Nil = "unassigned", DueDate = time.Time{} = "no
// deadline") use the zero value rather than pointer types. This is a
// deliberate choice — pointer-as-nullable adds heap allocations and a
// nil-check at every read site. The trade-off is that we can't distinguish
// "not set" from "explicitly set to zero" in JSON payloads, which we
// handle at the DTO layer (Phase 5) using pointer fields *there*, not
// here.
type Task struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	Title       string
	Description string
	Status      Status
	Priority    Priority
	AssigneeID  uuid.UUID // uuid.Nil when unassigned
	ReporterID  uuid.UUID
	DueDate     time.Time // zero value when no deadline
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewTaskInput is the data needed to create a Task. It does not have an
// ID, CreatedAt, or UpdatedAt — those are assigned by the service /
// repository at insert time.
//
// This is a *domain* input type, distinct from the *HTTP* request DTO
// (CreateTaskRequest in handler/dto, Phase 5). The DTO has JSON tags and
// validate tags; this type has neither. The handler validates the DTO,
// converts it to this, and passes it to the service.
type NewTaskInput struct {
	ProjectID   uuid.UUID
	Title       string
	Description string
	Status      Status    // optional; service falls back to workflow default
	Priority    Priority  // optional; service falls back to workflow default
	AssigneeID  uuid.UUID // uuid.Nil = unassigned
	ReporterID  uuid.UUID
	DueDate     time.Time // zero = no deadline
}
