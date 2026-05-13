// Package database constructs and manages the Postgres connection pool.
// Implementation lands in Phase 3 (Postgres with pgx, Migrations, Repositories):
// NewPool will build a *pgxpool.Pool with sane timeouts, run Ping on startup,
// and surface a Close method for graceful shutdown.
package database
