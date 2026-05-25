// Package keycloakadmin is a tiny Admin API client used to resolve a
// Keycloak subject (UUID) into the user's display name and email.
//
// This package exists ONLY because of Option A. Under Option B (local
// users table mirroring Keycloak), this same information would be a
// single SQL JOIN. We chose Option A in Phase 6 — Keycloak is the only
// user store, no local mirror — so the bill comes due here: name/email
// lookup is a network call to Keycloak's Admin REST API, plus the
// engineering that comes with it (token management, caching, graceful
// degradation, configurable timeout).
//
// What we do NOT do here:
//
//   - We do NOT call Keycloak per audit row. We cache by sub with a TTL.
//     A busy audit page might list 100 rows from 5 distinct users; we
//     make 5 calls (cached), not 100.
//
//   - We do NOT block any business operation on Keycloak. A failed
//     lookup degrades the audit response (subject without name); it
//     never causes the audit list itself to fail. Auditing is
//     authoritative because of the local audit_log row; the lookup is
//     decoration.
//
//   - We do NOT use the SPA's PKCE flow here. This is server-to-server,
//     so we use Client Credentials with a confidential client
//     (gotask-backend). The realm grants this client realm-management/
//     view-users so it can read user records.
package keycloakadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// UserInfo is the small slice of Keycloak's user record we actually need.
type UserInfo struct {
	Subject uuid.UUID
	Name    string
	Email   string
}

// Lookup is the interface the service consumes. Hiding the real client
// behind an interface lets tests inject a fake (returning canned answers
// or simulated failures) without spinning up Keycloak.
type Lookup interface {
	GetUser(ctx context.Context, sub uuid.UUID) (UserInfo, error)
}

// Config carries everything NewClient needs. Issuer is the same realm
// URL the auth middleware verifies tokens against; AdminBaseURL is the
// Keycloak root (no realm suffix) because admin endpoints live at
// /admin/realms/<realm>/... — different path tree.
type Config struct {
	Issuer       string        // e.g. http://localhost:8081/realms/gotask
	AdminBaseURL string        // e.g. http://localhost:8081
	Realm        string        // e.g. gotask
	ClientID     string        // the confidential client (gotask-backend)
	ClientSecret string        // shared secret with that client
	CacheTTL     time.Duration // how long to cache a successful GetUser
	Timeout      time.Duration // per-HTTP-call timeout
}

// Client is the production Lookup. It manages its own access token (the
// "service account token" — issued by Keycloak's token endpoint via the
// Client Credentials grant) with simple expiry tracking, and caches user
// records in memory with a TTL.
type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
	cache       map[uuid.UUID]cachedUser
}

type cachedUser struct {
	info   UserInfo
	expiry time.Time
}

// NewClient validates the config and returns a ready Lookup. It does NOT
// perform any network call here: the first GetUser fetches the token.
// Failing here would couple app startup to Keycloak admin readiness,
// which is a different reliability problem from token verification
// readiness (that one IS fatal — see main.go).
func NewClient(cfg Config) (*Client, error) {
	if cfg.AdminBaseURL == "" || cfg.Realm == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("keycloakadmin: AdminBaseURL, Realm, ClientID, ClientSecret are required")
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &Client{
		cfg:   cfg,
		http:  &http.Client{Timeout: cfg.Timeout},
		cache: make(map[uuid.UUID]cachedUser),
	}, nil
}

// GetUser returns the user with the given Keycloak subject. Cache hits
// return immediately. Misses fetch from Keycloak; failures propagate but
// callers (the audit handler) treat them as "fall back to subject only".
func (c *Client) GetUser(ctx context.Context, sub uuid.UUID) (UserInfo, error) {
	if u, ok := c.cacheGet(sub); ok {
		return u, nil
	}

	tok, err := c.serviceToken(ctx)
	if err != nil {
		return UserInfo{}, fmt.Errorf("keycloakadmin: get service token: %w", err)
	}

	// Admin endpoint path:
	//   GET {baseURL}/admin/realms/{realm}/users/{id}
	// Authorization: Bearer <service-account-token>.
	path := fmt.Sprintf("%s/admin/realms/%s/users/%s",
		strings.TrimRight(c.cfg.AdminBaseURL, "/"),
		url.PathEscape(c.cfg.Realm),
		sub.String(),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// A subject in our database not present in Keycloak is unusual
		// but possible (user was deleted in Keycloak after creating
		// tasks). Return a typed sentinel so callers can branch.
		return UserInfo{}, ErrUserNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return UserInfo{}, fmt.Errorf("keycloakadmin: admin api status %d", resp.StatusCode)
	}

	var payload struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return UserInfo{}, err
	}

	info := UserInfo{
		Subject: sub,
		Name:    strings.TrimSpace(payload.FirstName + " " + payload.LastName),
		Email:   payload.Email,
	}
	if info.Name == "" {
		info.Name = payload.Username
	}

	c.cachePut(sub, info)
	return info, nil
}

