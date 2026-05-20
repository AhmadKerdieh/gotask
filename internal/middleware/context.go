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

// UserClaims is the typed view of the verified token's payload that the
// rest of the application is allowed to depend on. It is deliberately
// minimal: only what handlers actually need. Roles join in Phase 7.
//
// Subject is the Keycloak "sub" claim — the stable, unique user id. Under
// Option A this is the ONLY user identifier the application has; it is
// what goes into tasks.reporter_id / projects.owner_id. Email and Name
// are convenience copies from the token for logging/UX; they are NOT
// authoritative (Keycloak is) and must never be used for an authorization
// decision.
type UserClaims struct {
	Subject string
	Email   string
	Name    string
}

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
