package handler

import (
	"net/http"
	"time"

	"gotask/pkg/response"
)

// Health reports basic liveness information. In Phase 9 this is split into
// /healthz (liveness — am I running?) and /readyz (readiness — can I serve
// traffic? DB up? Keycloak reachable?). For now a single endpoint is fine.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.OK(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "gotask",
		"env":     h.cfg.Env,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}
