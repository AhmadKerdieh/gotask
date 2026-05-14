package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout returns middleware that sets a deadline on the request context.
//
// IMPORTANT: this middleware does NOT race the handler nor write a timeout
// response on its own. It only signals the deadline via context. The
// handler — and anything it calls (database driver, HTTP client, child
// goroutines) — is responsible for observing r.Context().Done() and
// returning early.
//
// We chose this design over the more aggressive http.TimeoutHandler from
// stdlib for a few reasons:
//
//   - TimeoutHandler buffers the response, which breaks streaming.
//   - It doesn't propagate cancellation into downstream Go code; you still
//     end up with leaked goroutines on the server side.
//   - The "deadline via context" pattern is what production database and
//     HTTP clients already understand. pgx, net/http.Client, and
//     google.golang.org/grpc all check ctx.Done() — give them the signal
//     and they do the right thing.
//
// Handlers should use the pattern:
//
//	select {
//	case <-r.Context().Done():
//	    response.Error(w, http.StatusGatewayTimeout, "timeout", "...")
//	    return
//	case result := <-doWork(r.Context()):
//	    ...
//	}
//
// or pass r.Context() to library calls that already respect deadlines.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			// cancel is deferred to release the timer immediately when the
			// handler returns normally — otherwise we leak a timer per
			// request until the original deadline fires.
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
