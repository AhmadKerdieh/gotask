// Package metrics defines the Prometheus collectors the app exposes on
// /metrics. This is the single file every metric in the codebase is
// declared in — centralising the collectors here is the same discipline
// as centralising error mapping in respondError: one place to audit
// cardinality, naming, and label discipline.
//
// ── The four golden signals (Google SRE), mapped to our collectors ──
//
//   Latency      → httpRequestDuration (histogram by route/method/status)
//   Traffic      → httpRequestsTotal   (counter by route/method/status)
//   Errors       → httpRequestsTotal{status="5xx"} via the same counter
//   Saturation   → httpRequestsInFlight (gauge) + db_pool_* + outbox depth
//
// Every dashboard for a web service can start from this template.
//
// ── Cardinality discipline ──────────────────────────────────────────
//
// A Prometheus metric is identified by name + every label-value
// combination. Each unique combination is a "time series". Prometheus
// is happy with thousands, OOM-killed at tens of millions. The rule:
//
//     Label values must come from a SMALL, FIXED SET known at design
//     time — never from request data.
//
// Concrete examples of safe vs unsafe label sources, applied to this
// codebase:
//
//   route="/api/v1/tasks/{id}"   ✓  chi route pattern (~20 patterns total)
//   method="POST"                ✓  4-5 HTTP methods
//   status="2xx"|"4xx"|"5xx"     ✓  5 classes
//   action="task.created"        ✓  closed set of 4 audit actions
//   outcome="success"|"denied"   ✓  2 values
//
//   path="/api/v1/tasks/abc-123" ✗  one per UUID — kills Prometheus
//   user_id=<sub>                ✗  unbounded
//   error_message="..."          ✗  free-form text
//
// The chi router exposes the matched PATTERN via
// chi.RouteContext(r.Context()).RoutePattern() — that's the
// low-cardinality version of the URL. We use that, not r.URL.Path.
//
// ── Histogram vs counter vs gauge ───────────────────────────────────
//
//   Counter   only goes up; you ask `rate(x[5m])` for traffic/error %.
//   Gauge     samples a current value; you graph it directly.
//   Histogram pre-buckets values so you can compute P50/P95/P99 at
//             query time. Use this for latencies — average latency
//             alone is misleading ("on average users had a fine time"
//             while P99 was 30 seconds).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Registry is the single Prometheus registry the app exposes. We
// construct our own (instead of using the default global) so /metrics
// is fully under our control — no accidental leakage of metrics from
// libraries that helpfully register against the default.
//
// The Go runtime collectors are added explicitly below for the same
// reason: explicit beats implicit when the surface is what's exposed
// to operators.
var Registry = prometheus.NewRegistry()

// ── HTTP metrics (the four golden signals' three observable ones) ───

// HTTPRequestDuration is the latency histogram for inbound HTTP. The
// bucket layout is the Prometheus-recommended default for web
// services: spans 5ms → 10s with finer resolution near typical web
// latencies. Buckets are STATIC at registration time — picking these
// right matters because every query that computes P95 etc. depends
// on the bucket boundaries.
var HTTPRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "http_request_duration_seconds",
		Help: "HTTP request latency. Labels are route (chi pattern), method, status class.",
		// Buckets chosen to give useful percentile resolution for a
		// typical web service: many buckets near 50–500ms, fewer
		// above. The 10s upper bound exists because our REQUEST_TIMEOUT
		// is 15s — past 10s, individual buckets matter less than the
		// fact of being slow.
		Buckets: []float64{
			0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
		},
	},
	[]string{"route", "method", "status"},
)

// HTTPRequestsTotal is the traffic counter — and the basis for the
// error rate ("rate(http_requests_total{status='5xx'}[5m])").
// Same label set as the histogram for joinable queries.
var HTTPRequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests. Labels are route (chi pattern), method, status class.",
	},
	[]string{"route", "method", "status"},
)

// HTTPRequestsInFlight is the saturation gauge. A spike here without
// a corresponding traffic spike means handlers are getting slower
// (work queueing up); a sustained high value is a capacity warning.
var HTTPRequestsInFlight = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Current number of HTTP requests being processed.",
	},
)

// ── Audit metrics ───────────────────────────────────────────────────

// AuditEventsTotal counts audit events recorded. Cardinality is 4
// actions × 2 outcomes = 8 series, safely bounded. The interesting
// queries are:
//   - rate by outcome="denied"  (security signal: failed authz attempts)
//   - rate by action="task.deleted" + outcome="success" (deletion volume)
var AuditEventsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "audit_events_total",
		Help: "Total audit events recorded, by action and outcome.",
	},
	[]string{"action", "outcome"},
)

// AuditOutboxPending is THE alerting metric. The drainer updates it at
// the start of each iteration. In production you'd alert when
// audit_outbox_pending > 1000 sustained for 5 minutes — that means
// the drainer can't keep up and audit lag is accumulating.
//
// As a gauge, this answers "right now, how behind is the drainer?".
// As a histogram-over-time at query time, you can see the depth's
// daily pattern (catches a drainer that mysteriously falls behind
// at the same hour each day, say a backup job competing for I/O).
var AuditOutboxPending = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "audit_outbox_pending",
		Help: "Current number of unclaimed rows in audit_outbox. A growing value means the drainer is falling behind.",
	},
)

// DrainerIterationDuration measures how long each drain iteration
// takes. Useful for tuning AUDIT_DRAIN_BATCH_SIZE: if iterations are
// fast and outbox is growing, batch is too small. If iterations are
// slow, batch may be too large for the available DB throughput.
var DrainerIterationDuration = prometheus.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "audit_drainer_iteration_duration_seconds",
		Help:    "Duration of one drainer iteration (claim + insert + delete + commit).",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30},
	},
)

// DrainerRowsProcessed is a counter incremented per drained row. The
// rate is "audit events per second drained" — should track audit
// events created over the long run; if it consistently lags, you
// need a bigger batch or a faster DB.
var DrainerRowsProcessed = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "audit_drainer_rows_processed_total",
		Help: "Total audit rows drained from audit_outbox into audit_log.",
	},
)

// ── Wiring ──────────────────────────────────────────────────────────

// Register registers all the collectors above (plus the Go runtime
// collectors) into the package's Registry. Called once at startup
// from main.go before /metrics is served. Re-registration of a
// collector panics, which is what we want — it would mean someone
// added a duplicate Name and we'd see it immediately.
func Register() {
	Registry.MustRegister(
		HTTPRequestDuration,
		HTTPRequestsTotal,
		HTTPRequestsInFlight,
		AuditEventsTotal,
		AuditOutboxPending,
		DrainerIterationDuration,
		DrainerRowsProcessed,
		// Go runtime collectors come free with the library. They
		// surface goroutine count, GC pause time, memory residency,
		// open FD count, etc. — invaluable for diagnosing Go-level
		// issues without adding any instrumentation to the app.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// StatusClass converts an HTTP status code to its class label
// ("2xx", "4xx", "5xx" etc.). This is the cardinality-bounded label
// we use on HTTP metrics — using the raw 3-digit code would be
// acceptable (~20 codes) but the class is what most queries actually
// want to filter on.
func StatusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}
