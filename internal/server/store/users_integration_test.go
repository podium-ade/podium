//go:build integration

package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListUsers(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)

	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)
	_, err = s.UpsertUser(ctx, "bob@acme.com", "Bob")
	require.NoError(t, err)
	_, err = s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)

	users, err := s.ListUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "alice@acme.com", users[0].Login, "most recently seen first")
	require.Equal(t, "bob@acme.com", users[1].Login)
}

func TestAssignRole(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)
	_, err = s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.NoError(t, err)
	_, err = s.UpsertGoogleUser(ctx, "bob@acme.com", "Bob", "acme.com", "")
	require.NoError(t, err)

	bob, err := s.AssignRole(ctx, "bob@acme.com", RoleAdmin)
	require.NoError(t, err)
	require.Equal(t, []string{RoleAdmin}, bob.Roles)

	_, err = s.AssignRole(ctx, "bob@acme.com", "viewer")
	require.ErrorIs(t, err, ErrInvalidRole)

	_, err = s.AssignRole(ctx, "nobody@acme.com", RoleMember)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAssignRoleRefusesTheLastOwner(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)
	_, err = s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.NoError(t, err)

	_, err = s.AssignRole(ctx, "alice@acme.com", RoleMember)
	require.ErrorIs(t, err, ErrLastOwner)

	_, err = s.UpsertGoogleUser(ctx, "bob@acme.com", "Bob", "acme.com", "")
	require.NoError(t, err)
	_, err = s.AssignRole(ctx, "bob@acme.com", RoleOwner)
	require.NoError(t, err)

	alice, err := s.AssignRole(ctx, "alice@acme.com", RoleAdmin)
	require.NoError(t, err, "a second owner can be demoted")
	require.Equal(t, []string{RoleAdmin}, alice.Roles)

	_, err = s.AssignRole(ctx, "bob@acme.com", RoleMember)
	require.ErrorIs(t, err, ErrLastOwner, "bob is now the last owner")
}