// ErrUserNotFound is returned when Keycloak responds 404 for a sub.
var ErrUserNotFound = errors.New("keycloakadmin: user not found")

// serviceToken returns a current access token for the service account,
// fetching a new one when the cached one is about to expire. We refresh
// 30 seconds before nominal expiry to avoid races where a request is in
// flight as the token flips invalid.
func (c *Client) serviceToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Add(30*time.Second).Before(c.tokenExpiry) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)

	tokenURL := strings.TrimRight(c.cfg.Issuer, "/") + "/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloakadmin: token endpoint status %d", resp.StatusCode)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", errors.New("keycloakadmin: token endpoint returned empty access_token")
	}

	c.mu.Lock()
	c.token = payload.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	c.mu.Unlock()

	return payload.AccessToken, nil
}

func (c *Client) cacheGet(sub uuid.UUID) (UserInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.cache[sub]; ok && time.Now().Before(v.expiry) {
		return v.info, true
	}
	return UserInfo{}, false
}

func (c *Client) cachePut(sub uuid.UUID, info UserInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[sub] = cachedUser{info: info, expiry: time.Now().Add(c.cfg.CacheTTL)}
}

// Run is the cache cleanup goroutine — Phase 9 backlog item 5
// resolved. The Client's cacheGet checks v.expiry on read, so EXPIRED
// entries return false (cache miss); but the map entries themselves
// are never deleted, so memory grows monotonically with unique sub
// lookups. Over months in production with thousands of users, this
// is a slow leak. Run periodically evicts expired entries.
//
// ── The four questions of every goroutine ────────────────────────────
//
//   1. WHO STARTS IT — main.go, conditionally (only if the real
//      Client was constructed; NoopLookup has no Run method).
//   2. WHO STOPS IT — context cancellation. Same root context as the
//      drainer, propagated via errgroup.
//   3. WHAT HAPPENS IN-FLIGHT — eviction is fast (a single map scan
//      under the cache mutex); cancellation between iterations is
//      always clean. No need for the iterCtx pattern the drainer uses
//      because no transaction is involved.
//   4. WHAT HAPPENS ON PANIC — wrapped in deferred recover. The work
//      is non-critical (memory hygiene); a panic logs and exits, the
//      errgroup observes the goroutine return.
//
// This is the second long-lived goroutine in the codebase. Contrast
// with audit/drainer.go: same discipline applied to much simpler
// work. The shape is identical because the SHAPE is the point —
// every long-lived goroutine deserves these four answers, regardless
// of complexity.
func (c *Client) Run(ctx context.Context, log *slog.Logger) error {
	defer func() {
		if p := recover(); p != nil {
			log.Error("keycloakadmin cache cleanup panic", "panic", p)
		}
	}()

	// One eviction per CacheTTL is the right cadence: any entry not
	// touched for that long is definitely expired. More frequent
	// would waste cycles; less frequent would let stale entries
	// linger pointlessly.
	ticker := time.NewTicker(c.cfg.CacheTTL)
	defer ticker.Stop()

	log.Info("keycloakadmin cache cleanup started", "interval", c.cfg.CacheTTL)

	for {
		select {
		case <-ctx.Done():
			log.Info("keycloakadmin cache cleanup stopping", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			evicted := c.evictExpired()
			if evicted > 0 {
				log.Debug("keycloakadmin cache: evicted expired entries", "count", evicted)
			}
		}
	}
}

// evictExpired removes cache entries whose expiry has passed. Returns
// the number of entries evicted (for logging / future metric).
func (c *Client) evictExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	evicted := 0
	for sub, v := range c.cache {
		if now.After(v.expiry) {
			delete(c.cache, sub)
			evicted++
		}
	}
	return evicted
}

// NoopLookup is the safe fallback: it returns an empty UserInfo for
// every sub. main.go uses this when admin-client config is missing, so
// the app still runs (audit shows subjects without names) and Keycloak
// isn't a hard runtime dependency for the WHOLE app — only for
// name-enriched audit responses.
type NoopLookup struct{}

func (NoopLookup) GetUser(_ context.Context, sub uuid.UUID) (UserInfo, error) {
	return UserInfo{Subject: sub}, nil
}
