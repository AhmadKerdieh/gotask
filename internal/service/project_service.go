package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"gotask/internal/domain"
	"gotask/internal/repository"
)

// CreateProjectInput is the service-level input for creating a project.
type CreateProjectInput struct {
	Key         string
	Name        string
	Description string
	OwnerID     uuid.UUID
}

// Create validates and persists a project. The project key is a
// human-facing identifier (the "GOTASK" in "GOTASK-42"); the business
// rule that it is uppercased and bounded lives here, not in the handler
// or the database.
func (s *ProjectService) Create(ctx context.Context, in CreateProjectInput) (domain.Project, error) {
	ve := newValidationError()

	// Normalising the key is a business rule: "gotask" and "GOTASK"
	// must not be two different projects. Uppercase, trim, then
	// validate the normalised form.
	key := strings.ToUpper(strings.TrimSpace(in.Key))
	switch {
	case key == "":
		ve.add("key", "required")
	case len(key) > 10:
		ve.add("key", "must be at most 10 characters")
	case !isAlnum(key):
		ve.add("key", "must be letters and digits only")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		ve.add("name", "required")
	} else if len(name) > 200 {
		ve.add("name", "must be at most 200 characters")
	}

	if in.OwnerID == uuid.Nil {
		ve.add("owner_id", "required")
	}

	if err := ve.orNil(); err != nil {
		return domain.Project{}, err
	}

	created, err := s.projects.Create(ctx, repository.NewProjectInput{
		Key:         key,
		Name:        name,
		Description: in.Description,
		OwnerID:     in.OwnerID,
	})
	if err != nil {
		// A duplicate key surfaces from the repository as a conflict
		// (unique violation). Translate to the service vocabulary so
		// the handler maps it to 409 without knowing about pg codes.
		return domain.Project{}, mapRepoError(err)
	}
	return created, nil
}

// Get returns a single project or ErrNotFound.
func (s *ProjectService) Get(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	p, err := s.projects.GetByID(ctx, id)
	if err != nil {
		return domain.Project{}, mapRepoError(err)
	}
	return p, nil
}

// List returns all projects.
func (s *ProjectService) List(ctx context.Context) ([]domain.Project, error) {
	ps, err := s.projects.List(ctx)
	if err != nil {
		return nil, mapRepoError(err)
	}
	return ps, nil
}

// isAlnum reports whether s consists solely of ASCII letters and digits.
// Project keys feed into task references shown to users ("GOTASK-42"), so
// we keep them to a conservative character set rather than allowing
// arbitrary Unicode.
func isAlnum(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
