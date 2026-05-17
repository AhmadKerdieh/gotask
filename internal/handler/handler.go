// Package handler holds the HTTP handlers for the API.
//
// All handlers are methods on the Handler struct so they share dependencies
// (logger, config, and in later phases: services, validator). New phases
// add fields to Handler rather than introducing new top-level functions —
// this keeps the wiring in one place.
//
// Handlers in this codebase are thin. They:
//
//	1. parse and validate the request,
//	2. call into a service to do work,
//	3. translate the result (or error) into an HTTP response.
//
// They do NOT contain business logic. Business logic lives in the service
// layer (added in Phase 4).
package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"gotask/internal/apperror"
	"gotask/internal/config"
	"gotask/internal/middleware"
	"gotask/internal/database"
	"gotask/internal/repository"
	"gotask/internal/service"
	"gotask/internal/validator"
	"gotask/pkg/response"
)

// Handler bundles the shared dependencies every HTTP handler needs.
// Fields are unexported; construct with New.
type Handler struct {
	log       *slog.Logger
	cfg       *config.Config
	workflow  *config.Workflow
	validator *validator.Validator

	db          *database.DB
	taskRepo    repository.TaskRepository
	projectRepo repository.ProjectRepository

	taskSvc    *service.TaskService
	projectSvc *service.ProjectService
}

// Deps is the constructor input for Handler. Using a struct (rather than
// positional arguments) means adding a dependency in a future phase is a
// one-line change at call sites — we will keep doing this as services
// and repositories arrive.
type Deps struct {
	Logger    *slog.Logger
	Config    *config.Config
	Workflow  *config.Workflow
	Validator *validator.Validator

	DB          *database.DB
	TaskRepo    repository.TaskRepository
	ProjectRepo repository.ProjectRepository

	TaskSvc    *service.TaskService
	ProjectSvc *service.ProjectService
}

// New constructs a Handler from the given dependencies.
func New(d Deps) *Handler {
	return &Handler{
		log:         d.Logger,
		cfg:         d.Config,
		workflow:    d.Workflow,
		validator:   d.Validator,
		db:          d.DB,
		taskRepo:    d.TaskRepo,
		projectRepo: d.ProjectRepo,
		taskSvc:     d.TaskSvc,
		projectSvc:  d.ProjectSvc,
	}
}

// respondError is the SINGLE point in the codebase that translates an error
// into an HTTP response. Handlers never call response.Error directly; they
// produce an error (usually returned from a service) and pass it to this
// helper, which:
//
//   - unwraps to find an *apperror.Error,
//   - maps its Kind to the correct HTTP status code,
//   - writes the standard envelope (with Fields for validation errors).
//
// Unknown error types are treated as 500. Their detail is logged but never
// exposed to the client — that's a basic information-disclosure
// precaution; the client gets a generic message, operators get the real
// error in the logs.
func (h *Handler) respondError(w http.ResponseWriter, r *http.Request, err error) {
	log := middleware.LoggerFromContext(r.Context())
	if log == nil {
		log = h.log
	}

	// Service-vocabulary translation. The service package deliberately
	// does not import apperror (it has no concept of HTTP); this is the
	// single point where its errors become HTTP. Order matters: check
	// the typed *service.ValidationError before the sentinels, and map
	// sentinels with errors.Is so wrapped errors still match.
	var sve *service.ValidationError
	if errors.As(err, &sve) {
		response.ValidationError(w, sve.Fields)
		return
	}
	switch {
	case errors.Is(err, service.ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "resource not found")
		return
	case errors.Is(err, service.ErrInvalidTransition):
		// TransitionError carries from/to; surface its message so the
		// client learns which transition was rejected.
		msg := "invalid status transition"
		var te *service.TransitionError
		if errors.As(err, &te) {
			msg = te.Error()
		}
		response.Error(w, http.StatusConflict, "invalid_transition", msg)
		return
	case errors.Is(err, service.ErrConflict):
		response.Error(w, http.StatusConflict, "conflict", "the request conflicts with current state")
		return
	}

	var ae *apperror.Error
	if errors.As(err, &ae) {
		if ae.Kind == apperror.KindInternal {
			// Internal errors are interesting — log the wrapped cause.
			log.Error("internal error", "error", err)
		}
		if ae.Kind == apperror.KindValidation {
			response.ValidationError(w, ae.Fields)
			return
		}
		response.Error(w, apperror.HTTPStatus(ae.Kind), ae.Code, ae.Message)
		return
	}

	// Anything not classified is a programming error or an unexpected leak
	// from a library. Never reveal it to clients.
	log.Error("unclassified error", "error", err)
	response.Error(w, http.StatusInternalServerError, "internal", "internal server error")
}
