package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/internal/handler/dto"
	"gotask/pkg/response"
)

// CreateProject handles POST /api/v1/projects.
func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateProjectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.respondError(w, r, err)
		return
	}
	if fields := h.validator.Struct(&req); fields != nil {
		h.respondError(w, r, apperror.Validation(fields))
		return
	}

	// owner is the (stubbed) authenticated user — supplied here, never
	// from the request body. Same seam as task reporter.
	created, err := h.projectSvc.Create(r.Context(), req.ToServiceInput(stubReporterID))
	if err != nil {
		// Duplicate key surfaces as service.ErrConflict → 409 via
		// respondError.
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusCreated, dto.NewProjectResponse(created))
}

// GetProject handles GET /api/v1/projects/{id}.
func (h *Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.respondError(w, r, apperror.BadRequest("id must be a valid UUID"))
		return
	}

	p, err := h.projectSvc.Get(r.Context(), id)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, dto.NewProjectResponse(p))
}

// ListProjects handles GET /api/v1/projects.
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := h.projectSvc.List(r.Context())
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, dto.NewProjectListResponse(ps))
}
