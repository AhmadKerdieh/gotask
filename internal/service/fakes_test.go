package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"gotask/internal/domain"
	"gotask/internal/repository"
)

// This file is *_test.go, so it compiles only under `go test`. It
// contains hand-written fakes: real, working in-memory implementations
// of the Phase 3 repository interfaces, backed by a map instead of
// Postgres.
//
// A "fake" is not a "mock". A fake genuinely works — Create stores,
// GetByID retrieves, Delete removes. Tests use it exactly as they would
// use the real Postgres repository, minus the database. Building this by
// hand once makes generated mocks (shown in task_service_mock_test.go)
// unmysterious: a generated mock is this same shape with call-recording
// bolted on.
//
// The compile-time assertions below are the real proof the fakes satisfy
// the interfaces — if a method signature drifts, the build fails here,
// loudly, before any test runs.
var (
	_ repository.TaskRepository    = (*fakeTaskRepo)(nil)
	_ repository.ProjectRepository = (*fakeProjectRepo)(nil)
)

// fakeTaskRepo is an in-memory TaskRepository. The mutex makes it safe
// for the parallel tests we may add later; it costs nothing in serial
// tests and prevents a class of future flakiness.
type fakeTaskRepo struct {
	mu    sync.Mutex
	items map[uuid.UUID]domain.Task

	// failCreate, when non-nil, is returned by Create instead of
	// performing the insert. This lets a test exercise the service's
	// error-handling path without a real database failure.
	failCreate error
}

func newFakeTaskRepo() *fakeTaskRepo {
	return &fakeTaskRepo{items: make(map[uuid.UUID]domain.Task)}
}

func (f *fakeTaskRepo) Create(ctx context.Context, in domain.NewTaskInput) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate != nil {
		return domain.Task{}, f.failCreate
	}
	t := domain.Task{
		ID:          uuid.New(), // the fake mints the id, mirroring DB-owned identity
		ProjectID:   in.ProjectID,
		Title:       in.Title,
		Description: in.Description,
		Status:      in.Status,
		Priority:    in.Priority,
		AssigneeID:  in.AssigneeID,
		ReporterID:  in.ReporterID,
		DueDate:     in.DueDate,
	}
	f.items[t.ID] = t
	return t, nil
}

func (f *fakeTaskRepo) GetByID(ctx context.Context, id uuid.UUID) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.items[id]
	if !ok {
		// Return repoNotFound, which wraps repository.ErrNotFound —
		// matched by the service's mapRepoError via errors.Is, exactly
		// as the real repository's repoError dual-tagging does.
		return domain.Task{}, repoNotFound("task")
	}
	return t, nil
}

func (f *fakeTaskRepo) List(ctx context.Context, q repository.TaskFilter) ([]domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Task, 0, len(f.items))
	for _, t := range f.items {
		if q.ProjectID != uuid.Nil && t.ProjectID != q.ProjectID {
			continue
		}
		if q.Status != "" && t.Status != q.Status {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTaskRepo) Update(ctx context.Context, id uuid.UUID, in repository.UpdateTaskInput) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.items[id]
	if !ok {
		return domain.Task{}, repoNotFound("task")
	}
	if in.Status != nil {
		t.Status = *in.Status
	}
	if in.Title != nil {
		t.Title = *in.Title
	}
	if in.Priority != nil {
		t.Priority = *in.Priority
	}
	f.items[id] = t
	return t, nil
}

func (f *fakeTaskRepo) Delete(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return repoNotFound("task")
	}
	delete(f.items, id)
	return nil
}

// fakeProjectRepo is an in-memory ProjectRepository.
type fakeProjectRepo struct {
	mu    sync.Mutex
	items map[uuid.UUID]domain.Project
	keys  map[string]struct{} // enforces the unique-key constraint
}

func newFakeProjectRepo() *fakeProjectRepo {
	return &fakeProjectRepo{
		items: make(map[uuid.UUID]domain.Project),
		keys:  make(map[string]struct{}),
	}
}

func (f *fakeProjectRepo) Create(ctx context.Context, in domain.NewProjectInput) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.keys[in.Key]; dup {
		// Mirror the real repository: a duplicate unique key surfaces
		// as a conflict. repoConflict wraps repository.ErrConflict —
		// matched by mapRepoError via errors.Is.
		return domain.Project{}, repoConflict("project key")
	}
	p := domain.Project{
		ID:          uuid.New(),
		Key:         in.Key,
		Name:        in.Name,
		Description: in.Description,
		OwnerID:     in.OwnerID,
	}
	f.items[p.ID] = p
	f.keys[in.Key] = struct{}{}
	return p, nil
}

func (f *fakeProjectRepo) GetByID(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.items[id]
	if !ok {
		return domain.Project{}, repoNotFound("project")
	}
	return p, nil
}

func (f *fakeProjectRepo) List(ctx context.Context) ([]domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Project, 0, len(f.items))
	for _, p := range f.items {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProjectRepo) Delete(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return repoNotFound("project")
	}
	delete(f.items, id)
	return nil
}

// repoNotFound / repoConflict produce errors that wrap the repository
// layer's TYPED sentinels (Phase 9). They satisfy errors.Is(err,
// repository.ErrNotFound) and errors.Is(err, repository.ErrConflict)
// respectively — the same contract the real PostgresAuditor's
// translateError produces via repoError dual-tagging.
//
// Before Phase 9 these returned strings whose messages contained "not
// found" / "already exists", which the service's mapRepoError matched
// with strings.Contains. When mapRepoError moved to errors.Is, this
// file moved with it — because the test fakes implement the same
// contract as the real repository, and that contract is now typed.
//
// The fmt.Errorf %w wrap is what makes errors.Is walk the chain and
// find the sentinel; equivalent to repoError.Is() in the real impl,
// just expressed via stdlib %w rather than a custom Is method.
func repoNotFound(resource string) error {
	return fmt.Errorf("%s not found: %w", resource, repository.ErrNotFound)
}

func repoConflict(msg string) error {
	return fmt.Errorf("%s already exists: %w", msg, repository.ErrConflict)
}
