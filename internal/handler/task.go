package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/internal/domain"
	"gotask/internal/handler/dto"
	"gotask/internal/repository"
	"gotask/pkg/response"
)

// stubReporterID is the placeholder "authenticated user" used until
// Keycloak lands in Phase 6. Every created task/project is attributed to
// this id. It matches the seed user the migration helper inserts so the
// foreign keys resolve. The moment auth exists, this constant is replaced
// by the user id from the verified token — and nothing else in these
// handlers changes, because reporter/owner identity already flows in as
// a parameter, never from the client.
var stubReporterID = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")

// CreateTask handles POST /api/v1/tasks.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateTaskRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.respondError(w, r, err)
		return
	}
	if fields := h.validator.Struct(&req); fields != nil {
		h.respondError(w, r, apperror.Validation(fields))
		return
	}

	// Wire → service shape. The reporter is the (stubbed) authenticated
	// user, supplied here, never taken from the request body.
	created, err := h.taskSvc.Create(r.Context(), req.ToServiceInput(stubReporterID))
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusCreated, dto.NewTaskResponse(created))
}

// GetTask handles GET /api/v1/tasks/{id}.
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.respondError(w, r, apperror.BadRequest("id must be a valid UUID"))
		return
	}

	t, err := h.taskSvc.Get(r.Context(), id)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, dto.NewTaskResponse(t))
}

// ListTasks handles GET /api/v1/tasks with optional ?project_id=,
// ?status=, ?assignee_id=, ?limit=, ?offset= filters. Unknown or
// malformed filter values are treated as "no filter on that dimension"
// rather than erroring — a forgiving read path is the right default for
// a list endpoint; the service still rejects nonsense it cares about.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repository.TaskFilter{}

	if v := q.Get("project_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.ProjectID = id
		}
	}
	if v := q.Get("assignee_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.AssigneeID = id
		}
	}
	if v := q.Get("status"); v != "" {
		f.Status = domain.Status(v)
	}

	tasks, err := h.taskSvc.List(r.Context(), f)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, dto.NewTaskListResponse(tasks))
}

// UpdateTaskStatus handles PATCH /api/v1/tasks/{id}/status.
//
// Status is its own sub-resource, not part of a general task update,
// because it is the single mutation governed by the workflow transition
// rules. Isolating it gives that rule exactly one guarded entry point —
// a general field update can never smuggle a status change past the
// workflow check.
func (h *Handler) UpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.respondError(w, r, apperror.BadRequest("id must be a valid UUID"))
		return
	}

	var req dto.UpdateTaskStatusRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.respondError(w, r, err)
		return
	}
	if fields := h.validator.Struct(&req); fields != nil {
		h.respondError(w, r, apperror.Validation(fields))
		return
	}

	updated, err := h.taskSvc.UpdateStatus(r.Context(), id, domain.Status(req.Status))
	if err != nil {
		// The service returns *service.TransitionError /
		// service.ErrInvalidTransition for an illegal move; respondError
		// already maps that to 409 invalid_transition.
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, dto.NewTaskResponse(updated))
}

// DeleteTask handles DELETE /api/v1/tasks/{id}.
func (h *Handler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.respondError(w, r, apperror.BadRequest("id must be a valid UUID"))
		return
	}

	if err := h.taskSvc.Delete(r.Context(), id); err != nil {
		h.respondError(w, r, err)
		return
	}

	// 204: success, no body. The resource is gone; there is nothing
	// meaningful to return, and a body on a 204 is a protocol error.
	w.WriteHeader(http.StatusNoContent)
}
