package middleware

import (
	"net/http"

	"gotask/pkg/response"
)

// RequireRole returns middleware that rejects requests whose verified
// claims do not include any of the named roles. Use this for ROUTE-COARSE
// authorization — "anyone with manager OR admin may even attempt this
// endpoint." It is the *only* kind of authorization that belongs in
// middleware: it needs nothing but the claims and the route to decide.
//
// Resource-fine checks ("can THIS user act on THIS task?") must NOT live
// here; they belong in the service layer, because they require loading
// the resource to know who owns it. Putting them in middleware would
// force middleware to start calling repositories — exactly the layering
// erosion we've spent earlier phases preventing.
//
// On failure: 403 Forbidden. Not 401 — by the time RequireRole runs, the
// auth middleware has already established a verified identity. The
// failure is "I know who you are, but you may not do this" — the textbook
// definition of 403.
//
// Must be composed AFTER the auth middleware in the chain so claims are
// in context. If not (a programmer error), HasAny returns false and the
// request is denied, which fails safe.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := UserClaimsFromContext(r.Context())
			if !ok || !claims.HasAny(roles...) {
				response.Error(w, http.StatusForbidden, "forbidden",
					"the authenticated user does not have permission for this action")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
