package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"gotask/internal/apperror"
	"gotask/pkg/response"
)

// The debug handlers below exist to exercise the middleware stack from the
// frontend. They are mounted only when ENV=development (see server.go), so
// there is no risk of leaving them enabled in production.

// DebugPanic intentionally panics. The Recover middleware should:
//
//	1. log "panic recovered" with the stack trace, attributing it to the
//	   request_id of this request;
//	2. write a 500 envelope to the client;
//	3. allow AccessLog to record status=500 for this request.
func (h *Handler) DebugPanic(w http.ResponseWriter, r *http.Request) {
	panic("intentional panic from /api/v1/debug/panic")
}

// DebugSlow sleeps for ?ms milliseconds (default 20000) while observing the
// request context. When the Timeout middleware fires, ctx.Done() unblocks
// and we return 504 Gateway Timeout in our envelope shape.
//
// This is the production pattern: long-running operations select on
// ctx.Done() and bail out promptly. Database drivers and HTTP clients do
// this for you automatically when you pass them r.Context().
func (h *Handler) DebugSlow(w http.ResponseWriter, r *http.Request) {
	ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
	if ms <= 0 {
		ms = 20000
	}

	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		response.OK(w, http.StatusOK, map[string]any{"slept_ms": ms})

	case <-r.Context().Done():
		// ctx.Err() distinguishes deadline exceeded from a client-side
		// cancellation, but for the response we treat both as 504.
		h.respondError(w, r, apperror.New(
			apperror.KindTimeout,
			"timeout",
			"request exceeded deadline",
		))
	}
}

// DebugError lets the frontend exercise every error Kind so we can see how
// the apperror → HTTP status mapping behaves. URL: /debug/error/{kind}.
func (h *Handler) DebugError(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")

	switch apperror.Kind(kind) {
	case apperror.KindBadRequest:
		h.respondError(w, r, apperror.BadRequest("the request was malformed"))
	case apperror.KindUnauthorized:
		h.respondError(w, r, apperror.Unauthorized("missing or invalid credentials"))
	case apperror.KindForbidden:
		h.respondError(w, r, apperror.Forbidden("you don't have access to this resource"))
	case apperror.KindNotFound:
		h.respondError(w, r, apperror.NotFound("widget"))
	case apperror.KindConflict:
		h.respondError(w, r, apperror.Conflict("the resource already exists"))
	case apperror.KindValidation:
		h.respondError(w, r, apperror.Validation(map[string]string{
			"title":    "required",
			"priority": "must be one of: low, medium, high, critical",
		}))
	case apperror.KindTimeout:
		h.respondError(w, r, apperror.New(apperror.KindTimeout, "timeout", "took too long"))
	case apperror.KindInternal:
		// Wrap a fake underlying error to show the cause-preserving path.
		h.respondError(w, r, apperror.Internal(errFakeDB))
	default:
		h.respondError(w, r, apperror.BadRequest("unknown error kind: "+kind))
	}
}

// errFakeDB is a stand-in for a real wrapped error.
var errFakeDB = stringError("simulated database connection failure")

type stringError string

func (e stringError) Error() string { return string(e) }
