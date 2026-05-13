// Package main is the entry point for the gotask API server.
// It wires configuration, the logger, and all HTTP routes, then runs the
// server with graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gotask/internal/config"
	"gotask/pkg/logger"
	"gotask/pkg/response"
)

func main() {
	// Load configuration first. Anything that fails here is fatal — we have
	// no logger yet, so we fall back to stderr via the standard library.
	cfg, err := config.Load()
	if err != nil {
		// Using os.Stderr directly because the logger depends on cfg.
		_, _ = os.Stderr.WriteString("config: " + err.Error() + "\n")
		os.Exit(1)
	}

	log := logger.New(cfg.Env, cfg.LogLevel)

	mux := http.NewServeMux()

	// Health check — used by load balancers, container orchestrators, and the
	// Phase 0 frontend to confirm the server is reachable.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.OK(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "gotask",
			"env":     cfg.Env,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Static frontend (served at "/"). The Go 1.22 mux requires a trailing
	// slash on the pattern for prefix matching.
	mux.Handle("GET /", http.FileServer(http.Dir(cfg.StaticDir)))

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Run the server in a goroutine so we can listen for signals on the
	// main goroutine and trigger a graceful shutdown.
	serverErr := make(chan error, 1)
	go func() {
		log.Info("server starting",
			"addr", server.Addr,
			"env", cfg.Env,
			"static_dir", cfg.StaticDir,
		)
		serverErr <- server.ListenAndServe()
	}()

	// Wait for either a startup error or an OS shutdown signal.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}

	case sig := <-shutdown:
		log.Info("shutdown initiated", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Error("graceful shutdown failed; forcing close", "error", err)
			_ = server.Close()
			os.Exit(1)
		}
		log.Info("shutdown complete")
	}
}
