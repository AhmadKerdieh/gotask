package domain

import (
	"time"

	"github.com/google/uuid"
)

// Project groups Tasks. A Task always belongs to exactly one Project.
//
// Key is the short, human-readable identifier shown in task references
// like "GOTASK-42" — the prefix comes from the project Key. Keys must be
// unique across the system; we'll enforce that with a unique index in
// Phase 3.
type Project struct {
	ID          uuid.UUID
	Key         string // short uppercase code, e.g. "GOTASK"
	Name        string
	Description string
	OwnerID     uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewProjectInput captures the data needed to create a Project.
type NewProjectInput struct {
	Key         string
	Name        string
	Description string
	OwnerID     uuid.UUID
}
