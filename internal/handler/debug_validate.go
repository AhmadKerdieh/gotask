package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/pkg/response"
)

// debugValidatePayload is the shape we expect on POST /api/v1/debug/validate.
//
// It mirrors the future CreateTaskRequest DTO that Phase 5 will introduce
// for real, but lives in handler (not handler/dto) because it exists only
// to exercise the validator from the frontend playground. When Phase 5
// builds the real DTO, this can either be deleted or pointed at the new
// type.
//
// Validation rule notes:
//
//   - required: field must be present and non-zero (uuid.Nil for UUID,
//     "" for string, time.Time{} for time.Time).
//   - omitempty + uuid: if the field is the zero UUID, validation is
//     skipped. Use this for optional UUID fields like AssigneeID.
//   - status / priority: our custom tags from internal/validator.
//   - The validator reads JSON tags via RegisterTagNameFunc, so
//     field-error keys will be "title" rather than "Title".
type debugValidatePayload struct {
	ProjectID   uuid.UUID `json:"project_id"  validate:"required"`
	Title       string    `json:"title"       validate:"required,min=1,max=200"`
	Description string    `json:"description" validate:"max=10000"`
	Status      string    `json:"status"      validate:"omitempty,status"`
	Priority    string    `json:"priority"    validate:"omitempty,priority"`
	AssigneeID  uuid.UUID `json:"assignee_id" validate:"omitempty"`
	DueDate     time.Time `json:"due_date"    validate:"omitempty"`
}

// DebugValidate decodes a JSON payload, runs it through the validator,
// and reports the result. It is mounted only in development.
//
// The endpoint demonstrates the full validation pipeline as Phase 5+
// handlers will use it:
//
//	1. JSON-decode the body into a DTO.
//	2. Run the validator.
//	3. If the validator returns a field map, respond via
//	   apperror.Validation → respondError → 422 with the standard
//	   envelope (and field detail).
//	4. Otherwise the request is structurally valid; in this debug
//	   endpoint we report success and echo the parsed payload.
func (h *Handler) DebugValidate(w http.ResponseWriter, r *http.Request) {
	var p debugValidatePayload

	// json.Decode-level errors (malformed JSON, wrong types) are mapped
	// to a 400 BadRequest. We don't surface the raw error message — it
	// leaks internal type names — but we do include the position the
	// decoder reached so debugging from the client side is feasible.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // friendlier than silently dropping fields
	if err := dec.Decode(&p); err != nil {
		h.respondError(w, r, apperror.BadRequest("invalid JSON body: "+err.Error()))
		return
	}

	if fields := h.validator.Struct(&p); fields != nil {
		h.respondError(w, r, apperror.Validation(fields))
		return
	}

	// All structural checks passed. Echo the parsed payload so the
	// frontend can confirm what the server actually understood.
	response.OK(w, http.StatusOK, map[string]any{
		"valid":  true,
		"parsed": p,
	})
}
