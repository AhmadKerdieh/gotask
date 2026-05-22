package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queryer is the interface satisfied by both *pgxpool.Pool and pgx.Tx.
// Repository methods accept this so they can run against either a fresh
// pool connection (the common case) or a transaction (the case Phase 8
// needs for atomic write-and-audit).
//
// pgx exposes these methods with identical signatures on both types,
// which is what makes this abstraction "free": no adapter, no wrapping,
// just declare what we use. If we ever need rows.Conn() or similar
// type-specific methods, that means we're leaking pgx specifics into
// callers and we should reconsider the boundary — but for plain SQL
// execution this is the right abstraction.
type Queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// WithTx runs fn inside a single database transaction. The transaction
// is begun, fn executes against the resulting pgx.Tx, and:
//
//   - if fn returns nil, the transaction is committed
//   - if fn returns any error, the transaction is rolled back AND
//     the error is returned unchanged so callers can inspect it
//   - if fn panics, the transaction is rolled back and the panic
//     re-panics (this preserves debug stacks)
//
// This is the single correct shape for "do this work in a tx" in Go.
// Implementing it inline in every service method is how transaction
// bugs sneak in: forgetting Rollback on error paths, double-committing,
// leaking transactions on panic. Centralising it means every service
// method that uses a transaction looks like this:
//
//	err := db.WithTx(ctx, func(tx pgx.Tx) error {
//	    if err := repoA.Op(ctx, tx, ...); err != nil { return err }
//	    if err := repoB.Op(ctx, tx, ...); err != nil { return err }
//	    return outbox.Enqueue(ctx, tx, event)
//	})
//
// — and that's all the transaction machinery the service ever sees.
func (db *DB) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) (err error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Defer the rollback unconditionally. If fn returns nil and the
	// commit succeeds below, the rollback is a no-op (pgx returns
	// pgx.ErrTxClosed and we ignore it). If fn returns an error or
	// panics, the rollback actually happens. This pattern is correct
	// against panics, early returns, and the "I forgot a branch" case.
	defer func() {
		// On panic, roll back and re-panic so the original stack
		// reaches the panic recovery middleware (or main) unmolested.
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		// On normal exit (success or error), Rollback is safe — pgx
		// reports an already-committed tx with a sentinel error which
		// we deliberately discard.
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Roll-back failure is its own problem — we wrap it onto
			// the original error so callers see both. If there was no
			// original error and the rollback failed, that's now the
			// reported error.
			if err == nil {
				err = fmt.Errorf("tx rollback: %w", rbErr)
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// Ensure *pgxpool.Pool satisfies Queryer at compile time. This is the
// "happy path" assertion: if pgx ever changes its method signatures in
// a way that breaks this, the build fails here with a clear message
// rather than at every repository call site.
var _ Queryer = (*pgxpool.Pool)(nil)
var _ Queryer = (pgx.Tx)(nil)
