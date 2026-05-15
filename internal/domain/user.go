package domain

import (
	"time"

	"github.com/google/uuid"
)

// Role is what a user is allowed to do at the system level.
//
// Per-project permissions (member, observer, etc.) are a separate concept
// added in Phase 7. This Role is system-wide and comes from the Keycloak
// token claims after Phase 6 lands.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

func (r Role) String() string { return string(r) }

// User identifies who is acting. ID is the same UUID Keycloak issues
// (the "sub" claim) — we don't generate our own — so a single identity
// is referenced consistently across the token, our database, and logs.
type User struct {
	ID        uuid.UUID
	Email     string
	Name      string
	Role      Role
	CreatedAt time.Time
	UpdatedAt time.Time
}
