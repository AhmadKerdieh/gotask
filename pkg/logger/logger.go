// Package logger provides a thin wrapper around log/slog that picks an
// appropriate handler (text or JSON) based on the running environment.
// Returning *slog.Logger means the rest of the app uses the standard
// library API directly — no custom logger interface to maintain.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a *slog.Logger configured for the given environment and level.
//
//	env == "production"   → JSON to stdout (machine-friendly, ships to log
//	                       aggregators cleanly).
//	any other env         → human-friendly text to stdout.
//
// level is parsed case-insensitively; unknown values default to INFO.
func New(env, level string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     parseLevel(level),
		AddSource: env != "production", // file:line in dev, omitted in prod for noise
	}

	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}
