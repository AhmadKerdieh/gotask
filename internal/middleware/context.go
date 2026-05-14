// Package middleware provides HTTP middleware for the gotask API.
//
// Every middleware in this package follows the standard Go signature:
//
//	func(next http.Handler) http.Handler
//
// which makes them composable with any router that accepts net/http handlers
// (chi, gorilla/mux, the stdlib mux, etc.).
//
// The middleware chain used by the server, in order, is:
//
//	RequestID  →  AccessLog  →  Recover  →  CORS  →  Timeout  →  Handler
//
// See internal/server/server.go for the wiring and the rationale for the
// order.
package middleware

import (
	"context"
	"log/slog"
)

// ctxKey is a private type used to namespace context keys. The Go community
// strongly recommends a private key type per package: it prevents external
// packages from colliding with our keys, and the unexported type cannot be
// constructed elsewhere.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// WithRequestID returns a derived context carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext extracts the request ID, or "" if none is present.
// Returning "" instead of a bool makes call sites cleaner — an empty ID is
// rendered as an empty field in structured logs, which is acceptable.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithLogger returns a derived context carrying a request-scoped logger.
// Downstream code (service, repository) calls LoggerFromContext to retrieve
// it and inherits all the attributes attached upstream (request_id, user_id
// once auth is added in Phase 6, etc.).
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, log)
}

// LoggerFromContext returns the request-scoped logger, or nil if none was
// attached. Callers that always need a logger should fall back to a sane
// default rather than nil-checking inline.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return v
	}
	return nil
}
