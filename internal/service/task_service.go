package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"gotask/internal/audit"
	"gotask/internal/authz"
	"gotask/internal/domain"
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

	// 5. Persist. Convert the service input into the repository input
	//    shape. The repository owns NULL/zero mapping; we just pass the
	//    domain values.
	created, err := s.tasks.Create(ctx, domain.NewTaskInput{
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

	// 6. Audit. Recorded AFTER the operation succeeds, never before —
	//    auditing a write that subsequently fails would leave the
	//    history claiming an event that didn't happen. The audit call
	//    failing does not fail the operation: see the helper.
	s.audit(ctx, audit.Event{
		Actor:   in.ReporterID,
		Action:  audit.ActionTaskCreated,
		Target:  audit.Target{Kind: "task", ID: created.ID},
		Outcome: audit.OutcomeSuccess,
		Detail: map[string]any{
			"project_id": created.ProjectID.String(),
			"title":      created.Title,
			"status":     string(created.Status),
		},
	})

	return created, nil
}

// audit emits an event through the configured auditor. A nil auditor
// (early-startup test scenarios) is a no-op rather than a panic. A
// Record error is logged via the audit recorder's own concerns; we do
// NOT let an audit-write failure fail the business operation, because
// the operation has already succeeded — declaring it failed because we
// couldn't log it would be a worse outcome than a brief gap in audit.
// Persistent audit failure is an operational alarm condition; the
// service is not the layer that surfaces that.
func (s *TaskService) audit(ctx context.Context, e audit.Event) {
	if s.auditor == nil {
		return
	}
	_ = s.auditor.Record(ctx, e)
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
		s.audit(ctx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskStatusChanged,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeDenied,
			Detail: map[string]any{
				"reason": "not owner and not manager",
				"to":     string(to),
			},
		})
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

	updated, err := s.tasks.Update(ctx, id, repository.UpdateTaskInput{
		Status: &to,
	})
	if err != nil {
		return domain.Task{}, mapRepoError(err)
	}

	s.audit(ctx, audit.Event{
		Actor:   authz.Subject(claims),
		Action:  audit.ActionTaskStatusChanged,
		Target:  audit.Target{Kind: "task", ID: id},
		Outcome: audit.OutcomeSuccess,
		Detail:  map[string]any{"from": from, "to": string(to)},
	})

	return updated, nil
}

// Delete removes a task or returns ErrNotFound. Authorization: only the
// task's reporter or a manager may delete. Denied attempts are audited.
func (s *TaskService) Delete(ctx context.Context, claims authz.Claims, id uuid.UUID) error {
	current, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if !authz.CanDeleteTask(claims, current) {
		s.audit(ctx, audit.Event{
			Actor:   authz.Subject(claims),
			Action:  audit.ActionTaskDeleted,
			Target:  audit.Target{Kind: "task", ID: id},
			Outcome: audit.OutcomeDenied,
			Detail:  map[string]any{"reason": "not owner and not manager"},
		})
		return ErrForbidden
	}

	if err := s.tasks.Delete(ctx, id); err != nil {
		return mapRepoError(err)
	}

	s.audit(ctx, audit.Event{
		Actor:   authz.Subject(claims),
		Action:  audit.ActionTaskDeleted,
		Target:  audit.Target{Kind: "task", ID: id},
		Outcome: audit.OutcomeSuccess,
		Detail: map[string]any{
			"project_id": current.ProjectID.String(),
			"title":      current.Title,
		},
	})
	return nil
}

// isRepoNotFound reports whether a repository error represents "no such
// row". The repository layer translates pgx.ErrNoRows into an
// apperror.NotFound; rather than import apperror here (the service must
// not know about HTTP), we match on the error string boundary. This is a
// deliberate, documented seam — see mapRepoError for the fuller note.
func isRepoNotFound(err error) bool {
	if err == nil {
		return false
	}
	// apperror.Error.Error() for a not-found is of the form
	// "not_found: <resource> not found". We avoid importing apperror by
	// checking the stable substring the repository guarantees.
	return strings.Contains(err.Error(), "not found")
}

// mapRepoError converts a repository error into the service error
// vocabulary so callers (and tests) can branch with errors.Is on
// service.ErrNotFound / ErrConflict without knowing anything about
// apperror or pgx.
//
// Why string-matching instead of importing apperror: the service must
// not depend on the HTTP error package (that would couple business logic
// to transport). The repository's apperror messages are a stable,
// documented contract for exactly this translation. If this coupling
// ever feels too loose, the cleaner fix is a small typed error in the
// repository package that the service imports — noted as a Phase 9
// hardening candidate, intentionally not done now to keep the layer
// boundary obvious.
func mapRepoError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isRepoNotFound(err):
		return ErrNotFound
	case strings.Contains(err.Error(), "already exists"),
		strings.Contains(err.Error(), "conflict"):
		return ErrConflict
	default:
		// Pass through unknown errors unwrapped. Context
		// cancellation/deadline must stay recognisable so the HTTP
		// timeout path still yields 504, not 500.
		return err
	}
}
