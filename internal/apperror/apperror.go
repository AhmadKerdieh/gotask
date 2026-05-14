// Package apperror defines the application's typed error model.
//
// A single struct type, Error, carries every error the application can
// produce. The Kind field categorises errors so they can be mapped to HTTP
// status codes in one place; the Code field is a stable machine-readable
// identifier exposed to clients; Message is human-readable; Fields is set
// for validation errors so the client can highlight specific inputs.
//
// Design rationale
//
//  1. One type means handlers can use errors.As(err, &apperr) uniformly.
//  2. Kind is an enum, not a status code, so apperror has no dependency on
//     net/http. The HTTP mapping lives in HTTPStatus() at the edge of this
//     package and is the only HTTP-aware function.
//  3. Wrapping is supported via the standard errors.Unwrap contract.
//
// Typical usage:
//
//	if err != nil {
//	    return apperror.Wrap(err, apperror.KindInternal, "db_error", "could not load task")
//	}
//
//	return apperror.NotFound("task")           // shorthand for KindNotFound
package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// Kind categorises an error. Add new kinds here as the application grows;
// HTTPStatus must be updated in lockstep.
type Kind string

const (
	KindBadRequest   Kind = "bad_request"
	KindUnauthorized Kind = "unauthorized"
	KindForbidden    Kind = "forbidden"
	KindNotFound     Kind = "not_found"
	KindConflict     Kind = "conflict"
	KindValidation   Kind = "validation"
	KindTimeout      Kind = "timeout"
	KindInternal     Kind = "internal"
)

// Error is the application's only error type. The zero value is invalid:
// always construct via New, Wrap, or one of the helper constructors.
type Error struct {
	Kind    Kind              // category (drives HTTP status)
	Code    string            // stable snake_case identifier for clients
	Message string            // human-readable description
	Fields  map[string]string // per-field detail (validation errors)
	cause   error             // wrapped underlying error, if any
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %s", e.Kind, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *Error) Unwrap() error { return e.cause }

// Is enables errors.Is to compare by Kind. Two apperror.Error values are
// considered "the same" if they share a Kind. This is the comparison
// callers care about ("is this a not-found?"), not pointer equality.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Kind == t.Kind
}

// New constructs an Error with no underlying cause.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Wrap constructs an Error that wraps cause. Use this when translating an
// error from a lower layer (database, external service) into an application
// error. The cause is preserved for logging but not exposed to clients.
func Wrap(cause error, kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message, cause: cause}
}

// WithFields attaches per-field detail (used for validation errors).
// Returns the receiver to allow fluent construction.
func (e *Error) WithFields(fields map[string]string) *Error {
	e.Fields = fields
	return e
}

// ── Convenience constructors ──
//
// These exist so handlers can write `apperror.NotFound("task")` rather than
// repeating Kind / Code / Message triplets for the common cases.

// BadRequest returns a 400 with the given human message.
func BadRequest(message string) *Error {
	return New(KindBadRequest, "bad_request", message)
}

// Unauthorized returns a 401 — the request lacks valid credentials.
func Unauthorized(message string) *Error {
	return New(KindUnauthorized, "unauthorized", message)
}

// Forbidden returns a 403 — credentials are valid but access is denied.
func Forbidden(message string) *Error {
	return New(KindForbidden, "forbidden", message)
}

// NotFound returns a 404 for the named resource ("task", "project", ...).
func NotFound(resource string) *Error {
	return New(KindNotFound, "not_found", resource+" not found")
}

// Conflict returns a 409 — the request collides with current state (e.g.,
// duplicate unique key, illegal status transition).
func Conflict(message string) *Error {
	return New(KindConflict, "conflict", message)
}

// Validation returns a 422 with per-field detail.
func Validation(fields map[string]string) *Error {
	return New(KindValidation, "validation_error", "request validation failed").WithFields(fields)
}

// Internal returns a 500 wrapping an underlying cause. The cause is logged
// server-side; the client sees only the generic message.
func Internal(cause error) *Error {
	return Wrap(cause, KindInternal, "internal", "internal server error")
}

// HTTPStatus maps a Kind to the appropriate HTTP status code. Keep this
// list in sync with the Kind constants.
func HTTPStatus(kind Kind) int {
	switch kind {
	case KindBadRequest:
		return http.StatusBadRequest
	case KindUnauthorized:
		return http.StatusUnauthorized
	case KindForbidden:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindValidation:
		return http.StatusUnprocessableEntity
	case KindTimeout:
		return http.StatusGatewayTimeout
	case KindInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
