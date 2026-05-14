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
	"gotask/pkg/response"
)

// Handler bundles the shared dependencies every HTTP handler needs.
// Fields are unexported; construct with New.
type Handler struct {
	log *slog.Logger
	cfg *config.Config
}

// New constructs a Handler. Pass it the same dependencies you'd give to a
// service or repository — handlers, services, and repos all participate in
// the same dependency graph wired up by main.go.
func New(log *slog.Logger, cfg *config.Config) *Handler {
	return &Handler{log: log, cfg: cfg}
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
