package handler

import (
	"net/http"

	"gotask/pkg/response"
)

// Ready is the readiness probe. It differs from Health (liveness) in a way
// that matters operationally:
//
//   - Health answers "is this process alive?" — used by an orchestrator
//     to decide whether to RESTART the container.
//   - Ready answers "can this process serve traffic right now?" — used by
//     a load balancer to decide whether to ROUTE traffic to it.
//
// A process can be alive but not ready: started, but its database is
// unreachable. Routing traffic to it would produce 500s; restarting it
// would not help (the database is the problem, not the process). Keeping
// the two probes separate lets the infrastructure make the right call.
//
// The check respects the request context, so the Phase 1 timeout
// middleware bounds how long a hung database can make this endpoint hang.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Health(r.Context()); err != nil {
		// Not ready. 503 is the correct status — the service exists but
		// is temporarily unable to handle the request. Load balancers
		// understand 503 from a readiness endpoint as "don't route here
		// yet".
		log := h.log
		log.Warn("readiness check failed", "error", err)
		response.Error(w, http.StatusServiceUnavailable, "not_ready", "database is unreachable")
		return
	}
	response.OK(w, http.StatusOK, map[string]any{
		"status":   "ready",
		"database": "ok",
	})
}
