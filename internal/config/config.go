// Package config loads application configuration from a .env file and
// environment variables using Viper. Environment variables always win over
// values from the .env file, which is important for production deployments
// where you do not ship a .env at all.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

// Config holds every setting the application needs. Add fields here as new
// phases introduce new dependencies (database URL in Phase 3, Keycloak
// endpoints in Phase 6, etc.). Keep the struct flat — nested config is
// rarely worth the indirection at this size.
type Config struct {
	Env       string `mapstructure:"ENV"`
	Port      string `mapstructure:"PORT"`
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	StaticDir string `mapstructure:"STATIC_DIR"`

	// Phase 1 additions.
	//
	// CORSAllowedOrigins is a comma-separated list in the .env file, e.g.
	//   CORS_ALLOWED_ORIGINS=http://localhost:8080,http://localhost:5173
	// Viper's StringToSliceHookFunc splits it into []string at unmarshal
	// time. Strict allowlist — there is no "allow all" mode.
	CORSAllowedOrigins []string `mapstructure:"CORS_ALLOWED_ORIGINS"`

	// RequestTimeout is the per-request deadline applied by the timeout
	// middleware. Format: any string time.ParseDuration accepts ("15s",
	// "200ms", "1m"). StringToTimeDurationHookFunc decodes it.
	RequestTimeout time.Duration `mapstructure:"REQUEST_TIMEOUT"`

	// WorkflowPath is the location of workflow.yaml. Defaults to
	// ./workflow.yaml so `go run ./cmd/api` from the repo root works
	// out of the box. In production set this to an absolute path
	// (e.g. /etc/gotask/workflow.yaml) and ship the file alongside
	// the binary.
	WorkflowPath string `mapstructure:"WORKFLOW_PATH"`

	// DatabaseURL is the libpq-style Postgres connection string. There is
	// no default — running without a database is never correct for this
	// application, so an empty value is a hard startup error rather than
	// a silent fallback to some localhost guess.
	DatabaseURL string `mapstructure:"DATABASE_URL"`

	// DBMaxConns / DBMinConns size the connection pool. The defaults are
	// modest on purpose: Postgres has a global max_connections budget
	// (default 100) shared by every client, so a single service should
	// claim a known, bounded slice of it.
	DBMaxConns int `mapstructure:"DB_MAX_CONNS"`
	DBMinConns int `mapstructure:"DB_MIN_CONNS"`

	// ── OIDC / Keycloak (Phase 6) ──────────────────────────────────────
	//
	// OIDCIssuer is the realm's issuer URL, e.g.
	//   http://localhost:8081/realms/gotask
	// The middleware performs OIDC discovery against
	// <issuer>/.well-known/openid-configuration to find the JWKS endpoint;
	// it never hardcodes key URLs.
	//
	// OIDCClientID is this application's client as registered in the
	// realm. Every token's "aud" (audience) claim MUST include this value
	// — that check is what stops a token minted for a different app in
	// the same realm being replayed against us.
	//
	// There is no client SECRET here on purpose: the only OAuth client is
	// the browser SPA, which is a PUBLIC client using PKCE. A secret in
	// JavaScript is not a secret; PKCE is the correct substitute.
	OIDCIssuer   string `mapstructure:"OIDC_ISSUER"`
	OIDCClientID string `mapstructure:"OIDC_CLIENT_ID"`

	// ── Keycloak Admin API (Phase 7, Option A consequence) ────────────
	//
	// These configure the server-to-server lookup used to enrich audit
	// responses with real names/emails. They are OPTIONAL: if either
	// client id or secret is unset, the app falls back to a noop lookup
	// and audit responses show subjects only. This bounds the runtime
	// dependency on Keycloak to "audit response enrichment", not the
	// whole app.
	//
	// KeycloakAdminBaseURL is the Keycloak host (no realm path), e.g.
	// http://localhost:8081. The Admin API lives at /admin/realms/...
	// which is a different path tree from the OIDC discovery URL.
	KeycloakAdminBaseURL      string `mapstructure:"KEYCLOAK_ADMIN_BASE_URL"`
	KeycloakRealm             string `mapstructure:"KEYCLOAK_REALM"`
	KeycloakAdminClientID     string `mapstructure:"KEYCLOAK_ADMIN_CLIENT_ID"`
	KeycloakAdminClientSecret string `mapstructure:"KEYCLOAK_ADMIN_CLIENT_SECRET"`

	// ── Phase 8: concurrency & lifecycle ───────────────────────────────
	//
	// AuditDrainInterval is how often the audit drainer polls the
	// audit_outbox table when there's no recent work. Shorter = lower
	// audit lag, more empty queries. 1s is the conservative starting
	// value; if you saw outbox growth in metrics you'd lower this
	// before raising BatchSize.
	AuditDrainInterval time.Duration `mapstructure:"AUDIT_DRAIN_INTERVAL"`

	// AuditDrainBatchSize bounds how many outbox rows the drainer
	// processes per iteration. Higher = better commit amortisation;
	// lower = shorter per-iteration lock window.
	AuditDrainBatchSize int `mapstructure:"AUDIT_DRAIN_BATCH_SIZE"`

	// ShutdownTimeout bounds graceful shutdown. Should comfortably
	// exceed RequestTimeout (15s) so an in-flight long request can
	// finish; we default to 30s. Phase 5 flagged this as backlog item
	// 2 ("graceful-shutdown timeout < REQUEST_TIMEOUT") — Phase 8
	// resolves it.
	ShutdownTimeout time.Duration `mapstructure:"SHUTDOWN_TIMEOUT"`

	// PProfEnabled gates whether /debug/pprof/* handlers are mounted.
	// Off by default so production deployments don't expose them by
	// accident; flip to true via env when an incident calls for
	// profiling. Zero runtime cost when false — the handler is just
	// not registered.
	PProfEnabled bool `mapstructure:"PPROF_ENABLED"`
}

