package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gotask/internal/audit"
	"gotask/internal/authz"
	"gotask/internal/database"
	"gotask/internal/domain"
	"gotask/internal/reqctx"
	"gotask/internal/repository"
)

// CreateTaskInput is the service-level input for creating a task. It is
// distinct from domain.NewTaskInput (persistence shape) and from any
// HTTP DTO (wire shape). The service accepts this, applies business
// rules, and converts to the repository input itself.
//
// Status and Priority are optional: empty means "apply the workflow
// default". This is a business rule, so it lives here — not in the
// handler, not in the repository.
type CreateTaskInput struct {
	ProjectID   uuid.UUID
	Title       string
	Description string
	Status      domain.Status
	Priority    domain.Priority
	AssigneeID  uuid.UUID // uuid.Nil = unassigned
	ReporterID  uuid.UUID
}

// Create validates the input against the workflow, applies defaults, and
// persists. The order matters: structural validation first (cheap, no
// I/O), then existence checks (one query), then the write.
func (s *TaskService) Create(ctx context.Context, in CreateTaskInput) (domain.Task, error) {
	// 1. Structural validation. Accumulate every field error so the
	//    caller gets them all at once, not one per round-trip.
	ve := newValidationError()

	title := strings.TrimSpace(in.Title)
	if title == "" {
		ve.add("title", "required")
	} else if len(title) > 200 {
		ve.add("title", "must be at most 200 characters")
	}
	if in.ProjectID == uuid.Nil {
		ve.add("project_id", "required")
	}
	if in.ReporterID == uuid.Nil {
		ve.add("reporter_id", "required")
	}

	// 2. Apply defaults BEFORE legality checks, so an empty status is
	//    filled from the workflow and then validated like any other.
	status := in.Status
	if status == "" {
		status = domain.Status(s.workflow.DefaultStatus)
	}
	priority := in.Priority
	if priority == "" {
		priority = domain.Priority(s.workflow.DefaultPriority)
	}

	// 3. Value legality — this is the gate that closes the "banana"
	//    hole from the Phase 3 review. Nothing below the service
	//    enforces this; the database column is plain TEXT.
	if !s.workflow.IsStatus(string(status)) {
		ve.add("status", "must be a known status (see workflow.yaml)")
	}
	if !s.workflow.IsPriority(string(priority)) {
		ve.add("priority", "must be a known priority (see workflow.yaml)")
	}

	if err := ve.orNil(); err != nil {
		return domain.Task{}, err
	}

	// 4. Existence check: the project must exist. We surface a clean
	//    service error rather than relying on the database foreign-key
	//    violation, so the caller gets a precise message instead of a
	//    generic conflict.
	if _, err := s.projects.GetByID(ctx, in.ProjectID); err != nil {
		if isRepoNotFound(err) {
			return domain.Task{}, &ValidationError{Fields: map[string]string{
				"project_id": "project does not exist",
			}}
		}
		return domain.Task{}, err
	}

	// 5. Persist + audit, atomically. This is the Phase 8 change:
	//    instead of a plain repo call, we open a transaction, do the
	//    task INSERT against the tx, then write the audit_outbox row
	//    against the SAME tx. Either both commits land or neither
	//    does — the row a user sees in /tasks/{id} is guaranteed to
	//    have a corresponding audit row.
	//
	//    Path A's durability promise lives entirely in this WithTx
	//    closure (and the equivalent ones in UpdateStatus, Delete, and
	//    project Create). Outside this closure there is no atomicity
	//    machinery; inside, there's no way to lose half of it.
	//
	//    The db nil-check is for tests that use fakes and don't supply
	//    a DB. The fake repos and FakeAuditor ignore the runner anyway,
	//    so we fall through to plain calls.
	var created domain.Task
	if s.db == nil {
		c, err := s.tasks.Create(ctx, domain.NewTaskInput{
			ProjectID:   in.ProjectID,
			Title:       title,
			Description: in.Description,
			Status:      status,
			Priority:    priority,
			AssigneeID:  in.AssigneeID,
			ReporterID:  in.ReporterID,
		})
		if err != nil {
			return domain.Task{}, mapRepoError(err)
		}
		if err := s.audit(ctx, nil, audit.Event{
			Actor:   in.ReporterID,
			Action:  audit.ActionTaskCreated,
			Target:  audit.Target{Kind: "task", ID: c.ID},
			Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{
				"project_id": c.ProjectID.String(),
				"title":      c.Title,
				"status":     string(c.Status),
			},
		}); err != nil {
			return domain.Task{}, err
		}
		return c, nil
	}

	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		// Construct a tx-scoped task repo. The repository interface is
		// unchanged; only its backing Queryer differs. This is why the
		// Phase 8 refactor was cheap: we didn't have to thread tx
		// through every method signature.
		txTasks := repository.NewTaskRepositoryTx(tx)

		c, err := txTasks.Create(ctx, domain.NewTaskInput{
			ProjectID:   in.ProjectID,
			Title:       title,
			Description: in.Description,
			Status:      status,
			Priority:    priority,
			AssigneeID:  in.AssigneeID,
			ReporterID:  in.ReporterID,
		})
		if err != nil {
			return mapRepoError(err)
		}

		// 6. Audit inside the same tx. If this fails, the WithTx helper
		//    rolls back the transaction — the task is NOT persisted.
		//    This is Path A's atomicity promise enforced at the lowest
		//    practical layer.
		if err := s.audit(ctx, tx, audit.Event{
			Actor:   in.ReporterID,
			Action:  audit.ActionTaskCreated,
			Target:  audit.Target{Kind: "task", ID: c.ID},
			Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{
				"project_id": c.ProjectID.String(),
				"title":      c.Title,
				"status":     string(c.Status),
			},
		}); err != nil {
			return err
		}

		created = c
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return created, nil
}

