package authz_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"gotask/internal/authz"
	"gotask/internal/domain"
)

// These tests cover the predicates in isolation — no database, no HTTP,
// no service. The point of putting authz in its own package was exactly
// this: rules so small and so independent of I/O that they can be
// asserted exhaustively in microseconds.

func claims(sub string, roles ...string) authz.Claims {
	return authz.Claims{Subject: sub, Roles: roles}
}

func TestHasRole_Hierarchy(t *testing.T) {
	// member-only claims
	m := claims("11111111-1111-1111-1111-111111111111", authz.RoleMember)
	assert.True(t, authz.HasRole(m, authz.RoleMember))
	assert.False(t, authz.HasRole(m, authz.RoleManager))
	assert.False(t, authz.HasRole(m, authz.RoleAdmin))

	// manager — implies member
	mgr := claims("22222222-2222-2222-2222-222222222222", authz.RoleManager)
	assert.True(t, authz.HasRole(mgr, authz.RoleMember))
	assert.True(t, authz.HasRole(mgr, authz.RoleManager))
	assert.False(t, authz.HasRole(mgr, authz.RoleAdmin))

	// admin — implies manager AND member
	adm := claims("33333333-3333-3333-3333-333333333333", authz.RoleAdmin)
	assert.True(t, authz.HasRole(adm, authz.RoleMember))
	assert.True(t, authz.HasRole(adm, authz.RoleManager))
	assert.True(t, authz.HasRole(adm, authz.RoleAdmin))
}

func TestHasRole_UnknownRoleDenied(t *testing.T) {
	c := claims("anyone", authz.RoleAdmin)
	// Defensive default: unknown role names → false, regardless of how
	// high the caller's actual privilege. Returning true for unrecognised
	// input is the kind of bug that turns into a CVE.
	assert.False(t, authz.HasRole(c, "superuser"))
	assert.False(t, authz.HasRole(c, ""))
}

func TestHasRole_AnonymousDenied(t *testing.T) {
	anon := authz.Claims{} // Subject is empty
	assert.False(t, authz.HasRole(anon, authz.RoleMember))
	assert.False(t, authz.HasRole(anon, authz.RoleManager))
	assert.False(t, authz.HasRole(anon, authz.RoleAdmin))
}

func TestCanDeleteTask_OwnerOrManager(t *testing.T) {
	owner := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	stranger := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	task := domain.Task{ReporterID: owner}

	t.Run("owner may delete own", func(t *testing.T) {
		c := claims(owner.String(), authz.RoleMember)
		assert.True(t, authz.CanDeleteTask(c, task))
	})

	t.Run("stranger as member may NOT delete", func(t *testing.T) {
		c := claims(stranger.String(), authz.RoleMember)
		assert.False(t, authz.CanDeleteTask(c, task))
	})

	t.Run("stranger as manager may delete", func(t *testing.T) {
		c := claims(stranger.String(), authz.RoleManager)
		assert.True(t, authz.CanDeleteTask(c, task))
	})

	t.Run("admin inherits manager", func(t *testing.T) {
		c := claims(stranger.String(), authz.RoleAdmin)
		assert.True(t, authz.CanDeleteTask(c, task))
	})
}

func TestCanChangeTaskStatus_OwnerOrManager(t *testing.T) {
	owner := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	stranger := uuid.MustParse("99999999-8888-7777-6666-555555555555")
	task := domain.Task{ReporterID: owner}

	// Same logical shape as delete. We test it independently because in
	// future it may diverge (e.g. assignee may also change status); the
	// independent test gates that change.
	assert.True(t, authz.CanChangeTaskStatus(claims(owner.String(), authz.RoleMember), task))
	assert.False(t, authz.CanChangeTaskStatus(claims(stranger.String(), authz.RoleMember), task))
	assert.True(t, authz.CanChangeTaskStatus(claims(stranger.String(), authz.RoleManager), task))
}

func TestCanListAuditAcrossUsers_AdminOnly(t *testing.T) {
	// Listing across users is a NARROWER power than manager. We
	// explicitly check that manager does NOT get it — if the hierarchy
	// ever silently grants this to manager, this test fires.
	assert.False(t, authz.CanListAuditAcrossUsers(claims("x", authz.RoleMember)))
	assert.False(t, authz.CanListAuditAcrossUsers(claims("x", authz.RoleManager)))
	assert.True(t, authz.CanListAuditAcrossUsers(claims("x", authz.RoleAdmin)))
}

func TestHasAny_Empty(t *testing.T) {
	// Empty arguments must NOT match vacuously. A bug here that returned
	// true for HasAny() with no args would silently bypass every
	// RequireRole check.
	c := claims("x", authz.RoleAdmin)
	assert.False(t, c.HasAny())
}

func TestSubject_ParsesUUID(t *testing.T) {
	id := uuid.New()
	assert.Equal(t, id, authz.Subject(claims(id.String())))
	assert.Equal(t, uuid.Nil, authz.Subject(claims("not-a-uuid")))
	assert.Equal(t, uuid.Nil, authz.Subject(authz.Claims{}))
}
