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

	"golang.org/x/sync/errgroup"

	"gotask/internal/audit"
	"gotask/internal/config"
	"gotask/internal/database"
	"gotask/internal/keycloakadmin"
	"gotask/internal/middleware"
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
	// Construct the Postgres auditor before the services, because the
	// services depend on it (they emit audit events on every mutation).
	auditor := audit.NewPostgres(db.Pool)

	// Build the services on top of the repositories and the auditor.
	svcDeps := service.Deps{
		Tasks:    taskRepo,
		Projects: projectRepo,
		Workflow: wf,
		DB:       db,
		Auditor:  auditor,
	}
	taskSvc := service.NewTaskService(svcDeps)
	projectSvc := service.NewProjectService(svcDeps)

	// Construct the OIDC authenticator. This performs OIDC discovery
	// against the Keycloak realm issuer — one network call, here at
	// startup, NOT per request. A failure is fatal: an app that cannot
	// establish how to verify tokens must not serve protected traffic.
	//
	// Note this means Keycloak must be reachable at startup. If you see
	// this fail, the realm isn't up yet — `make kc-up` and wait for it to
	// be healthy before `make run` (the Makefile target waits for you).
	authCtx, authCancel := context.WithTimeout(context.Background(), 15*time.Second)
	auth, err := middleware.NewAuthenticator(authCtx, cfg.OIDCIssuer, cfg.OIDCClientID)
	authCancel()
	if err != nil {
		log.Error("oidc discovery failed",
			"error", err,
			"issuer", cfg.OIDCIssuer,
			"hint", "is Keycloak running and the realm imported? (make kc-up)",
		)
		os.Exit(1)
	}
	log.Info("oidc authenticator ready",
		"issuer", cfg.OIDCIssuer,
		"client_id", cfg.OIDCClientID,
	)

	// Build the Keycloak Admin API lookup if configured. If the admin
	// client id/secret are not set, fall back to the noop lookup so the
	// app still runs — audit responses degrade gracefully to subject-only.
	// This is Option A's "Keycloak is a runtime dependency for user info"
	// made explicit and bounded: if Keycloak admin is unreachable, the
	// app continues to function, just without enriched audit responses.
	var userLookup keycloakadmin.Lookup
	if cfg.KeycloakAdminClientID != "" && cfg.KeycloakAdminClientSecret != "" {
		client, lerr := keycloakadmin.NewClient(keycloakadmin.Config{
			Issuer:       cfg.OIDCIssuer,
			AdminBaseURL: cfg.KeycloakAdminBaseURL,
			Realm:        cfg.KeycloakRealm,
			ClientID:     cfg.KeycloakAdminClientID,
			ClientSecret: cfg.KeycloakAdminClientSecret,
		})
		if lerr != nil {
			log.Error("keycloak admin lookup disabled", "error", lerr)
			userLookup = keycloakadmin.NoopLookup{}
		} else {
			userLookup = client
			log.Info("keycloak admin lookup ready",
				"admin_base_url", cfg.KeycloakAdminBaseURL,
				"realm", cfg.KeycloakRealm,
			)
		}
	} else {
		userLookup = keycloakadmin.NoopLookup{}
		log.Info("keycloak admin lookup disabled (no admin credentials configured); audit responses will show subjects only")
	}

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
		Auth:       auth,
		Auditor:    auditor,
		UserLookup: userLookup,
	})

	// Construct the audit drainer. This is the headline goroutine of
	// Phase 8 — it owns the asynchronous outbox→audit_log pipeline.
	// See internal/audit/drainer.go for the four-questions discipline
	// applied to its design.
	drainer := audit.NewDrainer(db.Pool, log, audit.DrainerConfig{
		PollInterval: cfg.AuditDrainInterval,
		BatchSize:    cfg.AuditDrainBatchSize,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + httpTimeoutGrace,
		WriteTimeout:      cfg.RequestTimeout + httpTimeoutGrace,
		IdleTimeout:       60 * time.Second,
	}

	// ── Process lifecycle (Phase 8) ──────────────────────────────────
	//
	// We have two long-lived goroutines that need coordinated shutdown:
	//
	//   1. The HTTP server (accepts requests, runs handlers)
	//   2. The audit drainer (consumes from audit_outbox, writes to
	//      audit_log)
	//
	// The choice from Path A was STRICTLY SEQUENTIAL shutdown:
	//
	//   SIGTERM → stop accepting new requests (HTTP first)
	//           → wait for in-flight requests to finish
	//           → drain remaining audit_outbox rows
	//           → close DB pool
	//
	// Why this order:
	//
	//   - Stopping HTTP first guarantees no NEW outbox rows arrive
	//     after this point. The drainer's "drain everything pending"
	//     is well-defined.
	//
	//   - The drainer must finish AFTER HTTP because any in-flight
	//     mutation that committed during HTTP shutdown produces a row
	//     the drainer is responsible for moving to audit_log. Closing
	//     the drainer first would orphan those rows in the outbox
	//     until next start — they'd eventually drain, but the
	//     durability promise is "drained at shutdown", not "drained
	//     eventually".
	//
	//   - The DB pool is closed LAST because both the HTTP handlers
	//     and the drainer need it until they're done.
	//
	// The signal handler triggers this by cancelling a root context
	// the drainer observes. The HTTP server is shut down explicitly
	// via srv.Shutdown(ctx) because it doesn't itself observe a
	// context — Go's http.Server pre-dates context cancellation as
	// the universal shutdown signal.

	// Root context cancelled when SIGTERM arrives. Drainer observes it.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Signal handler converts SIGTERM/SIGINT into rootCtx cancellation.
	// We use a dedicated goroutine because signal.Notify pushes to a
	// channel and we want the rest of main to be the lifecycle code.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-shutdown
		log.Info("shutdown signal received", "signal", sig.String())
		rootCancel()
	}()

	// Start the drainer in its own goroutine. Run blocks until ctx is
	// cancelled, then returns nil on clean shutdown. errgroup is
	// overkill for this many goroutines, but using it here keeps the
	// pattern in front of us — future workers slot in as additional
	// g.Go calls without restructuring.
	g, gctx := errgroup.WithContext(rootCtx)
	g.Go(func() error {
		return drainer.Run(gctx)
	})

	// Start the HTTP server. We do NOT add it as g.Go because Go's
	// http.Server has a different shutdown protocol (Shutdown(ctx)
	// rather than context-observation); mixing the two in the same
	// errgroup makes the lifecycle harder to read, not easier. We
	// run it in a plain goroutine and wait for its error separately.
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

	// Main goroutine: wait for either a server crash or a shutdown
	// signal (which cancels gctx, which we observe via gctx.Done()).
	select {
	case err := <-serverErr:
		// Server stopped on its own — usually means a bind failure on
		// startup. http.ErrServerClosed is the clean-Shutdown sentinel
		// and only appears AFTER we've called srv.Shutdown, which
		// hasn't happened in this branch.
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			rootCancel() // signal drainer to stop
			_ = g.Wait()
			db.Close()
			os.Exit(1)
		}

	case <-gctx.Done():
		// SIGTERM arrived (rootCtx cancelled) OR the drainer returned
		// an error (errgroup cancels gctx on first error). Either way
		// it's time to shut down everything else.

		// Step 1: stop HTTP. srv.Shutdown blocks until in-flight
		// requests finish OR the shutdown ctx times out. We bound
		// this independently of the cancelled rootCtx because using
		// the cancelled ctx would make Shutdown return immediately
		// without draining — we want it to actually wait.
		log.Info("step 1/3: stopping HTTP server (draining in-flight requests)")
		httpShutdownCtx, httpCancel := context.WithTimeout(
			context.Background(),
			cfg.ShutdownTimeout,
		)
		if err := srv.Shutdown(httpShutdownCtx); err != nil {
			log.Error("graceful HTTP shutdown failed; forcing close", "error", err)
			_ = srv.Close()
		} else {
			log.Info("HTTP server stopped cleanly")
		}
		httpCancel()

		// Step 2: drain remaining audit_outbox rows. The drainer is
		// observing rootCtx (cancelled) — its Run loop will return
		// after the current iteration. We wait for it via errgroup.
		//
		// IMPORTANT: at this point no new outbox rows are being
		// produced (HTTP is stopped), so the drainer's next iteration
		// processes the final accumulated batch and exits. Worst-case
		// duration is one PollInterval + batch processing time.
		log.Info("step 2/3: waiting for audit drainer to finish")
		if err := g.Wait(); err != nil {
			log.Error("drainer exited with error", "error", err)
		} else {
			log.Info("audit drainer stopped cleanly")
		}

		// Step 3: close the DB pool. Nothing else holds it now.
		log.Info("step 3/3: closing DB pool")
		db.Close()
		log.Info("shutdown complete")
	}
}
}