// audit records an event through the configured Recorder. Phase 8
// semantics changed substantially from Phase 7:
//
//   - The runner parameter (a Queryer, typically a pgx.Tx) carries the
//     event into the audit_outbox table in the SAME transaction as the
//     business write. The atomic property the whole outbox pattern is
//     built on.
//
//   - If Record returns an error, we PROPAGATE it. The caller is
//     inside a WithTx closure; returning the error causes the tx to
//     roll back, undoing the business write AND the outbox insert
//     together. This is the whole point of Path A: durability over
//     latency. A task is deleted ⇔ an audit row exists.
//
//   - A nil auditor (test scenarios where the test doesn't care about
//     audit) is a no-op, NOT an error. This preserves the Phase 4
//     fakes-first testing model.
//
// This is the opposite Phase 7 of the swallow-errors choice. The
// architectural choice from Path A makes audit a hard dependency of the
// business operation; the helper enforces that by routing failures up,
// not absorbing them.
func (s *TaskService) audit(ctx context.Context, runner database.Queryer, e audit.Event) error {
	if s.auditor == nil {
		return nil
	}
	// Phase 9: stamp request_id from context onto the event. The audit
	// row stores it; the drainer's logs include it; you can grep an
	// incident's request_id and see both the HTTP log line AND the
	// drain log line for the same operation. This is the trace
	// continuity across the goroutine boundary that distinguishes
	// "audit recorded" from "audit you can correlate to its cause".
	if e.RequestID == "" {
		e.RequestID = reqctx.RequestIDFromContext(ctx)
	}
	return s.auditor.Record(ctx, runner, e)
}

// Get returns a single task or ErrNotFound.
func (s *TaskService) Get(ctx context.Context, id uuid.UUID) (domain.Task, error) {
	t, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return domain.Task{}, mapRepoError(err)
	}
	return t, nil
}

// List returns tasks matching the filter. Listing has no business rules
// of its own yet, so this is a thin pass-through; it exists on the
// service anyway so handlers never reach past the service to a repo.
func (s *TaskService) List(ctx context.Context, f repository.TaskFilter) ([]domain.Task, error) {
	tasks, err := s.tasks.List(ctx, f)
	if err != nil {
		return nil, mapRepoError(err)
	}
	return tasks, nil
}

// UpdateStatus is the canonical business-rule method of the whole course:
// it enforces that a status change is permitted by workflow.yaml. This is
// the rule that is neither HTTP nor SQL — it could only ever live here.
//
// The sequence:
//  1. load the current task (need its present status)
//  2. validate the requested status is even a known value
//  3. ask the workflow whether old -> new is a legal transition
//     (this also rejects moves out of terminal statuses and no-op
//      self-transitions, by the workflow's own rules)
//  4. only then write
func (s *TaskService) UpdateStatus(ctx context.Context, claims authz.Claims, id uuid.UUID, to domain.Status) (domain.Task, error) {
	current, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return domain.Task{}, mapRepoError(err)
	}

	// Authorization is fine-grained (depends on the loaded resource), so
	// it lives here, not in middleware. Audit the denial too — knowing
	// someone TRIED to change a task they shouldn't is exactly what
	// security review wants.
	if !authz.CanChangeTaskStatus(claims, current) {
		// Audit the denial. With Phase 8 semantics this also runs in
		// its own tx — the denial audit row is its own atomic unit
		// (there's no business mutation to roll back along with it,
		// but the outbox insert still belongs in a transaction so it
		// behaves uniformly with the success path).
		if auditErr := s.recordOutboxOnly(ctx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskStatusChanged,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeDenied,
			Detail: map[string]any{
				"reason": "not owner and not manager",
				"to":     string(to),
			},
		}); auditErr != nil {
			// Even denied-audit failures must surface. Auditing is
			// non-optional under Path A. A 5xx is the right answer:
			// "we couldn't honestly record what just happened, so
			// we cannot honestly return the result either."
			return domain.Task{}, auditErr
		}
		return domain.Task{}, ErrForbidden
	}

	if !s.workflow.IsStatus(string(to)) {
		return domain.Task{}, &ValidationError{Fields: map[string]string{
			"status": "must be a known status (see workflow.yaml)",
		}}
	}

	from := string(current.Status)
	if !s.workflow.CanTransition(from, string(to)) {
		// TransitionError satisfies errors.Is(err, ErrInvalidTransition)
		// while carrying the specific from/to for a useful message.
		return domain.Task{}, &TransitionError{From: from, To: string(to)}
	}

	// Mutation + audit in one tx. Same shape as Create.
	var updated domain.Task
	if s.db == nil {
		u, err := s.tasks.Update(ctx, id, repository.UpdateTaskInput{Status: &to})
		if err != nil {
			return domain.Task{}, mapRepoError(err)
		}
		if err := s.audit(ctx, nil, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskStatusChanged,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeSuccess,
			Detail:  map[string]any{"from": from, "to": string(to)},
		}); err != nil {
			return domain.Task{}, err
		}
		return u, nil
	}

	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		txTasks := repository.NewTaskRepositoryTx(tx)
		u, err := txTasks.Update(ctx, id, repository.UpdateTaskInput{Status: &to})
		if err != nil {
			return mapRepoError(err)
		}
		if err := s.audit(ctx, tx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskStatusChanged,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeSuccess,
			Detail:  map[string]any{"from": from, "to": string(to)},
		}); err != nil {
			return err
		}
		updated = u
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return updated, nil
}

