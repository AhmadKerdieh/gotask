// Package server constructs the application's HTTP handler: the middleware
// chain plus the route table. It exists separately from cmd/api/main.go so
// that integration tests can build a router without running a real server
// (they call httptest.NewServer(server.NewRouter(...)).
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"gotask/internal/config"
	"gotask/internal/handler"
	"gotask/internal/middleware"
	"gotask/internal/validator"
	"gotask/pkg/response"
)

// Deps bundles everything NewRouter needs. As the application grows we add
// services and repositories here. Doing it via a struct (rather than
// positional arguments) means callers don't have to memorise argument
// order and adding a new dep is a one-line change.
type Deps struct {
	Config    *config.Config
	Logger    *slog.Logger
	Workflow  *config.Workflow
	Validator *validator.Validator
}

// NewRouter assembles the middleware chain and the route table and returns
// an http.Handler ready to be served by the *http.Server in main.go.
//
// Middleware chain (order matters — see comments inline):
//
//	RequestID  →  AccessLog  →  Recover  →  CORS  →  Timeout  →  routes
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	// 1. RequestID first — every later layer needs the ID for correlation.
	r.Use(middleware.RequestID)

	// 2. AccessLog second — wraps the response writer so EVERY downstream
	//    write (including 500s from Recover) updates the captured status,
	//    and injects the request-scoped logger into r.Context().
	r.Use(middleware.AccessLog(d.Logger))

	// 3. Recover inside AccessLog — its 500 response flows through
	//    AccessLog's wrapped writer, so AccessLog reports status=500.
	r.Use(middleware.Recover(d.Logger))

	// 4. CORS — preflight OPTIONS responses short-circuit here, before we
	//    waste a timeout budget on them.
	r.Use(middleware.CORS(middleware.CORSConfig{
		AllowedOrigins: d.Config.CORSAllowedOrigins,
		AllowedMethods: []string{
			http.MethodGet, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete, http.MethodOptions,
		},
		AllowedHeaders: []string{"Content-Type", "Authorization", middleware.HeaderRequestID},
		MaxAge:         3600,
	}))

	// 5. Timeout — sets deadline on r.Context(). Handlers must observe it.
	r.Use(middleware.Timeout(d.Config.RequestTimeout))

	// Chi's default 404 / 405 are plain text. Override to keep the envelope.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		response.Error(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		response.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed for this route")
	})

	// Build the handler bundle. Services and repositories will join in
	// later phases — same construction pattern.
	h := handler.New(handler.Deps{
		Logger:    d.Logger,
		Config:    d.Config,
		Workflow:  d.Workflow,
		Validator: d.Validator,
	})

	// ── API routes ────────────────────────────────────────────────────
	// Versioning the API under /api/v1 from day one costs nothing now and
	// avoids a painful URL migration when v2 arrives. The frontend uses
	// these paths; integration tests will too.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.Health)

		// Workflow introspection — public, read-only, useful for the
		// frontend to render dropdowns with the legal statuses /
		// priorities. Kept under /api/v1 (not /debug) because we'll
		// likely want this in production too.
		r.Get("/workflow", h.GetWorkflow)

		// Debug routes only in development. The condition is checked once
		// at startup; in production these routes simply do not exist.
		if d.Config.Env == "development" {
			r.Route("/debug", func(r chi.Router) {
				r.Get("/panic", h.DebugPanic)
				r.Get("/slow", h.DebugSlow)
				r.Get("/error/{kind}", h.DebugError)

				// Phase 2: exercise the validator from the frontend.
				// POST a JSON body shaped like CreateTaskRequest;
				// receive either {valid:true} or the field error map.
				r.Post("/validate", h.DebugValidate)
			})
		}
	})

	// ── Static frontend ───────────────────────────────────────────────
	// The diagnostic page at "/" is served by an explicit handler so we
	// keep full control over what does and doesn't reach the FileServer.
	r.Get("/", h.Index)

	return r
}
