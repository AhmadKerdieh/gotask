// Package reqctx holds context keys and accessors that need to be
// readable from BOTH the HTTP-aware layer (middleware) and the
// HTTP-unaware layer (service, audit). Splitting it out keeps the
// service's "no net/http" invariant intact: the service imports
// reqctx (which depends only on the stdlib `context`), not middleware
// (which transitively pulls net/http).
//
// Currently this package holds only the request_id key. Future cross-
// layer context values (a deadline-watch token, a tracing span ID)
// would land here too, by the same reasoning.
package reqctx

import "context"

// ctxKey is the private type for this package's context keys.
// Using a per-package private type prevents accidental collisions
// across packages — two packages could both pick `string` keys and
// stomp on each other; private types can't.
type ctxKey int

const (
	requestIDKey ctxKey = iota
)

// WithRequestID returns a derived context carrying the request ID.
// Called by the RequestID middleware on every inbound request.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID, or "" if none is set.
// Called by any layer that wants to correlate work with the request
// that triggered it — handler, service, audit recorder.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
