// Package authz holds the application's authorization rules.
//
// What goes here vs. what goes in service vs. middleware:
//
//   - This package: PURE PREDICATES about who-may-do-what. No I/O,
//     no logging, no HTTP. Inputs are claims and (where relevant) the
//     resource. Outputs are bool / typed Decision values. Easy to read,
//     easy to test exhaustively, easy to change one rule without
//     touching unrelated ones.
//
//   - service/* uses these predicates. The service is what owns the
//     transaction "load → authorize → mutate → audit"; it CALLS
//     authz.CanX(...) rather than open-coding the rule.
//
//   - middleware uses RequireRole for ROUTE-COARSE checks ("the audit
//     list endpoint requires admin"). Middleware never authorizes on
//     specific resources because doing so would require it to load
//     resources, which is the service's job.
//
// Roles are the simplest model that fits this app. The Phase 7 set is:
//
//	member  — default; can act on their own resources
//	manager — can act on others' tasks (delete, status change)
//	admin   — can list audit history across all users
//
// Each higher role inherits all lower-role permissions; this is enforced
// in code (see HasRole) rather than configured anywhere, because three
// roles is simple enough that explicit > clever.
package authz

import (
	"github.com/google/uuid"

	"gotask/internal/domain"
)

// Claims is the typed view of a verified token's payload that other
// packages depend on. It LIVES HERE (not in middleware) so the service
// can depend on it without transitively importing net/http — keeping the
// "service has no HTTP knowledge" invariant the course has held since
// Phase 1.
//
// The auth middleware constructs Claims from the verified token and
// stashes them in the request context; downstream code retrieves them
// from context, but the TYPE is owned by authz, the package that
// actually reasons about them.
//
// Subject is the Keycloak "sub" claim — the stable, unique user id.
// Email and Name are convenience copies from the token; they are NOT
// authoritative and must never be used for an authorization decision —
// only Subject + Roles may.
type Claims struct {
	Subject string
	Email   string
	Name    string

	// Roles are the realm-level roles asserted by the token (from the
	// realm_access.roles claim). They are CRYPTOGRAPHICALLY VERIFIED:
	// the signature check guarantees Keycloak issued these roles for
	// this user. No per-request Keycloak call is needed to check a role.
	//
	// Trade-off: when a role is revoked in Keycloak, the user keeps it
	// until their token expires (max accessTokenLifespan, 5 min in our
	// realm). That is the standard JWT property; we accept it.
	Roles []string
}

// HasAny reports whether the claims include any of the given role names.
// Order-independent. Empty arguments → false (refuses to match
// vacuously).
func (c Claims) HasAny(roles ...string) bool {
	if len(roles) == 0 {
		return false
	}
	for _, want := range roles {
		for _, have := range c.Roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

// Role names exposed as constants so callers don't pass magic strings.
// Keycloak stores roles as plain strings; matching them in one place
// (here) means a Keycloak rename only requires changing these constants.
const (
	RoleMember  = "member"
	RoleManager = "manager"
	RoleAdmin   = "admin"
)

// HasRole reports whether the claims grant the given role, taking the
// hierarchy into account. admin implies manager implies member: a user
// with admin satisfies HasRole(RoleManager) without needing the manager
// role explicitly assigned in Keycloak.
//
// This is the only place the hierarchy is encoded. Every other check
// goes through HasRole, so if the hierarchy ever changes (e.g. adding a
// "support" role between member and manager), it changes here only.
func HasRole(c Claims, role string) bool {
	switch role {
	case RoleMember:
		return c.Subject != ""
	case RoleManager:
		return c.HasAny(RoleManager, RoleAdmin)
	case RoleAdmin:
		return c.HasAny(RoleAdmin)
	default:
		return false
	}
}

// CanCreateTask: any authenticated member can create.
func CanCreateTask(c Claims) bool { return HasRole(c, RoleMember) }

// CanCreateProject: any authenticated member can create.
func CanCreateProject(c Claims) bool { return HasRole(c, RoleMember) }

// CanDeleteTask: the reporter (owner) may delete their own task; a
// manager may delete any task.
func CanDeleteTask(c Claims, t domain.Task) bool {
	if HasRole(c, RoleManager) {
		return true
	}
	return isOwner(c, t.ReporterID)
}

// CanChangeTaskStatus: same shape as delete. Status changes are
// authorization-sensitive because they advance the workflow.
func CanChangeTaskStatus(c Claims, t domain.Task) bool {
	if HasRole(c, RoleManager) {
		return true
	}
	return isOwner(c, t.ReporterID)
}

// CanListAuditAcrossUsers: only admin. A manager isn't an auditor;
// audit-listing across users is a separate, narrower power.
func CanListAuditAcrossUsers(c Claims) bool { return HasRole(c, RoleAdmin) }

// Subject returns the caller's subject as a uuid.UUID, or uuid.Nil if it
// is missing/malformed.
func Subject(c Claims) uuid.UUID {
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func isOwner(c Claims, ownerSub uuid.UUID) bool {
	if ownerSub == uuid.Nil {
		return false
	}
	return Subject(c) == ownerSub
}
