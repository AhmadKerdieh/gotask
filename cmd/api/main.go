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
	"gotask/internal/database"
	"gotask/internal/repository"
	"gotask/internal/server"
	"gotask/internal/service"
	"gotask/internal/validator"
	"gotask/pkg/logger"
)

// httpTimeoutGrace is the buffer added to the application-level request
// timeout when configuring http.Server's transport-level timeouts.
//
// http.Server.WriteTimeout is enforced at the TCP layer — when it fires
// the connection's write deadline expires and any subsequent Write()
// returns an error. Our middleware/Timeout operates higher up: it cancels
// the request context so the handler can serialize and write a clean
// {"error":{"code":"timeout"}} envelope.
//
// If WriteTimeout equals our RequestTimeout, both fire simultaneously and
// race — the handler's Write loses, the client sees a dropped connection
// with no body, and browsers sit on the dead socket for minutes before
// failing the fetch. Setting WriteTimeout = RequestTimeout + grace
// guarantees our middleware always finishes its 504 envelope before the
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

	// Load workflow.yaml — the authoritative declaration of legal statuses,
	// allowed transitions, and priorities. Loaded once; treated as
	// immutable for the life of the process.
	wf, err := config.LoadWorkflow(cfg.WorkflowPath)
	if err != nil {
		log.Error("workflow load failed", "error", err, "path", cfg.WorkflowPath)
		os.Exit(1)
	}
	log.Info("workflow loaded",
		"path", cfg.WorkflowPath,
		"statuses", wf.Statuses,
		"priorities", wf.Priorities,
		"default_status", wf.DefaultStatus,
		"default_priority", wf.DefaultPriority,
	)

	// Construct the request validator once and share it across handlers.
	// It is safe for concurrent use.
	vld := validator.New(wf)

	// Connect the Postgres pool. A failure here is fatal: there is no
	// point serving traffic with no database. The bounded ConnectTimeout
	// means an unreachable database fails startup in seconds, not after a
	// multi-minute OS TCP timeout.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.NewPool(dbCtx, database.Config{
		URL:             cfg.DatabaseURL,
		MaxConns:        int32(cfg.DBMaxConns),
		MinConns:        int32(cfg.DBMinConns),
		MaxConnLifetime: time.Hour,
		ConnectTimeout:  5 * time.Second,
	})
	dbCancel()
	if err != nil {
		log.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	// Closed during graceful shutdown (see the shutdown branch below).
	// Close blocks until in-flight queries finish and connections drain.
	log.Info("database connected",
		"max_conns", cfg.DBMaxConns,
		"min_conns", cfg.DBMinConns,
	)

	// Build the repositories on top of the pool. These are the only
	// objects that know SQL exists; everything above them depends on the
	// repository interfaces, not these concrete values.
	taskRepo := repository.NewTaskRepository(db.Pool)
	projectRepo := repository.NewProjectRepository(db.Pool)

	// Build the services on top of the repositories. The services receive
	// the repository INTERFACES (taskRepo/projectRepo satisfy them) plus
	// the workflow. This is the seam that makes the service unit-testable
	// against in-memory fakes — see internal/service/*_test.go. In
	// production the same constructor gets the Postgres-backed repos.
	svcDeps := service.Deps{
		Tasks:    taskRepo,
		Projects: projectRepo,
		Workflow: wf,
	}
	taskSvc := service.NewTaskService(svcDeps)
	projectSvc := service.NewProjectService(svcDeps)

	// Build the HTTP handler. server.NewRouter is the single place routes
	// and middleware are composed; main just hands over dependencies.
	handler := server.NewRouter(server.Deps{
		Config:     cfg,
		Logger:     log,
		Workflow:   wf,
		Validator:  vld,
		DB:         db,
		TaskSvc:    taskSvc,
		ProjectSvc: projectSvc,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + httpTimeoutGrace,
		WriteTimeout:      cfg.RequestTimeout + httpTimeoutGrace,
		IdleTimeout:       60 * time.Second,
	}

	// Run the server in a goroutine so the main goroutine can wait on
	// signals and trigger a graceful shutdown.
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
			db.Close()
			os.Exit(1)
		}

		// Close the pool only AFTER the HTTP server has stopped accepting
		// and finished in-flight requests. Closing it earlier would yank
		// the database out from under requests that are still running.
		db.Close()
		log.Info("shutdown complete")
	}
}
