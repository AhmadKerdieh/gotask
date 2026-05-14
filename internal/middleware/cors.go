package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig controls Cross-Origin Resource Sharing behaviour.
//
// AllowedOrigins is a strict allowlist. We deliberately do NOT support the
// "*" wildcard in production code paths; if you want to accept everything
// during local development, list "http://localhost:5173" (or whatever your
// dev origin is) explicitly. This avoids the very common security mistake
// of leaving a wildcard in production along with Allow-Credentials.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	MaxAge         int // seconds; how long the browser may cache preflight
}

// CORS returns middleware that enforces the configured policy.
//
// Behaviour:
//
//   - For requests with no Origin header: pass through untouched. These are
//     same-origin or non-browser requests (curl, server-to-server) and CORS
//     doesn't apply.
//   - For requests with an Origin we DON'T allow: pass through, but omit
//     Allow-Origin. The browser will block the response from JavaScript.
//   - For requests with an allowed Origin: set Allow-Origin (echoing the
//     specific origin, never "*"), Vary: Origin, Allow-Credentials.
//   - For OPTIONS preflight from an allowed origin: also set Allow-Methods,
//     Allow-Headers, Max-Age, and return 204 immediately — never invoke the
//     handler.
//
// Echoing the specific origin (rather than "*") is required when sending
// credentials and is also better for caches: a response cached for origin
// A won't be served to origin B.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	// Build O(1) lookup for the allowlist once at construction.
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o != "" {
			allowed[o] = struct{}{}
		}
	}
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, ok := allowed[origin]

			if origin != "" && ok {
				// Echo the specific origin — never "*" — so credentials work.
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				// Vary tells caches and CDNs that the response depends on
				// the Origin request header.
				w.Header().Add("Vary", "Origin")
			}

			// Preflight requests don't reach the handler; we answer here.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if ok {
					w.Header().Set("Access-Control-Allow-Methods", methods)
					w.Header().Set("Access-Control-Allow-Headers", headers)
					if cfg.MaxAge > 0 {
						w.Header().Set("Access-Control-Max-Age", maxAge)
					}
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
