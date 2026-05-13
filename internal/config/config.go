// Package config loads application configuration from a .env file and
// environment variables using Viper. Environment variables always win over
// values from the .env file, which is important for production deployments
// where you do not ship a .env at all.
package config

import (
	"fmt"
	"strings"

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

	// .env file lookup. AddConfigPath calls are searched in order; we add
	// the project root and a couple of common alternates so the binary
	// works whether you `go run ./cmd/api` from the repo root or run a
	// compiled binary from inside ./cmd/api.
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

	// Environment variable handling. AutomaticEnv binds any env var whose
	// name matches a known key. The key replacer lets users write
	// `LOG_LEVEL=debug` rather than the literal `log.level`.
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
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
	return nil
}
