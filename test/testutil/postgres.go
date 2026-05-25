// Package testutil provides shared test infrastructure for the
// integration tests. The headline function is StartPostgres, which
// returns a connection pool backed by a real Postgres container with
// the application's migrations already applied.
//
// ── Scope (Phase 10 decision: per-suite) ────────────────────────────
//
// One container starts when the first integration test in a package
// runs, lives for the duration of `go test`, and is torn down by
// TestMain. Subsequent tests share the container. This is the fastest
// scope choice (one ~3s startup amortised across many tests) and the
// one that demands the most test-side discipline:
//
//   - Each test MUST call Truncate(t) in t.Cleanup BEFORE doing any
//     writes. Cleanup runs in LIFO order and is guaranteed to fire
//     even on panic/fail, so the next test always starts empty.
//
//   - Tests MUST NOT use t.Parallel(). The shared pool means parallel
//     tests would race; the per-suite scope is incompatible with
//     parallelism. The trade-off is deliberate.
//
//   - Schema state lives across tests (created once via migrations);
//     row state never does (truncated by every Cleanup). This split is
//     what makes the per-suite scope safe.
//
// The alternative scopes — per-test (new container each test) or
// per-package (one per package) — are also valid choices. Per-suite
// won the design discussion because the tests are short and the
// container startup dominates everything else; eliminating N-1
// startups is the largest available win.
//
// ── How TestMain wires this up ──────────────────────────────────────
//
// Each integration test file's package declares a TestMain that calls
// StartPostgres once, stores the *Pool in a package var, runs m.Run,
// and Terminates the container. Individual tests obtain the pool via
// a package helper (SharedPool) and call Truncate(t) in cleanup.
//
// ── Why this isn't just init() ───────────────────────────────────────
//
// We CANNOT use init() because container startup involves blocking
// network calls (waiting for Postgres to be ready) that can fail in
// CI for transient reasons. init() failures are unrecoverable; TestMain
// can log a useful error and exit with a non-zero code that CI sees.
package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresFixture is the container + connection pool the integration
// tests share. Construct via StartPostgres; tear down via Terminate.
type PostgresFixture struct {
	Container *postgres.PostgresContainer
	Pool      *pgxpool.Pool
	DSN       string
}

// StartPostgres boots a Postgres container, applies every migration in
// the project's migrations directory, opens a connection pool, and
// returns a fixture the tests share.
//
// The function blocks until Postgres is healthy AND migrations have
// completed; on return, callers can immediately begin queries.
//
// On any failure, the function logs the cause and calls t.Fatalf —
// which in TestMain context (where t is a *testing.T provided by a
// helper, not the test runner) is normally not appropriate. Instead
// we return an error for TestMain to log and exit with non-zero.
func StartPostgres(ctx context.Context) (*PostgresFixture, error) {
	// Use the official postgres image at a pinned version. Pinning is
	// the same discipline as in production: a CI run today should
	// produce the same result as a CI run six months from now, and
	// "latest" violates that.
	container, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		postgres.WithDatabase("gotask_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		// Postgres prints "ready to accept connections" twice during
		// startup (once during init, once for real). The default
		// strategy waits for the SECOND occurrence, which is what
		// we want — connecting too early would race against the
		// database actually being initialised.
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("read DSN: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("open pool: %w", err)
	}

	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	return &PostgresFixture{
		Container: container,
		Pool:      pool,
		DSN:       dsn,
	}, nil
}

// Terminate closes the pool and stops the container. Safe to call
// from a deferred TestMain block; never panics.
func (f *PostgresFixture) Terminate(ctx context.Context) {
	if f == nil {
		return
	}
	if f.Pool != nil {
		f.Pool.Close()
	}
	if f.Container != nil {
		_ = f.Container.Terminate(ctx)
	}
}

// Truncate clears every table that the application writes to,
// restoring the database to "schema present, no rows" state. Call
// this from t.Cleanup in every integration test that does writes,
// so the NEXT test starts empty.
//
// We use TRUNCATE ... RESTART IDENTITY CASCADE for three reasons:
//   - TRUNCATE is dramatically faster than DELETE on non-trivial
//     tables; we want cleanup to take microseconds, not milliseconds
//     adding up across the suite.
//   - RESTART IDENTITY resets any sequences, so auto-increment IDs
//     start fresh — important when tests assert on specific IDs.
//   - CASCADE handles foreign-key dependencies in one call instead
//     of forcing us to maintain a delete order.
//
// The list of tables is the only thing that needs maintenance: when
// the schema grows a new table, add it here.
func (f *PostgresFixture) Truncate(t *testing.T) {
	t.Helper()
	const q = `TRUNCATE TABLE
		audit_log,
		audit_outbox,
		tasks,
		projects
		RESTART IDENTITY CASCADE`
	if _, err := f.Pool.Exec(context.Background(), q); err != nil {
		t.Fatalf("testutil: truncate failed: %v", err)
	}
}

// applyMigrations reads every .up.sql file in the project's
// migrations directory and runs them in lexical order. This mirrors
// what golang-migrate would do in production; we don't pull in the
// migrate library here because the test-side requirement is so
// narrow (just "apply the up migrations once, in order, against a
// fresh DB") that a 30-line implementation is clearer than the
// abstractions.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migDir, err := findMigrationsDir()
	if err != nil {
		return err
	}

	entries, err := filepath.Glob(filepath.Join(migDir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	// filepath.Glob returns files in lexical order, which for our
	// `NNNNNN_name.up.sql` naming is also the chronological order
	// we want. If we ever switched to non-numeric prefixes this
	// assumption would break loudly (a Phase 11 problem, not now).

	for _, path := range entries {
		sql, err := readFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// findMigrationsDir walks upward from the current source file's
// directory looking for `migrations/`. This lets the helper work no
// matter which package's test invokes it — internal/repository/
// tests, internal/audit/ tests, etc. all find the same migrations.
func findMigrationsDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "migrations")
		if isDir(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("migrations directory not found above %s", thisFile)
}
