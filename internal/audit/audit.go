// Package audit records authorization-relevant actions taken by users.
//
// Why a package rather than just a table:
//
//   - The SHAPE of an audit event ("actor, action, target, outcome,
//     detail") is universal; it does not belong inside any one service.
//   - The Auditor INTERFACE lets the service receive an audit sink it
//     does not own. In production the sink writes to Postgres. In tests
//     the sink is a fake that captures calls so tests can assert "the
//     service emitted EXACTLY this event and no others." This is the
//     same fakes-first discipline we used for repositories.
//   - Reading audit history is a separate concern from writing it; the
//     Reader interface is small and lives alongside Recorder so the
//     handler can consume it without dragging the writer along.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Action is the small, closed vocabulary of things we audit. Adding a
// new audited operation = adding a constant here. Centralising prevents
// "task.deleted" in one file and "task_deleted" in another from
// producing two different audit streams for the same thing.
const (
	ActionTaskCreated        = "task.created"
	ActionTaskDeleted        = "task.deleted"
	ActionTaskStatusChanged  = "task.status_changed"
	ActionProjectCreated     = "project.created"
)

// Outcome is the closed set of audit outcomes. We deliberately record
// 'denied' as well as 'success' — security reviews care equally about
// attempts that failed authorization, not just successes.
const (
	OutcomeSuccess = "success"
	OutcomeDenied  = "denied"
)

// Target identifies the resource an event was about.
type Target struct {
	Kind string    // "task" | "project"
	ID   uuid.UUID
}

// Event is the value the service emits to the auditor. Detail is a
// free-form map (serialised to JSONB) so different actions can carry
// their own structured fields without growing the table schema. The
// service decides what goes in Detail; the auditor just stores it.
type Event struct {
	Actor     uuid.UUID
	Action    string
	Target    Target
	Outcome   string
	Detail    map[string]any
	RequestID string // ties this event to the access-log line for the same HTTP request
}

// Entry is what Read returns: an Event plus the database-assigned fields.
type Entry struct {
	ID         uuid.UUID
	OccurredAt time.Time
	Actor      uuid.UUID
	Action     string
	Target     Target
	Outcome    string
	Detail     map[string]any
	RequestID  string
}

// Query narrows a Read. Zero-value fields mean "no constraint on this
// dimension" — same convention as repository.TaskFilter.
type Query struct {
	Actor   uuid.UUID // uuid.Nil → all actors
	Action  string    // "" → all actions
	Target  Target    // zero kind/id → all targets
	Limit   int       // <=0 → service default
}

// Recorder writes audit events. Implementations must be safe to call
// concurrently. A Record call that fails should NOT block the caller's
// business operation from completing — auditing is important, but the
// chosen architecture is "service performs operation, then records" and
// a failed record is logged, not rethrown. (The service decides this;
// the interface allows either treatment.)
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Reader reads audit history.
type Reader interface {
	List(ctx context.Context, q Query) ([]Entry, error)
}

// Auditor is the union — the typical injection in main.go is a single
// value satisfying both.
type Auditor interface {
	Recorder
	Reader
}

// marshalDetail / unmarshalDetail centralise the JSONB <-> map[string]any
// translation so implementations don't reinvent it. A nil/empty map maps
// to SQL NULL (so the JSONB column isn't burdened with "{}" rows).
func marshalDetail(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func unmarshalDetail(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