// Load reads the .env file (if present) and overlays environment variables,
// returning a populated Config. A missing .env file is not an error — that
// is the expected state in production.
func Load() (*Config, error) {
	v := viper.New()

	// Defaults are the source of truth for "what does this app do if
	// nothing is configured?". Keep them sensible for local development.
	v.SetDefault("ENV", "development")
	v.SetDefault("PORT", "8080")
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("STATIC_DIR", "./static")
	v.SetDefault("CORS_ALLOWED_ORIGINS", "http://localhost:8080")
	v.SetDefault("REQUEST_TIMEOUT", "15s")
	v.SetDefault("WORKFLOW_PATH", "./workflow.yaml")
	v.SetDefault("DB_MAX_CONNS", 10)
	v.SetDefault("DB_MIN_CONNS", 2)
	v.SetDefault("OIDC_ISSUER", "http://localhost:8081/realms/gotask")
	v.SetDefault("OIDC_CLIENT_ID", "gotask-spa")
	v.SetDefault("KEYCLOAK_ADMIN_BASE_URL", "http://localhost:8081")
	v.SetDefault("KEYCLOAK_REALM", "gotask")
	v.SetDefault("AUDIT_DRAIN_INTERVAL", "1s")
	v.SetDefault("AUDIT_DRAIN_BATCH_SIZE", 100)
	v.SetDefault("SHUTDOWN_TIMEOUT", "30s")
	v.SetDefault("PPROF_ENABLED", false)

	// Bind env vars that have no SetDefault. Viper's AutomaticEnv only
	// surfaces env vars for keys it has already seen via SetDefault, a
	// loaded config file, or an explicit BindEnv. When the .env file is
	// PRESENT (the make-run path) Viper learns these keys from the file
	// and AutomaticEnv works transparently — but in a containerised
	// deploy where there's no .env on disk and DATABASE_URL is supplied
	// via `-e`, Viper would silently fail to bind it. These BindEnv
	// calls close that gap. The keys here are exactly those that:
	//   (a) have no SetDefault (because they're required-with-no-default
	//       or are optional-without-fallback), AND
	//   (b) callers might legitimately set via env in a no-.env-file
	//       deployment.
	_ = v.BindEnv("DATABASE_URL")
	_ = v.BindEnv("KEYCLOAK_ADMIN_CLIENT_ID")
	_ = v.BindEnv("KEYCLOAK_ADMIN_CLIENT_SECRET")

	// .env file lookup.
	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")
	v.AddConfigPath("./../..")
	v.AddConfigPath("/etc/gotask")

	if err := v.ReadInConfig(); err != nil {
		// "file not found" is fine — env vars can provide everything.
		// Any other read error (parse failure, permission denied) is fatal.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	// Bind environment variables. AutomaticEnv binds any env var whose name
	// matches a known key. The replacer lets users write `LOG_LEVEL=debug`
	// rather than the literal `log.level`.
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	var cfg Config

	// Decode hooks teach mapstructure how to convert string inputs (which is
	// what every env var is) into richer Go types:
	//   - StringToTimeDurationHookFunc:  "15s" → time.Duration
	//   - StringToSliceHookFunc(","):    "a,b,c" → []string{"a","b","c"}
	hooks := mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)

	if err := v.Unmarshal(&cfg, viper.DecodeHook(hooks)); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validate runs basic sanity checks. As the config grows, this is the place
// to assert that required fields are non-empty, ports parse to a uint16,
// URLs are well-formed, etc.
func (c *Config) validate() error {
	if c.Port == "" {
		return fmt.Errorf("config: PORT must not be empty")
	}
	if c.StaticDir == "" {
		return fmt.Errorf("config: STATIC_DIR must not be empty")
	}
	switch c.Env {
	case "development", "staging", "production", "test":
		// ok
	default:
		return fmt.Errorf("config: ENV %q is not one of development|staging|production|test", c.Env)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("config: REQUEST_TIMEOUT must be positive, got %s", c.RequestTimeout)
	}
	if len(c.CORSAllowedOrigins) == 0 {
		return fmt.Errorf("config: CORS_ALLOWED_ORIGINS must list at least one origin")
	}
	if c.WorkflowPath == "" {
		return fmt.Errorf("config: WORKFLOW_PATH must not be empty")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("config: DATABASE_URL is required (no default — see .env.example)")
	}
	if c.DBMaxConns <= 0 {
		return fmt.Errorf("config: DB_MAX_CONNS must be positive, got %d", c.DBMaxConns)
	}
	if c.DBMinConns < 0 || c.DBMinConns > c.DBMaxConns {
		return fmt.Errorf("config: DB_MIN_CONNS must be between 0 and DB_MAX_CONNS (%d), got %d", c.DBMaxConns, c.DBMinConns)
	}
	if c.OIDCIssuer == "" {
		return fmt.Errorf("config: OIDC_ISSUER is required (the Keycloak realm issuer URL)")
	}
	if c.OIDCClientID == "" {
		return fmt.Errorf("config: OIDC_CLIENT_ID is required (this app's client in the realm)")
	}
	if c.AuditDrainInterval <= 0 {
		return fmt.Errorf("config: AUDIT_DRAIN_INTERVAL must be positive, got %s", c.AuditDrainInterval)
	}
	if c.AuditDrainBatchSize <= 0 {
		return fmt.Errorf("config: AUDIT_DRAIN_BATCH_SIZE must be positive, got %d", c.AuditDrainBatchSize)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("config: SHUTDOWN_TIMEOUT must be positive, got %s", c.ShutdownTimeout)
	}
	if c.ShutdownTimeout <= c.RequestTimeout {
		// The Phase 5 backlog item: shutdown must comfortably exceed
		// per-request timeout so an in-flight long request can finish.
		return fmt.Errorf("config: SHUTDOWN_TIMEOUT (%s) must exceed REQUEST_TIMEOUT (%s)",
			c.ShutdownTimeout, c.RequestTimeout)
	}
	return nil
}
