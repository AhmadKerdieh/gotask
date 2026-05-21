package dto

import (
	"time"

	"github.com/google/uuid"

	"gotask/internal/audit"
)

// AuditEntryResponse is the wire shape for one audit row.
//
// The Actor field has two forms intentionally: the bare Subject (always
// present, the cryptographic identity) and the optional Name/Email
// (populated via the Keycloak Admin API lookup when available). This
// mirrors the Option-A reality: the database knows only the sub, names
// come from Keycloak, and a lookup failure must NOT prevent the audit
// row from being shown. Empty Name/Email means "lookup unavailable or
// the user no longer exists in Keycloak"; the Subject still identifies
// the actor unambiguously.
type AuditEntryResponse struct {
	ID            uuid.UUID      `json:"id"`
	OccurredAt    time.Time      `json:"occurred_at"`
	ActorSubject  uuid.UUID      `json:"actor_subject"`
	ActorName     string         `json:"actor_name,omitempty"`
	ActorEmail    string         `json:"actor_email,omitempty"`
	Action        string         `json:"action"`
	TargetKind    string         `json:"target_kind"`
	TargetID      uuid.UUID      `json:"target_id"`
	Outcome       string         `json:"outcome"`
	Detail        map[string]any `json:"detail,omitempty"`
	RequestID     string         `json:"request_id,omitempty"`
}

// UserResolver is the minimal lookup the response builder needs. Defined
// here as an interface so dto stays unaware of the keycloakadmin package
// directly; the handler injects whatever satisfies it.
type UserResolver interface {
	Resolve(sub uuid.UUID) (name, email string)
}

// NewAuditEntryResponse maps one audit.Entry into the wire shape,
// optionally enriching the Actor with name/email from a UserResolver.
// If resolver is nil or returns blanks, the response carries only the
// subject — a fully working degraded mode.
func NewAuditEntryResponse(e audit.Entry, r UserResolver) AuditEntryResponse {
	resp := AuditEntryResponse{
		ID:           e.ID,
		OccurredAt:   e.OccurredAt,
		ActorSubject: e.Actor,
		Action:       e.Action,
		TargetKind:   e.Target.Kind,
		TargetID:     e.Target.ID,
		Outcome:      e.Outcome,
		Detail:       e.Detail,
		RequestID:    e.RequestID,
	}
	if r != nil {
		resp.ActorName, resp.ActorEmail = r.Resolve(e.Actor)
	}
	return resp
}

// NewAuditListResponse maps many. Non-nil empty slice for "no rows".
func NewAuditListResponse(entries []audit.Entry, r UserResolver) []AuditEntryResponse {
	out := make([]AuditEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, NewAuditEntryResponse(e, r))
	}
	return out
}
