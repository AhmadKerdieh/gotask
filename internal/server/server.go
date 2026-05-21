// Package server constructs the application's HTTP handler: the middleware
// chain plus the route table. It exists separately from cmd/api/main.go so
// that integration tests can build a router without running a real server
// (they call httptest.NewServer(server.NewRouter(...)).
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"gotask/internal/audit"
	"gotask/internal/config"
	"gotask/internal/database"
	"gotask/internal/handler"
	"gotask/internal/keycloakadmin"
	"gotask/internal/middleware"
	"gotask/internal/service"
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

	// DB is carried so the readiness probe can ping it. Handlers reach
	// the data layer only through services now (the raw repos and the
	// debug probes that used them were removed in Phase 5).
	DB *database.DB

	TaskSvc    *service.TaskService
	ProjectSvc *service.ProjectService

	// Auth is the OIDC authenticator. Its Middleware guards the protected
	// route group; public routes (health/ready/workflow/static) are
	// mounted OUTSIDE that group so a load balancer probing /ready never
	// needs a token.
	Auth *middleware.Authenticator

	// Auditor (read side) and UserLookup feed the /audit endpoint.
	Auditor    audit.Reader
	UserLookup keycloakadmin.Lookup
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

	// Build the handler bundle. Repositories are passed as interfaces;
	// the service layer in Phase 4 will sit between handlers and repos,
	// but for now the dev-only DB probe talks to them directly to prove
	// the persistence layer works end to end.
	h := handler.New(handler.Deps{
		Logger:     d.Logger,
		Config:     d.Config,
		Workflow:   d.Workflow,
		Validator:  d.Validator,
		DB:         d.DB,
		TaskSvc:    d.TaskSvc,
		ProjectSvc: d.ProjectSvc,
		Auditor:    d.Auditor,
		UserLookup: d.UserLookup,
	})

	// ── API routes ────────────────────────────────────────────────────
	// Versioning the API under /api/v1 from day one costs nothing now and
	// avoids a painful URL migration when v2 arrives. The frontend uses
	// these paths; integration tests will too.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.Health)

		// Readiness vs liveness: /health answers "is the process up?".
		// /ready answers "can it actually serve traffic?" — which for
		// this app means the database round-trips. Load balancers and
		// orchestrators should gate traffic on readiness, not liveness.
		r.Get("/ready", h.Ready)

		// Workflow introspection — public, read-only. The frontend needs
		// the status list to render its UI BEFORE the user has logged in
		// (e.g. to build dropdowns on the login-gated screen), so this
		// stays outside the auth group. It exposes no user data.
		r.Get("/workflow", h.GetWorkflow)

		// ── Protected group ───────────────────────────────────────────
		// Everything inside requires a verified token. The auth
		// middleware is applied to THIS group only, not globally, so the
		// public endpoints above (and the static page / health checks)
		// never require a token — a load balancer probing /ready has no
		// token and must not need one.
		//
		// chi's r.Group creates a fresh middleware stack sharing the same
		// routing tree; Use() here affects only routes registered inside
		// this closure.
		r.Group(func(r chi.Router) {
			r.Use(d.Auth.Middleware)

			// ── Projects ──────────────────────────────────────────
			r.Route("/projects", func(r chi.Router) {
				r.Post("/", h.CreateProject)
				r.Get("/", h.ListProjects)
				r.Get("/{id}", h.GetProject)
			})

			// ── Tasks ─────────────────────────────────────────────
			r.Route("/tasks", func(r chi.Router) {
				r.Post("/", h.CreateTask)
				r.Get("/", h.ListTasks)
				r.Get("/{id}", h.GetTask)
				r.Delete("/{id}", h.DeleteTask)

				// Status is its own sub-resource, not part of a general
				// task update, because it is the single mutation governed
				// by the workflow transition rules. One guarded entry
				// point for that rule.
				r.Patch("/{id}/status", h.UpdateTaskStatus)
			})

			// Audit. Authentication is sufficient to LIST your own
			// history; cross-user listing is gated INSIDE the handler
			// by authz.CanListAuditAcrossUsers. We deliberately do NOT
			// wrap this in RequireRole("admin") at the route level —
			// the endpoint is meaningfully usable by every
			// authenticated user, just narrowed for non-admins. That
			// is fine-grained, so it belongs in the handler/service
			// layer, not in middleware.
			r.Get("/audit", h.ListAudit)
		})
	})

	// ── Static frontend ───────────────────────────────────────────────
	// The diagnostic page at "/" is served by an explicit handler so we
	// keep full control over what does and doesn't reach the FileServer.
	r.Get("/", h.Index)

	return r
}
