package service

import (
	"gotask/internal/audit"
	"gotask/internal/config"
	"gotask/internal/repository"
)

// Deps is the constructor input for the service layer. It carries the
// repository INTERFACES (not the Postgres implementations), the workflow
// config, and from Phase 7 the audit recorder.
//
// Because these are interfaces, production wiring injects the Postgres
// repos + the Postgres auditor while tests inject in-memory fakes — the
// service code is identical either way.
type Deps struct {
	Tasks    repository.TaskRepository
	Projects repository.ProjectRepository
	Workflow *config.Workflow

	// Auditor records authorization-relevant actions. The service emits
	// audit events as part of business operations (NOT via middleware —
	// middleware sees HTTP, not outcomes; see the audit package doc).
	// May be nil during early-startup tests; the service guards against
	// that by using the audit-or-noop helper.
	Auditor audit.Recorder
}

// TaskService holds task business logic. Fields are unexported; construct
// with NewTaskService.
type TaskService struct {
	tasks    repository.TaskRepository
	projects repository.ProjectRepository
	workflow *config.Workflow
	auditor  audit.Recorder
}

// ProjectService holds project business logic.
type ProjectService struct {
	projects repository.ProjectRepository
	auditor  audit.Recorder
}

// NewTaskService wires a TaskService from its dependencies.
func NewTaskService(d Deps) *TaskService {
	return &TaskService{
		tasks:    d.Tasks,
		projects: d.Projects,
		workflow: d.Workflow,
		auditor:  d.Auditor,
	}
}

// NewProjectService wires a ProjectService.
func NewProjectService(d Deps) *ProjectService {
	return &ProjectService{
		projects: d.Projects,
		auditor:  d.Auditor,
	}
}
