package service

import (
	"gotask/internal/config"
	"gotask/internal/repository"
)

// Deps is the constructor input for the service layer. It carries the
// repository INTERFACES (not the Postgres implementations) and the
// workflow config. Because these are interfaces, production wiring
// injects the Postgres repos while tests inject in-memory fakes — the
// service code is identical either way. That substitutability is the
// entire point of the Phase 3 interface seam.
type Deps struct {
	Tasks    repository.TaskRepository
	Projects repository.ProjectRepository
	Workflow *config.Workflow
}

// TaskService holds task business logic. Fields are unexported; construct
// with NewTaskService.
type TaskService struct {
	tasks    repository.TaskRepository
	projects repository.ProjectRepository
	workflow *config.Workflow
}

// ProjectService holds project business logic.
type ProjectService struct {
	projects repository.ProjectRepository
}

// NewTaskService wires a TaskService from its dependencies.
func NewTaskService(d Deps) *TaskService {
	return &TaskService{
		tasks:    d.Tasks,
		projects: d.Projects,
		workflow: d.Workflow,
	}
}

// NewProjectService wires a ProjectService.
func NewProjectService(d Deps) *ProjectService {
	return &ProjectService{projects: d.Projects}
}
