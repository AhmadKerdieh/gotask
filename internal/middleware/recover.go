package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"gotask/pkg/response"
)

// Recover returns middleware that catches panics raised by downstream
// handlers, logs them with a stack trace, and writes a 500 response in the
// standard envelope shape.
//
// Why this matters: in Go, a panic in an HTTP handler does NOT crash the
// server process (net/http has its own recover that just closes the
// connection), but the client sees a dropped connection and your logs show
// nothing about why. This middleware gives you both a structured log entry
// and a well-formed response.
//
// Placement: this middleware sits INSIDE AccessLog so that the 500 response
// it writes flows through AccessLog's wrapped writer — AccessLog then logs
// status=500 correctly. See server.go for the chain order.
//
// The fallback logger is used when no request-scoped logger is found on the
// context (which shouldn't happen in normal operation, but defensiveness in
// recovery code is a good habit — you don't want your panic handler to
// itself panic with a nil pointer).
func Recover(fallback *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				// Prefer the request-scoped logger (carries request_id) but
				// fall back to the base logger if for any reason it's not
				// in the context.
				log := fallback
				if l := LoggerFromContext(r.Context()); l != nil {
					log = l
				}

				log.Error("panic recovered",
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
					"path", r.URL.Path,
					"method", r.Method,
				)

				// If the panic happened after some bytes were already
				// flushed to the client, we can't write a clean 500 —
				// status is locked in, possibly headers are sent. Best
				// effort: don't try.
				if rw, ok := w.(*responseWriter); ok && rw.Wrote() {
					return
				}

				response.Error(w,
					http.StatusInternalServerError,
					"internal",
					"internal server error",
				)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
