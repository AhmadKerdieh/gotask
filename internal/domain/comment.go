package domain

import (
	"time"

	"github.com/google/uuid"
)

// Comment is a note attached to a Task by a user.
//
// EditedAt is the zero value when the comment has never been edited.
// We use the zero value rather than a *time.Time for the reasons given
// in task.go (avoiding pointer-as-nullable in domain types).
type Comment struct {
	ID        uuid.UUID
	TaskID    uuid.UUID
	AuthorID  uuid.UUID
	Body      string
	CreatedAt time.Time
	EditedAt  time.Time
}

// NewCommentInput is the data needed to create a Comment.
type NewCommentInput struct {
	TaskID   uuid.UUID
	AuthorID uuid.UUID
	Body     string
}
