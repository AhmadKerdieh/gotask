package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/internal/domain"
	"gotask/internal/repository"
	"gotask/internal/service"
	"gotask/pkg/response"
)

// The handlers in this file are mounted only when ENV=development. They
// exercise the real repository layer against the real database so the
// frontend can prove persistence works end to end before the full Tasks
// API exists (that arrives in Phase 5, behind the service layer).

// seedReporterID is a fixed throwaway user id used by the seed probe.
// tasks.reporter_id has a NOT NULL foreign key to users(id), so the probe
// must ensure a user row exists first. Using a constant id makes the seed
// idempotent — repeated calls reuse the same demo user instead of piling
// up rows.
var seedReporterID = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")

// DebugDBSeed creates a throwaway project and a task inside it, proving
// the Create → RETURNING path and the foreign-key relationship work.
//
// It performs three writes in sequence: ensure a demo user exists (raw
// SQL, because there is no user repository yet — that is Phase 6), create
// a project via the repository, then create a task in that project via
// the repository. The task and project ids are returned so the frontend
// can show them.
func (h *Handler) DebugDBSeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Ensure the demo user exists. ON CONFLICT DO NOTHING makes this
	// idempotent. This is the one place we touch SQL outside a
	// repository, and only because the user repository does not exist
	// yet; it is clearly a debug-only shortcut.
	const ensureUser = `
		INSERT INTO users (id, email, name, role)
		VALUES ($1, 'seed@example.com', 'Seed User', 'member')
		ON CONFLICT (id) DO NOTHING`
	if _, err := h.db.Pool.Exec(ctx, ensureUser, seedReporterID); err != nil {
		h.respondError(w, r, apperror.Internal(err))
		return
	}

	// Phase 4: go through the SERVICE, not the repository. The service
	// normalises the project key, validates, and applies workflow
	// defaults to the task. This exercises the real enforced path the
	// production Tasks API will use in Phase 5.
	proj, err := h.projectSvc.Create(ctx, service.CreateProjectInput{
		Key:         "SEED" + time.Now().Format("150405"),
		Name:        "Seed Project",
		Description: "Created by the Phase 4 service probe.",
		OwnerID:     seedReporterID,
	})
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	// No status/priority supplied — the service applies the workflow
	// defaults. Contrast with Phase 3, where the handler did this by
	// hand; that responsibility now lives in exactly one place.
	task, err := h.taskSvc.Create(ctx, service.CreateTaskInput{
		ProjectID:   proj.ID,
		Title:       "Seeded task",
		Description: "Created via the service; defaults applied by the service.",
		ReporterID:  seedReporterID,
	})
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusCreated, map[string]any{
		"project": proj,
		"task":    task,
	})
}

// DebugDBListTasks lists tasks via the SERVICE, optionally filtered by
// ?status=.
func (h *Handler) DebugDBListTasks(w http.ResponseWriter, r *http.Request) {
	f := repository.TaskFilter{}
	if s := r.URL.Query().Get("status"); s != "" {
		f.Status = domain.Status(s)
	}

	tasks, err := h.taskSvc.List(r.Context(), f)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, map[string]any{
		"count": len(tasks),
		"tasks": tasks,
	})
}

// DebugTransition exercises the centrepiece business rule from the
// browser: given an existing task id and a target status, attempt the
// transition through the service. A legal move returns the updated task;
// an illegal one returns 409 invalid_transition; an unknown status
// returns 422. This lets you watch a business rule reject a request
// without building the full Tasks API (Phase 5).
//
// Query params: ?id=<task-uuid>&to=<status>
func (h *Handler) DebugTransition(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	to := r.URL.Query().Get("to")

	id, err := uuid.Parse(idStr)
	if err != nil {
		h.respondError(w, r, apperror.BadRequest("id must be a valid task UUID"))
		return
	}

	updated, err := h.taskSvc.UpdateStatus(r.Context(), id, domain.Status(to))
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	response.OK(w, http.StatusOK, map[string]any{
		"task": updated,
	})
}

// DebugDBSlowQuery runs `SELECT pg_sleep($1)` so we can prove the whole
// cancellation chain end to end: HTTP timeout middleware → request
// context deadline → pgx aborting the in-flight query at the protocol
// level → clean 504, NOT a hung connection.
//
// Pass ?seconds=N. Anything at or above the configured REQUEST_TIMEOUT
// should be cut off by the middleware. The key observation when testing:
// the Postgres server-side query is actually cancelled (you can see it
// disappear from pg_stat_activity), not just abandoned client-side.
func (h *Handler) DebugDBSlowQuery(w http.ResponseWriter, r *http.Request) {
	seconds := 30
	if v := r.URL.Query().Get("seconds"); v != "" {
		if n, err := time.ParseDuration(v + "s"); err == nil {
			seconds = int(n.Seconds())
		}
	}

	// pg_sleep blocks server-side. Passing r.Context() means pgx will
	// send a cancellation to Postgres when the context deadline fires.
	_, err := h.db.Pool.Exec(r.Context(), "SELECT pg_sleep($1)", seconds)
	if err != nil {
		// A cancelled context surfaces here. translateError-style
		// classification is the repository's job; this debug handler
		// short-circuits: if the context is done, it is a timeout.
		if ctxErr := r.Context().Err(); ctxErr != nil {
			h.respondError(w, r, apperror.New(
				apperror.KindTimeout, "timeout",
				"query cancelled by request deadline",
			))
			return
		}
		h.respondError(w, r, apperror.Internal(err))
		return
	}

	response.OK(w, http.StatusOK, map[string]any{
		"slept_seconds": seconds,
		"note":          "completed without hitting the deadline",
	})
}
