// Package main is the entry point for the gotask API server.
//
// Its only job is to wire dependencies and run the server with graceful
// shutdown. All routing and middleware lives in internal/server; all
// configuration loading lives in internal/config; etc. This separation
// means integration tests can build the same router without running this
// binary, and `main` stays small enough to read at a glance.
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
	"gotask/internal/server"
	"gotask/pkg/logger"
)

// httpTimeoutGrace is the buffer added to the application-level request
// timeout when configuring http.Server's transport-level timeouts.
//
// Why this matters: http.Server.WriteTimeout is enforced at the TCP
// connection layer — when it fires, the connection's write deadline expires
// and any subsequent Write() returns an error. Our middleware/Timeout
// operates at a higher layer: it cancels the request context so the handler
// can serialize and write a clean {"error":{"code":"timeout"}} envelope.
//
// If WriteTimeout equals our RequestTimeout, both fire simultaneously and
// race — typically the handler's Write loses, the client sees a dropped
// connection with no body, and browsers sit on the dead socket for minutes
// before failing the fetch. Setting WriteTimeout = RequestTimeout + grace
// guarantees our middleware always finishes writing its response before the
// stdlib server tears the connection down.
const httpTimeoutGrace = 5 * time.Second

func main() {
	// Load configuration first. Anything that fails here is fatal — we have
	// no logger yet, so we fall back to stderr via the standard library.
	cfg, err := config.Load()
	if err != nil {
		_, _ = os.Stderr.WriteString("config: " + err.Error() + "\n")
		os.Exit(1)
	}

	log := logger.New(cfg.Env, cfg.LogLevel)

	// Build the HTTP handler (router + middleware + routes). Everything
	// about how requests are processed lives in this one call.
	handler := server.NewRouter(server.Deps{
		Config: cfg,
		Logger: log,
	})

	// Read/Write timeouts deliberately exceed cfg.RequestTimeout so the
	// application-level Timeout middleware always wins the race and can
	// emit its 504 envelope before the stdlib server tears the connection
	// down. See the httpTimeoutGrace comment above.
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + httpTimeoutGrace,
		WriteTimeout:      cfg.RequestTimeout + httpTimeoutGrace,
		IdleTimeout:       60 * time.Second,
	}

	// Run the server in a goroutine so we can listen for signals on the
	// main goroutine and trigger a graceful shutdown.
	serverErr := make(chan error, 1)
	go func() {
		log.Info("server starting",
			"addr", srv.Addr,
			"env", cfg.Env,
			"request_timeout", cfg.RequestTimeout.String(),
			"http_write_timeout", srv.WriteTimeout.String(),
			"cors_allowed_origins", cfg.CORSAllowedOrigins,
		)
		serverErr <- srv.ListenAndServe()
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

		if err := srv.Shutdown(ctx); err != nil {
			log.Error("graceful shutdown failed; forcing close", "error", err)
			_ = srv.Close()
			os.Exit(1)
		}
		log.Info("shutdown complete")
	}
}