// recordOutboxOnly writes a single audit event in its own transaction.
// Used for the denial path, where there's no business mutation to
// atomic-bundle with. We still wrap it in a tx for uniform error-
// handling (the WithTx helper owns the rollback discipline; calling
// Record directly against the pool would leave us to reimplement it).
func (s *TaskService) recordOutboxOnly(ctx context.Context, e audit.Event) error {
	if s.auditor == nil {
		return nil
	}
	if e.RequestID == "" {
		e.RequestID = reqctx.RequestIDFromContext(ctx)
	}
	if s.db == nil {
		return s.auditor.Record(ctx, nil, e)
	}
	return s.db.WithTx(ctx, func(tx pgx.Tx) error {
		return s.auditor.Record(ctx, tx, e)
	})
}

// Delete removes a task or returns ErrNotFound. Authorization: only the
// task's reporter or a manager may delete. Denied attempts are audited.
func (s *TaskService) Delete(ctx context.Context, claims authz.Claims, id uuid.UUID) error {
	current, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if !authz.CanDeleteTask(claims, current) {
		if auditErr := s.recordOutboxOnly(ctx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskDeleted,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeDenied,
			Detail:  map[string]any{"reason": "not owner and not manager"},
		}); auditErr != nil {
			return auditErr
		}
		return ErrForbidden
	}

	// Delete + audit, atomic. Same shape as the other mutators.
	if s.db == nil {
		if err := s.tasks.Delete(ctx, id); err != nil {
			return mapRepoError(err)
		}
		return s.audit(ctx, nil, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskDeleted,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{
				"project_id": current.ProjectID.String(),
				"title":      current.Title,
			},
		})
	}

	return s.db.WithTx(ctx, func(tx pgx.Tx) error {
		txTasks := repository.NewTaskRepositoryTx(tx)
		if err := txTasks.Delete(ctx, id); err != nil {
			return mapRepoError(err)
		}
		return s.audit(ctx, tx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskDeleted,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{
				"project_id": current.ProjectID.String(),
				"title":      current.Title,
			},
		})
	})
}

// isRepoNotFound reports whether a repository error represents "no such
// row". Phase 9 replaced Phase 4-8's string matching with a typed
// errors.Is check — the repository now exports repository.ErrNotFound
// as a sentinel, and translateError wraps the apperror in a repoError
// that satisfies both errors.Is(repository.ErrNotFound) AND
// errors.As(&apperror.Error{}). Each layer matches on its own
// vocabulary; this layer's is repository.ErrNotFound.
func isRepoNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound)
}

// mapRepoError converts a repository error into the service error
// vocabulary so callers (and tests) can branch with errors.Is on
// service.ErrNotFound / ErrConflict.
//
// Phase 9 cleanup: this used to do string matching on apperror
// messages, with a comment apologising for the coupling and a note
// flagging it as a Phase 9 candidate. That fix landed: the repository
// now exports typed sentinels, this function uses errors.Is, and the
// service no longer needs to know anything about the apperror format.
// The "intentionally not done now" backlog item from Phase 4 is closed.
func mapRepoError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, repository.ErrConflict):
		return ErrConflict
	default:
		// Pass through unknown errors unwrapped. Context
		// cancellation/deadline must stay recognisable so the HTTP
		// timeout path still yields 504, not 500. Constraint
		// violations (repository.ErrConstraintViolation) also pass
		// through unwrapped — the repoError's Unwrap returns the
		// apperror.BadRequest cause, which the handler maps to 400.
		return err
	}
}
