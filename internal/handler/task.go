package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/internal/authz"
	"gotask/internal/domain"
	"gotask/internal/handler/dto"
	"gotask/internal/middleware"
	"gotask/internal/repository"
	"gotask/pkg/response"
)

// authedSubject returns the verified Keycloak subject as a uuid.UUID.
//
// This replaces Phase 5's stubReporterID. The seam deliberately left in
// Phase 5 — reporter/owner identity flowing in as a PARAMETER, never as a
// client-supplied wire field — is why this change is one helper and three
// call-site edits, with zero change to request shapes or service code.
//
// It is only ever called from handlers behind the auth middleware, so a
// verified subject is guaranteed present; an empty subject would mean the
// middleware was misconfigured (a protected route mounted outside the
// auth group), which is a programming error, surfaced as 401 rather than
// silently attributing data to the nil user.
//
// Under Option A the subject IS the user identity end to end: it is what
// the database stores in reporter_id/owner_id, with no local users table
// to validate it against. Keycloak is the only thing that knows the
// subject corresponds to a real user — and it already proved that by
// signing the token we verified.
func (h *Handler) authedSubject(r *http.Request) (uuid.UUID, error) {
	sub := middleware.UserSubjectFromContext(r.Context())
	if sub == "" {
		return uuid.Nil, apperror.Unauthorized("no authenticated user on request")
	}
	id, err := uuid.Parse(sub)
	if err != nil {
		// Keycloak subjects are UUIDs by default. A non-UUID subject
		// means the realm is configured with a different subject format
		// than this app's schema (UUID columns) expects — a setup error,
		// not a client error.
		return uuid.Nil, apperror.Unauthorized("token subject is not a valid user id")
	}
	return id, nil
}

// authedClaims returns the verified claims. The auth middleware guarantees
// these are present on every protected route; we still treat absence as
// 401 (fail safe) rather than panicking, because a misconfigured route
// mounted outside the auth group is the only realistic way to get here
// without claims, and 401 is the honest answer.
func (h *Handler) authedClaims(r *http.Request) (authz.Claims, error) {
	c, ok := middleware.UserClaimsFromContext(r.Context())
	if !ok || c.Subject == "" {
		return authz.Claims{}, apperror.Unauthorized("no authenticated user on request")
	}
	return c, nil
}

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

	// The reporter is the VERIFIED authenticated user, read from the
	// request context (put there by the auth middleware). It is supplied
	// here as a parameter, never taken from the request body — a client
	// cannot forge authorship.
	reporter, err := h.authedSubject(r)
	if err != nil {
		h.respondError(w, r, err)
		return
	}
	created, err := h.taskSvc.Create(r.Context(), req.ToServiceInput(reporter))
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

	claims, err := h.authedClaims(r)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	updated, err := h.taskSvc.UpdateStatus(r.Context(), claims, id, domain.Status(req.Status))
	if err != nil {
		// The service returns:
		//  - *service.TransitionError / ErrInvalidTransition → 409
		//  - service.ErrForbidden (you don't own this task)   → 403
		//  - *service.ValidationError (unknown status)        → 422
		// respondError already maps all of these.
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

	claims, err := h.authedClaims(r)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	if err := h.taskSvc.Delete(r.Context(), claims, id); err != nil {
		// Service returns ErrForbidden if claims don't allow it → 403.
		h.respondError(w, r, err)
		return
	}

	// 204: success, no body. The resource is gone; there is nothing
	// meaningful to return, and a body on a 204 is a protocol error.
	w.WriteHeader(http.StatusNoContent)
}
