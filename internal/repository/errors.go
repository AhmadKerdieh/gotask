package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gotask/internal/apperror"
)

// Repository-layer sentinel errors. These are the typed surface the
// service uses with errors.Is(), replacing the Phase 4-8 string-matching
// in service.mapRepoError. The full chain a service sees is:
//
//     errors.Is(err, repository.ErrNotFound) → true
//     errors.As(err, &apperror.Error{...})   → true (still works)
//
// because translateError returns errors that wrap BOTH a repository
// sentinel AND an apperror underneath. The handler's apperror-aware
// respondError keeps working; the service gets clean typed checks.
//
// What goes here vs apperror: apperror is the HTTP-shaped error
// vocabulary (status codes, response envelopes). These sentinels are
// the BUSINESS-shaped outcomes ("the row wasn't there", "the unique
// constraint was violated"). The repository now speaks both languages
// fluently and the service only consumes the business one.
var (
	// ErrNotFound — the queried row does not exist.
	ErrNotFound = errors.New("repository: not found")

	// ErrConflict — a unique constraint or foreign-key constraint was
	// violated. The service surfaces this as a 409 Conflict to clients.
	ErrConflict = errors.New("repository: conflict")

	// ErrConstraintViolation — a CHECK or NOT NULL constraint was hit.
	// Typically indicates a defense-in-depth catch of something
	// validation should have rejected earlier. Surfaces as 400.
	ErrConstraintViolation = errors.New("repository: constraint violation")
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
		// Wrap both the sentinel AND the apperror so both `errors.Is`
		// paths work: the service uses repository.ErrNotFound; the
		// handler's respondError uses apperror.NotFound. Each layer
		// matches on its own vocabulary.
		return repoError{
			sentinel: ErrNotFound,
			cause:    apperror.NotFound(resource),
		}
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
			return repoError{
				sentinel: ErrConflict,
				cause:    apperror.Conflict("a record with the same unique value already exists"),
			}
		case pgForeignKeyViolation:
			return repoError{
				sentinel: ErrConflict,
				cause:    apperror.Conflict("the request references a record that does not exist or is still in use"),
			}
		case pgCheckViolation, pgNotNullViolation:
			return repoError{
				sentinel: ErrConstraintViolation,
				cause:    apperror.BadRequest("the request violates a database constraint"),
			}
		}
	}

	// Unclassified: a genuine internal error. apperror.Internal preserves
	// the cause for server-side logging while presenting a generic
	// message to the client.
	return apperror.Internal(err)
}

// repoError carries both a repository sentinel (for the service's
// errors.Is checks) and an apperror (for the handler's respondError).
// Its Error() formats the sentinel + cause for legible logs; Unwrap
// returns the cause so errors.As(apperror.Error) finds the apperror.
// Is matches the sentinel so errors.Is(repository.ErrX) returns true.
//
// This dual-tagging is the cleanest replacement for the Phase 4-8
// string-matching: each layer matches on its own vocabulary, no layer
// has to know the other's vocabulary, and the underlying cause is
// preserved for diagnostics.
type repoError struct {
	sentinel error
	cause    error
}

func (e repoError) Error() string {
	if e.cause == nil {
		return e.sentinel.Error()
	}
	return fmt.Sprintf("%s: %s", e.sentinel.Error(), e.cause.Error())
}

// Is matches the sentinel — repository.ErrNotFound etc. The service's
// `errors.Is(err, repository.ErrNotFound)` returns true via this.
func (e repoError) Is(target error) bool {
	return e.sentinel == target
}

// Unwrap returns the apperror cause so the handler's `errors.As(err,
// &apperror.Error{...})` still finds it. This is what keeps the
// apperror-based HTTP mapping working without changes.
func (e repoError) Unwrap() error {
	return e.cause
}
