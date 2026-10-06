package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidRole(t *testing.T) {
	t.Parallel()
	require.True(t, ValidRole(RoleOwner))
	require.True(t, ValidRole(RoleAdmin))
	require.True(t, ValidRole(RoleMember))
	require.False(t, ValidRole(""))
	require.False(t, ValidRole("viewer"))
}

func TestCanonicalRole(t *testing.T) {
	t.Parallel()
	require.Equal(t, RoleOwner, CanonicalRole([]string{RoleMember, RoleOwner}))
	require.Equal(t, RoleAdmin, CanonicalRole([]string{RoleAdmin, RoleMember}))
	require.Equal(t, RoleMember, CanonicalRole([]string{RoleMember}))
	require.Equal(t, RoleMember, CanonicalRole(nil))
}

func TestHasRole(t *testing.T) {
	t.Parallel()
	owner := []string{RoleOwner}
	admin := []string{RoleAdmin}
	member := []string{RoleMember}

	require.True(t, HasRole(owner, RoleOwner))
	require.True(t, HasRole(owner, RoleAdmin))
	require.True(t, HasRole(owner, RoleMember))

	require.False(t, HasRole(admin, RoleOwner))
	require.True(t, HasRole(admin, RoleAdmin))
	require.True(t, HasRole(admin, RoleMember))

	require.False(t, HasRole(member, RoleOwner))
	require.False(t, HasRole(member, RoleAdmin))
	require.True(t, HasRole(member, RoleMember))

	require.True(t, HasRole(nil, RoleMember), "empty roles are a member after a claim")
	require.False(t, HasRole(nil, RoleAdmin))
}
