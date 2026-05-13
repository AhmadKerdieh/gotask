// Package middleware holds HTTP middleware used across the application.
//
// keycloak.go (this file) houses the OIDC middleware that will:
//   - extract the Bearer token from Authorization,
//   - validate signature against the realm's JWKS (cached, with rotation),
//   - validate issuer, audience, exp, nbf,
//   - decode claims into a typed struct,
//   - store them in the request context under the key "user_claims".
//
// Implementation lands in Phase 6 (Keycloak Auth).
package middleware
