// Package audit records authorization-relevant actions taken by users.
//
// Phase 8 reshape: durable outbox pattern.
//
// Why the redesign:
//
//   - Phase 7's Recorder wrote directly to audit_log on the request hot
//     path. A slow audit DB (or network blip) became user-visible
//     latency on every mutation; worse, an app crash between the
//     business write and the audit write produced an audit-log gap.
//
//   - Phase 8 Path A (chosen deliberately) routes audit writes through
//     the audit_outbox table IN THE SAME DATABASE TRANSACTION as the
//     business write. Atomicity: both rows live or both die. A
//     background Drainer goroutine moves rows from audit_outbox to
//     audit_log asynchronously. The caller commits and returns
//     immediately; durability is unaffected by drainer state.
//
//   - The Reader half is unchanged: GET /api/v1/audit still reads from
//     audit_log directly. That table is the authoritative, queryable
//     audit history.
//
// Layering note:
//
//   - Recorder.Record now takes a Queryer (typically a pgx.Tx the
//     service obtained via database.WithTx). The "Recorder writes
//     atomically with whatever transaction you give it" contract is
//     what makes the outbox pattern work.
//
//   - The Drainer is a separate type with its own lifecycle, started
//     from main.go alongside the HTTP server. See drainer.go.
//
//   - The FakeAuditor (used in service tests) still implements
//     Recorder; it ignores the Queryer parameter and just appends to
//     an in-memory slice, which is exactly what the tests need.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"gotask/internal/database"
)

// Action is the small, closed vocabulary of things we audit.
const (
	ActionTaskCreated       = "task.created"
	ActionTaskDeleted       = "task.deleted"
	ActionTaskStatusChanged = "task.status_changed"
	ActionProjectCreated    = "project.created"
)

// Outcome — closed set. 'denied' is audited deliberately.
const (
	OutcomeSuccess = "success"
	OutcomeDenied  = "denied"
)

// Target identifies the resource an event was about.
type Target struct {
	Kind string
	ID   uuid.UUID
}

// Event is the value the service emits to the auditor. Detail is a
// free-form map (serialised to JSONB).
type Event struct {
	Actor     uuid.UUID
	Action    string
	Target    Target
	Outcome   string
	Detail    map[string]any
	RequestID string
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

// Query narrows a Read. Zero-value fields mean "no constraint".
type Query struct {
	Actor  uuid.UUID
	Action string
	Target Target
	Limit  int
}

// Recorder writes audit events to the OUTBOX. The runner must be the
// SAME transaction (or pool) the business write used, so the outbox
// insert is atomic with that write.
//
// In production, callers always pass a pgx.Tx obtained from
// database.WithTx. The fake ignores the parameter.
type Recorder interface {
	Record(ctx context.Context, runner database.Queryer, e Event) error
}

// Reader reads from audit_log (the durable, queryable history table).
// Read calls are NOT inside any business transaction; they're plain
// pool queries.
type Reader interface {
	List(ctx context.Context, q Query) ([]Entry, error)
}

// Auditor is the union — main.go typically wires one value satisfying
// both. The split is for clarity: services depend on Recorder; the
// handler that lists audit history depends on Reader. Neither needs to
// know about the other half.
type Auditor interface {
	Recorder
	Reader
}

// marshalDetail / unmarshalDetail centralise the JSONB <-> map
// translation.
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
