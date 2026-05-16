// Package database constructs and owns the Postgres connection pool.
//
// The rest of the application never imports pgx directly through this
// package's surface — repositories receive a *pgxpool.Pool and use it,
// but the *construction* and *lifecycle* of that pool (timeouts, health
// check, graceful close) live here in one place.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config carries the pool tuning knobs. These are deliberately explicit
// rather than relying on pgx defaults: the defaults are reasonable but
// invisible, and invisible configuration is the kind you discover during
// an incident. Every value here is something an operator might need to
// reason about.
type Config struct {
	// URL is the libpq-style connection string, e.g.
	//   postgres://user:pass@host:5432/dbname?sslmode=disable
	URL string

	// MaxConns caps the pool size. Too low and requests queue behind a
	// starved pool; too high and you exhaust Postgres's own
	// max_connections (default 100, shared across every client). A
	// service should own a known slice of that budget.
	MaxConns int32

	// MinConns keeps a few connections warm so the first request after an
	// idle period doesn't pay full connection-establishment latency.
	MinConns int32

	// MaxConnLifetime forces connections to be recycled periodically.
	// This bounds the blast radius of a connection that has drifted into
	// a bad state and plays nicely with rolling Postgres restarts.
	MaxConnLifetime time.Duration

	// ConnectTimeout bounds how long NewPool will wait for the initial
	// connectivity check before giving up. Startup should fail fast and
	// loud if the database is unreachable, not hang indefinitely.
	ConnectTimeout time.Duration
}

// DB wraps the pool. It is a thin type rather than a bare *pgxpool.Pool so
// that (a) callers depend on our type, giving us a place to add
// instrumentation later without touching every call site, and (b) Close
// has an obvious home.
type DB struct {
	Pool *pgxpool.Pool
}

// NewPool builds the pool, applies the configuration, and verifies
// connectivity with a Ping before returning. A returned error means the
// application must not start — there is no point serving traffic with no
// database.
func NewPool(ctx context.Context, cfg Config) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// Parse errors are almost always a malformed DATABASE_URL — a
		// configuration mistake, surfaced at startup where it is cheap
		// to fix.
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime

	// pgxpool.NewWithConfig establishes the minimum connections lazily;
	// it does NOT itself prove the database is reachable. That is what
	// the explicit Ping below is for.
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Bound the connectivity check. Without this, an unreachable host
	// makes startup hang until the OS TCP timeout (can be minutes).
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		// Clean up the pool we just created before returning — leaking
		// it would hold descriptors open in the failure path.
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close drains and closes the pool. It blocks until all connections are
// returned and shut down, which is what we want during graceful shutdown:
// in-flight queries finish, then the process exits. Safe to call once.
func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
}

// Health runs a lightweight round-trip used by the readiness probe. It is
// deliberately a separate method from the startup Ping: startup failure is
// fatal, whereas a runtime health check is informational and must respect
// the caller's context deadline.
func (db *DB) Health(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}
