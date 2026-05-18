package dto

import (
	"time"

	"github.com/google/uuid"

	"gotask/internal/domain"
	"gotask/internal/service"
)

// CreateTaskRequest is exactly what a client may send to create a task.
//
// Note what is ABSENT: reporter_id. Authorship is the authenticated
// user, not a client-supplied value — letting a caller set it would let
// them forge who created a task. Until Keycloak (Phase 6) the handler
// stubs the reporter to a fixed user; the field never appears on the
// wire either way.
//
// status and priority are optional (omitempty + the custom workflow
// validators). Empty means "let the service apply the workflow default"
// — that optionality is a business rule, expressed here only as "not
// required", and actually applied in the service.
type CreateTaskRequest struct {
	ProjectID   uuid.UUID `json:"project_id"  validate:"required"`
	Title       string    `json:"title"       validate:"required,min=1,max=200"`
	Description string    `json:"description" validate:"max=10000"`
	Status      string    `json:"status"      validate:"omitempty,status"`
	Priority    string    `json:"priority"    validate:"omitempty,priority"`
	AssigneeID  uuid.UUID `json:"assignee_id" validate:"omitempty"`
}

// ToServiceInput converts the validated wire request into the service's
// business-input shape. reporterID is supplied by the handler (the
// authenticated user; stubbed pre-Phase-6), NOT by the client — which is
// the entire reason this is a function parameter and not a request field.
func (r CreateTaskRequest) ToServiceInput(reporterID uuid.UUID) service.CreateTaskInput {
	return service.CreateTaskInput{
		ProjectID:   r.ProjectID,
		Title:       r.Title,
		Description: r.Description,
		Status:      domain.Status(r.Status),
		Priority:    domain.Priority(r.Priority),
		AssigneeID:  r.AssigneeID,
		ReporterID:  reporterID,
	}
}

// UpdateTaskStatusRequest is the body of PATCH /tasks/{id}/status. Status
// changes are their own endpoint precisely because they are the one
// mutation governed by the workflow transition rules — isolating them
// gives that rule a single guarded entry point.
type UpdateTaskStatusRequest struct {
	Status string `json:"status" validate:"required,status"`
}

// TaskResponse is exactly what the API returns for a task. It is a
// separate type from domain.Task so the wire format is stable even if
// the domain or schema grows internal fields. Absent values use the
// wire's natural representation: empty string for unassigned/no-deadline
// rather than a zero UUID or 0001-01-01, which would leak the domain's
// zero-value convention to clients.
type TaskResponse struct {
	ID          uuid.UUID `json:"id"`
	ProjectID   uuid.UUID `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	AssigneeID  *string   `json:"assignee_id"` // null when unassigned
	ReporterID  uuid.UUID `json:"reporter_id"`
	DueDate     *string   `json:"due_date"`    // null when no deadline
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewTaskResponse maps a domain.Task to its wire shape, translating the
// domain's zero-value "absence" convention into JSON null.
func NewTaskResponse(t domain.Task) TaskResponse {
	resp := TaskResponse{
		ID:          t.ID,
		ProjectID:   t.ProjectID,
		Title:       t.Title,
		Description: t.Description,
		Status:      string(t.Status),
		Priority:    string(t.Priority),
		ReporterID:  t.ReporterID,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
	if t.AssigneeID != uuid.Nil {
		s := t.AssigneeID.String()
		resp.AssigneeID = &s
	}
	if !t.DueDate.IsZero() {
		s := t.DueDate.Format(time.RFC3339)
		resp.DueDate = &s
	}
	return resp
}

// NewTaskListResponse maps a slice. It returns a non-nil empty slice for
// "no tasks" so the JSON is [] not null — a recurring frontend-bug
// source otherwise.
func NewTaskListResponse(tasks []domain.Task) []TaskResponse {
	out := make([]TaskResponse, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, NewTaskResponse(t))
	}
	return out
}
