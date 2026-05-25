package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"gotask/internal/metrics"
)

// Metrics is the HTTP-level Prometheus instrumentation middleware. It
// records, per request:
//
//   - Latency (histogram) → metrics.HTTPRequestDuration
//   - Count   (counter)   → metrics.HTTPRequestsTotal
//   - In-flight (gauge)   → metrics.HTTPRequestsInFlight
//
// ── Why this is middleware ──────────────────────────────────────────
//
// HTTP-level metrics live at the boundary where requests enter and
// leave; middleware is the natural place. The alternative — calling
// metric updates in every handler — would scatter the same code across
// dozens of sites and forget it on the next new handler. The recover-
// from-panic discipline is exactly the same reason recover is middleware.
//
// ── Cardinality, in code ────────────────────────────────────────────
//
// The route LABEL value comes from chi.RouteContext().RoutePattern() —
// e.g. "/api/v1/tasks/{id}" — NOT from r.URL.Path. This is the single
// thing this middleware does that distinguishes "production-ready" from
// "kills Prometheus in a week". Pattern: ~20 unique values. Path: one
// per UUID, unbounded. See internal/metrics for the full discipline.
//
// The status LABEL value comes from metrics.StatusClass — "2xx" / "4xx"
// / "5xx" — also bounded.
//
// ── Order in the chain ──────────────────────────────────────────────
//
// This middleware is wired AFTER chi's routing (because RoutePattern
// only resolves once routing has happened) and AFTER the response-
// writer wrap (so it can read the status code). In server.go it sits
// inside the AccessLog wrapper:
//
//     AccessLog → Metrics → handlers
//
// — which means a request that times out inside a handler still gets
// its latency recorded by Metrics, then its line logged by AccessLog.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bump the in-flight gauge for the entire request lifetime.
		// The defer-Dec ensures we never leak a count even on panic —
		// the recover middleware above us catches the panic, but the
		// defer here runs first.
		metrics.HTTPRequestsInFlight.Inc()
		defer metrics.HTTPRequestsInFlight.Dec()

		start := time.Now()

		// Wrap the writer ONLY if it's not already wrapped. Access-log
		// wraps it too; we don't want double-wrap. Type-assert first.
		rw, ok := w.(*responseWriter)
		if !ok {
			rw = wrapWriter(w)
			w = rw
		}

		next.ServeHTTP(w, r)

		// chi populates RouteContext after routing. If the request hit
		// no route (404 from chi's default handler), RoutePattern() is
		// empty — we label it "unmatched" to keep cardinality bounded
		// (rather than label it with the raw path, which is unbounded).
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		status := metrics.StatusClass(rw.Status())
		method := r.Method
		dur := time.Since(start).Seconds()

		metrics.HTTPRequestDuration.WithLabelValues(route, method, status).Observe(dur)
		metrics.HTTPRequestsTotal.WithLabelValues(route, method, status).Inc()
	})
}
