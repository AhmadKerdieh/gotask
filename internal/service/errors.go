// Package service holds the application's business logic: the rules that
// would still be true if gotask were a CLI instead of an HTTP API.
//
// Layer contract:
//
//   - handlers   do HTTP (parse, validate shape, serialize, status codes)
//   - services   do business rules (this package)
//   - repositories do persistence (SQL, nothing else)
//
// The service depends on the repository INTERFACES (not the Postgres
// implementations) and on the workflow config. It does NOT import
// apperror or net/http — it has no concept of HTTP. The handler owns the
// single translation point that maps the service error vocabulary defined
// below into apperror/HTTP. This mirrors the discipline already used at
// the repository boundary (repository/errors.go) and the HTTP boundary
// (handler respondError).
package service

import (
	"errors"
	"fmt"
)

// Sentinel errors for conditions a caller may want to branch on with
// errors.Is. These carry no data; when per-field detail is needed, use
// ValidationError below.
var (
	// ErrNotFound is returned when an entity does not exist. The service
	// also surfaces this when a repository reports no rows; the handler
	// maps it to 404.
	ErrNotFound = errors.New("not found")

	// ErrInvalidTransition is returned when a status change is not
	// permitted by workflow.yaml (including any change out of a terminal
	// status, and no-op self-transitions). Handler maps it to 409.
	ErrInvalidTransition = errors.New("invalid status transition")

	// ErrConflict is returned for state collisions that are not
	// transitions — e.g. a duplicate project key. Handler maps it to 409.
	ErrConflict = errors.New("conflict")
)

// ValidationError reports one or more invalid input fields. It is a
// distinct type (not a sentinel) because it carries a field->message map,
// exactly the shape the HTTP layer's validation envelope wants. The
// handler unwraps this and produces a 422 with the field detail.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed: %d field(s)", len(e.Fields))
}

// newValidationError is an internal helper so service code can accumulate
// field errors ergonomically.
func newValidationError() *ValidationError {
	return &ValidationError{Fields: make(map[string]string)}
}

// add records a field error and returns the receiver for chaining.
func (e *ValidationError) add(field, msg string) *ValidationError {
	e.Fields[field] = msg
	return e
}

// orNil returns the error if any fields were recorded, otherwise nil.
// This lets callers write:  return ve.orNil()
func (e *ValidationError) orNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// TransitionError augments ErrInvalidTransition with the specific
// from/to so the handler can produce a useful message while still
// allowing errors.Is(err, ErrInvalidTransition) to match.
type TransitionError struct {
	From string
	To   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid status transition: %s -> %s", e.From, e.To)
}

// Is makes errors.Is(err, ErrInvalidTransition) return true for any
// TransitionError, so callers can branch on the category without caring
// about the specific from/to.
func (e *TransitionError) Is(target error) bool {
	return target == ErrInvalidTransition
}
