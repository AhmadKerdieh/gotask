package middleware

import (
	"net/http"

	"github.com/google/uuid"
)

// HeaderRequestID is the conventional HTTP header for request correlation.
const HeaderRequestID = "X-Request-ID"

// RequestID middleware ensures every request has a correlation ID:
//
//   - If the client sent X-Request-ID, we accept it (within reason; see note).
//   - Otherwise we generate a UUIDv4.
//
// The ID is stored in r.Context() so downstream code can attach it to logs,
// and echoed back in the X-Request-ID response header so clients can
// correlate request/response pairs in their own logs.
//
// Note on accepting client-supplied IDs: in production behind a trusted
// proxy this is fine. In a hostile setting you may want to ignore or sanity
// check incoming IDs (length, character set) to prevent log injection.
// We accept-as-is here for simplicity; revisit in Phase 9.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = uuid.NewString()
		}

		// Echo back so the client sees the ID even before reading the body.
		w.Header().Set(HeaderRequestID, id)

		// Carry the ID forward in context for logs, metrics, and traces.
		ctx := WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
