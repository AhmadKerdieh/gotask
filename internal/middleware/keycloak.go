package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"gotask/pkg/response"
)

// Authenticator wraps a verified OIDC setup. It is constructed once at
// startup (it performs OIDC discovery against the issuer, which is a
// network call we want to do exactly once, not per request) and then
// shared. Its Middleware method is the per-request guard.
//
// What "verified" means here, concretely — the five checks go-oidc's
// Verifier performs on every token, none of which we may skip:
//
//  1. SIGNATURE: recompute against Keycloak's public key (fetched from
//     the realm's JWKS endpoint, cached, auto-refreshed on key rotation).
//     A forged or tampered token fails here.
//  2. ISSUER (iss): must equal our realm issuer exactly. Stops a token
//     from a different realm/IdP.
//  3. AUDIENCE (aud): must include our client id. Stops a token minted
//     for a different app in the same realm being replayed against us.
//     This is the check people most often forget; omitting it is a real
//     vulnerability.
//  4. EXPIRY (exp): must be in the future.
//  5. NOT-BEFORE / ISSUED-AT: time-window sanity.
//
// We do local verification (pure CPU, the public key is cached) rather
// than calling Keycloak's introspection endpoint per request. A token is
// trusted because the math proves Keycloak signed it, not because we
// asked Keycloak — that keeps Keycloak off the hot path for every API
// call.
type Authenticator struct {
	verifier *oidc.IDTokenVerifier
}

// NewAuthenticator performs OIDC discovery against issuer (one network
// call, at startup) and builds a verifier bound to clientID as the
// expected audience. A failure here is fatal: the application must not
// start if it cannot establish how to verify tokens — serving traffic
// while unable to authenticate is worse than not starting.
func NewAuthenticator(ctx context.Context, issuer, clientID string) (*Authenticator, error) {
	// oidc.NewProvider fetches <issuer>/.well-known/openid-configuration
	// and from it learns the JWKS URI. We never hardcode key URLs; the
	// discovery document is the single source of that truth.
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}

	// The verifier is configured with the expected audience (clientID).
	// go-oidc enforces signature, iss, aud, exp, nbf. We keep its
	// defaults — they are the secure ones — and only set the audience.
	verifier := provider.Verifier(&oidc.Config{
		ClientID: clientID,
	})

	return &Authenticator{verifier: verifier}, nil
}

// rawClaims is the subset of the token payload we extract. Keycloak puts
// the user id in "sub"; email/name come from the standard OIDC profile
// scopes; realm-level roles arrive nested under realm_access.roles. We
// bind only what we use — extra claims are ignored, not an error.
type rawClaims struct {
	Subject     string `json:"sub"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Middleware is the per-request guard. On success it injects the verified
// identity into the request context (via WithUser) and calls next. On any
// failure it writes a 401 in the standard envelope and does NOT call next
// — an unauthenticated request must never reach a protected handler.
//
// 401 vs 403 distinction (kept precise on purpose):
//   - 401 Unauthorized: the request lacks a valid identity (no token,
//     malformed, expired, bad signature, wrong issuer/audience). "I do
//     not know who you are."
//   - 403 Forbidden would mean "I know who you are but you may not do
//     this" — that is an AUTHORIZATION decision and belongs to Phase 7,
//     not here. This middleware only ever emits 401.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := bearerToken(r)
		if err != nil {
			unauthorized(w, "missing or malformed Authorization header")
			return
		}

		// The single call that performs all five checks. A non-nil error
		// here means the token failed verification for SOME reason; we
		// deliberately do NOT echo the library's error detail to the
		// client (it can leak token internals) — a generic 401 is the
		// correct response to every verification failure. The detail is
		// available server-side via logging if needed.
		idToken, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			unauthorized(w, "invalid or expired token")
			return
		}

		var claims rawClaims
		if err := idToken.Claims(&claims); err != nil {
			unauthorized(w, "token claims could not be read")
			return
		}
		if claims.Subject == "" {
			// A token with no subject is unusable as an identity even if
			// it verified — refuse rather than proceed with an empty user.
			unauthorized(w, "token has no subject")
			return
		}

		ctx := WithUser(r.Context(), UserClaims{
			Subject: claims.Subject,
			Email:   claims.Email,
			Name:    claims.Name,
			Roles:   claims.RealmAccess.Roles,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the raw JWT from "Authorization: Bearer <token>".
// It is strict about the scheme: a missing header, a non-Bearer scheme,
// or an empty token are all rejected. Strictness here is correct — a
// fuzzy parser is an attack surface.
func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("no authorization header")
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", errors.New("authorization header is not a Bearer token")
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}

// unauthorized writes a 401 in the standard envelope. Centralised so
// every auth-failure path is identical and the message is generic — we
// never tell an attacker WHICH check failed.
func unauthorized(w http.ResponseWriter, msg string) {
	response.Error(w, http.StatusUnauthorized, "unauthorized", msg)
}
