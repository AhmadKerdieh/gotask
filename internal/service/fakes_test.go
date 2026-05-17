package service

import (
	"context"
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
		// Return an error whose message contains "not found" so the
		// service's mapRepoError translates it to ErrNotFound, exactly
		// as the real repository's apperror would.
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
		// as a conflict. The message contains "already exists" so
		// mapRepoError translates it to service.ErrConflict.
		return domain.Project{}, repoConflict("project key already exists")
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

// repoNotFound / repoConflict produce errors whose messages match the
// stable substrings the real repository guarantees ("not found",
// "already exists"), so the service's mapRepoError behaves identically
// against fakes and against Postgres. This is what keeps the unit tests
// honest: they exercise the same translation logic production uses.
type stringErr string

func (e stringErr) Error() string { return string(e) }

func repoNotFound(resource string) error { return stringErr(resource + " not found") }
func repoConflict(msg string) error      { return stringErr(msg + " (already exists)") }
