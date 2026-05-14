package middleware

import "net/http"

// responseWriter wraps http.ResponseWriter to capture the status code and
// byte count that downstream handlers produce. The access log middleware
// wraps the response writer once at the top of the chain; everything inside
// — including the recover middleware — writes through this wrapper, so we
// have an honest view of what the client received.
//
// Why the bool: ResponseWriter.WriteHeader can technically be called
// multiple times; only the first call wins. The bool prevents us from
// overwriting the recorded status if a buggy handler does that.
type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

// wrapWriter returns a responseWriter with status defaulted to 200, matching
// Go's net/http behaviour: if you call Write without calling WriteHeader, the
// server sends 200.
func wrapWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

// WriteHeader records the status the first time it's called and passes the
// call through. Subsequent calls are passed through (matching stdlib
// behaviour, which logs a "superfluous WriteHeader" warning) but do not
// update the recorded status.
func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status = code
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

// Write records the byte count. If WriteHeader was never called, the implicit
// 200 status is locked in here, matching stdlib behaviour.
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Status returns the status code that was (or will be) sent. Useful for
// access logging and for the recover middleware to check whether a panic
// happened after the response was committed.
func (rw *responseWriter) Status() int { return rw.status }

// Wrote reports whether headers have been sent. Recover uses this to decide
// whether it can still write a 500 envelope (true → too late, response
// already committed; false → safe to write).
func (rw *responseWriter) Wrote() bool { return rw.wroteHeader }
