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
	return nil
}
