package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"time"
)

// AccessLog returns middleware that:
//
//  1. wraps the response writer so the status code and byte count can be
//     captured;
//  2. derives a request-scoped *slog.Logger with request_id attached and
//     stores it in r.Context() for downstream code;
//  3. after the handler returns, emits one structured INFO line per request
//     with the standard access log fields.
//
// The returned middleware closes over the supplied base logger. Constructing
// it once at startup (in server.NewRouter) and reusing the handler keeps the
// hot path allocation-free apart from the per-request With() call.
func AccessLog(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := wrapWriter(w)

			// Build a request-scoped logger and attach it to the context.
			// Every downstream slog call that goes through LoggerFromContext
			// will inherit request_id automatically — no need to thread it
			// manually through service and repository layers.
			reqID := RequestIDFromContext(r.Context())
			reqLog := base.With("request_id", reqID)
			ctx := WithLogger(r.Context(), reqLog)

			// Invoke the rest of the chain.
			next.ServeHTTP(rw, r.WithContext(ctx))

			// Emit the access record. Logging after next.ServeHTTP means we
			// see the real final status, including 500s written by the
			// recover middleware downstream.
			reqLog.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.Status(),
				"duration", time.Since(start).String(),
				"remote_ip", clientIP(r),
				"user_agent", r.UserAgent(),
			)
		})
	}
}

// clientIP returns the host portion of r.RemoteAddr. When the server runs
// behind a reverse proxy (nginx, ALB, Cloudflare), the real client address
// arrives in X-Forwarded-For — but parsing that header safely requires
// knowing which proxies you trust. We defer that complexity to Phase 9.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr was already "ip" with no port, or some unexpected
		// shape — return it verbatim rather than dropping it.
		return r.RemoteAddr
	}
	return host
}
