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

	"gotask/internal/authz"
)

// ctxKey is a private type used to namespace context keys. The Go community
// strongly recommends a private key type per package: it prevents external
// packages from colliding with our keys, and the unexported type cannot be
// constructed elsewhere.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
	userSubjectKey
	userClaimsKey
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

// UserClaims is an alias for authz.Claims, retained at this name so the
// middleware's API doesn't change for existing callers. The TYPE itself
// lives in the authz package because that's where the type's
// reason-to-exist (authorization decisions) lives — and so the service
// can depend on Claims without transitively importing this package's
// net/http dependencies. This is a real architectural seam: the auth
// layer produces a value the authz layer consumes; middleware doesn't
// own the schema.
type UserClaims = authz.Claims

// WithUser returns a derived context carrying the verified user identity.
// Set ONLY by the auth middleware after full token verification — never
// from anything a client controls directly.
func WithUser(ctx context.Context, c UserClaims) context.Context {
	ctx = context.WithValue(ctx, userSubjectKey, c.Subject)
	return context.WithValue(ctx, userClaimsKey, c)
}

// UserSubjectFromContext returns the verified Keycloak subject, or "" if
// the request was not authenticated. Handlers behind the auth middleware
// can rely on this being non-empty; public handlers must not assume it.
func UserSubjectFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userSubjectKey).(string); ok {
		return v
	}
	return ""
}

// UserClaimsFromContext returns the full verified claims and whether they
// were present.
func UserClaimsFromContext(ctx context.Context) (UserClaims, bool) {
	v, ok := ctx.Value(userClaimsKey).(UserClaims)
	return v, ok
}
