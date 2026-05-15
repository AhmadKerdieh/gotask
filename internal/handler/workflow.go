package handler

import (
	"net/http"

	"gotask/pkg/response"
)

// GetWorkflow returns the loaded workflow.yaml as a JSON document. Useful
// for:
//
//   - the frontend, which can populate status/priority dropdowns and
//     enable/disable transition buttons without hardcoding values;
//   - operators, who can confirm at runtime which workflow file the
//     running process actually loaded;
//   - integration tests, which can assert against the live workflow
//     rather than re-parsing the YAML themselves.
//
// We return a shaped object (not the *config.Workflow struct directly)
// to keep the wire format stable even if the internal type grows
// unexported helper fields.
func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	// Compose the response from the loaded workflow. Returning maps and
	// slices means the JSON shape is exactly what an API client expects;
	// no JSON tags need to be added to *config.Workflow.
	body := map[string]any{
		"statuses":          h.workflow.Statuses,
		"terminal_statuses": h.workflow.TerminalStatuses,
		"transitions":       h.workflow.Transitions,
		"priorities":        h.workflow.Priorities,
		"default_status":    h.workflow.DefaultStatus,
		"default_priority":  h.workflow.DefaultPriority,
	}
	response.OK(w, http.StatusOK, body)
}
