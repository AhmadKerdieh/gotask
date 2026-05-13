// Package response provides a single, stable JSON envelope for every HTTP
// response the API emits. Having one shape means clients (including the
// vanilla-JS frontend) can parse responses uniformly without inspecting the
// route to decide what fields to expect.
//
// Envelope shape:
//
//	{
//	  "success": true,
//	  "data": { ... }                 // present on success
//	}
//
//	{
//	  "success": false,
//	  "error": {
//	    "code":    "not_found",
//	    "message": "task not found",
//	    "fields":  { "title": "required" }   // optional; set by ValidationError
//	  }
//	}
package response

import (
	"encoding/json"
	"net/http"
)

// Envelope is the wire shape for every response.
type Envelope struct {
	Success bool       `json:"success"`
	Data    any        `json:"data,omitempty"`
	Error   *ErrorBody `json:"error,omitempty"`
}

// ErrorBody describes a failure. Code is a stable machine-readable identifier
// (snake_case); Message is human-readable. Fields is populated for validation
// failures so the frontend can highlight specific inputs.
type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// OK writes a successful response with the given status code and payload.
// Use 200 for reads, 201 for creates, 204 for empty bodies.
func OK(w http.ResponseWriter, status int, data any) {
	write(w, status, Envelope{Success: true, Data: data})
}

// Error writes an error response. `code` is a stable, snake_case identifier
// that the frontend can switch on; `message` is the human description.
func Error(w http.ResponseWriter, status int, code, message string) {
	write(w, status, Envelope{
		Success: false,
		Error:   &ErrorBody{Code: code, Message: message},
	})
}

// ValidationError writes a 422 with per-field error messages, the shape the
// validator package will produce in Phase 2.
func ValidationError(w http.ResponseWriter, fields map[string]string) {
	write(w, http.StatusUnprocessableEntity, Envelope{
		Success: false,
		Error: &ErrorBody{
			Code:    "validation_error",
			Message: "request validation failed",
			Fields:  fields,
		},
	})
}

func write(w http.ResponseWriter, status int, body Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Ignore the encoder error: if the connection is broken there is
	// nothing we can sensibly do here, and we have already committed
	// the status code above.
	_ = json.NewEncoder(w).Encode(body)
}
