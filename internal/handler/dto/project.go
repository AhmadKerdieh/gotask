package dto

import (
	"time"

	"github.com/google/uuid"

	"gotask/internal/domain"
	"gotask/internal/service"
)

// CreateProjectRequest is what a client may send to create a project.
//
// owner_id is absent for the same reason reporter_id is absent from
// CreateTaskRequest: the owner is the authenticated user, not a
// client-chosen value. Stubbed by the handler until Phase 6.
//
// The key is accepted as the client sends it; the SERVICE normalises it
// (uppercase, trim) — normalisation is a business rule, so the DTO does
// not pre-empt it, it only bounds the length so an absurd payload is
// rejected early.
type CreateProjectRequest struct {
	Key         string `json:"key"         validate:"required,max=10"`
	Name        string `json:"name"        validate:"required,max=200"`
	Description string `json:"description" validate:"max=10000"`
}

// ToServiceInput converts the validated request into the service input.
// ownerID is the authenticated user, supplied by the handler.
func (r CreateProjectRequest) ToServiceInput(ownerID uuid.UUID) service.CreateProjectInput {
	return service.CreateProjectInput{
		Key:         r.Key,
		Name:        r.Name,
		Description: r.Description,
		OwnerID:     ownerID,
	}
}

// ProjectResponse is exactly what the API returns for a project.
type ProjectResponse struct {
	ID          uuid.UUID `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerID     uuid.UUID `json:"owner_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewProjectResponse maps a domain.Project to its wire shape.
func NewProjectResponse(p domain.Project) ProjectResponse {
	return ProjectResponse{
		ID:          p.ID,
		Key:         p.Key,
		Name:        p.Name,
		Description: p.Description,
		OwnerID:     p.OwnerID,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

// NewProjectListResponse maps a slice, empty-but-non-nil for "none".
func NewProjectListResponse(ps []domain.Project) []ProjectResponse {
	out := make([]ProjectResponse, 0, len(ps))
	for _, p := range ps {
		out = append(out, NewProjectResponse(p))
	}
	return out
}
