package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gotask/internal/apperror"
)

// Postgres SQLSTATE codes we care about. The full list is in the Postgres
// documentation under "Appendix A. Error Codes"; these are the handful
// that map to client-meaningful outcomes.
const (
	pgUniqueViolation     = "23505" // duplicate key
	pgForeignKeyViolation = "23503" // referenced row missing / still referenced
	pgCheckViolation      = "23514" // CHECK constraint failed
	pgNotNullViolation    = "23502" // NOT NULL constraint failed
)

// translateError is the SINGLE point in the repository layer that converts
// a database error into an apperror. Same discipline as the handler's
// respondError from Phase 1: one function owns the mapping, so changing
// how a class of error is reported is a one-line change, not a sweep.
//
// The contract:
//
//   - pgx.ErrNoRows               → apperror.NotFound (caller passes the
//                                    resource name so the message is useful)
//   - 23505 unique violation      → apperror.Conflict
//   - 23503 foreign key violation → apperror.Conflict (the referenced
//                                    row is missing, or this row is still
//                                    referenced — either way the request
//                                    conflicts with current state)
//   - 23514 / 23502               → apperror.BadRequest (the payload
//                                    violates a structural constraint;
//                                    this generally means validation
//                                    should have caught it earlier, but
//                                    defence in depth is correct here)
//   - context cancelled/deadline  → passed through UNwrapped so the
//                                    caller / middleware can recognise it
//                                    as a timeout, not a 500
//   - anything else               → apperror.Internal (real error logged
//                                    upstream, generic message to client)
//
// `resource` is the human name of the thing being looked up ("task",
// "project") used only for the NotFound message.
func translateError(err error, resource string) error {
	if err == nil {
		return nil
	}

	// No rows is the canonical "not found". pgx returns a sentinel we can
	// match with errors.Is.
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.NotFound(resource)
	}

	// Context errors must propagate unwrapped. The timeout middleware and
	// the handler's respondError already know how to turn a
	// context.DeadlineExceeded into a 504; if we wrapped it in
	// apperror.Internal here it would surface as a misleading 500.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	// Postgres server-side errors carry a SQLSTATE we can branch on.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return apperror.Conflict("a record with the same unique value already exists")
		case pgForeignKeyViolation:
			return apperror.Conflict("the request references a record that does not exist or is still in use")
		case pgCheckViolation, pgNotNullViolation:
			return apperror.BadRequest("the request violates a database constraint")
		}
	}

	// Unclassified: a genuine internal error. apperror.Internal preserves
	// the cause for server-side logging while presenting a generic
	// message to the client.
	return apperror.Internal(err)
}
