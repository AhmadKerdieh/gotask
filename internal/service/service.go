package service

import (
	"gotask/internal/audit"
	"gotask/internal/config"
	"gotask/internal/database"
	"gotask/internal/repository"
)

// Deps is the constructor input for the service layer.
//
// Phase 8 added DB so mutating service methods can begin transactions
// via db.WithTx — required by the outbox pattern, where the business
// write and the audit_outbox insert must be atomic.
//
// Note that we deliberately do NOT pass a pgxpool.Pool here: services
// know about *database.DB (our internal wrapper) but never directly
// about pgx. The WithTx helper hides pgx behind the database package's
// boundary. If we ever swap the connection library, the change is
// scoped to internal/database/ and internal/repository/.
type Deps struct {
	Tasks    repository.TaskRepository
	Projects repository.ProjectRepository
	Workflow *config.Workflow

	// DB is needed for transactional service methods (Phase 8). May be
	// nil in tests that don't exercise transactions; the service code
	// guards against that and skips WithTx, calling repos directly.
	// This nil-tolerance is what lets the Phase 7 fake-based tests
	// continue to work without each gaining a real database.
	DB *database.DB

	// Auditor records to the audit_outbox (Phase 8). May be nil in
	// tests; the service helper is nil-safe.
	Auditor audit.Recorder
}

// TaskService holds task business logic. Fields are unexported;
// construct with NewTaskService.
type TaskService struct {
	tasks    repository.TaskRepository
	projects repository.ProjectRepository
	workflow *config.Workflow
	db       *database.DB
	auditor  audit.Recorder
}

// ProjectService holds project business logic.
type ProjectService struct {
	projects repository.ProjectRepository
	db       *database.DB
	auditor  audit.Recorder
}

// NewTaskService wires a TaskService from its dependencies.
func NewTaskService(d Deps) *TaskService {
	return &TaskService{
		tasks:    d.Tasks,
		projects: d.Projects,
		workflow: d.Workflow,
		db:       d.DB,
		auditor:  d.Auditor,
	}
}

// NewProjectService wires a ProjectService.
func NewProjectService(d Deps) *ProjectService {
	return &ProjectService{
		projects: d.Projects,
		db:       d.DB,
		auditor:  d.Auditor,
	}
}
