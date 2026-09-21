//go:build integration

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaimInstance(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)

	_, err := s.GetInstance(ctx)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.ErrorIs(t, err, ErrNotFound, "a login that has never been seen cannot claim")

	alice, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)
	require.Equal(t, "acme.com", alice.HostedDomain)
	require.Empty(t, alice.Roles)

	inst, err := s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.NoError(t, err)
	require.Equal(t, "acme.com", inst.HostedDomain)
	require.Equal(t, "alice@acme.com", inst.ClaimedBy)
	require.False(t, inst.ClaimedAt.IsZero())

	again, err := s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.NoError(t, err, "re-claiming the same domain as the owner is a no-op")
	require.Equal(t, inst.ClaimedAt, again.ClaimedAt)

	_, err = s.ClaimInstance(ctx, "alice@acme.com", "other.com")
	require.ErrorIs(t, err, ErrAlreadyClaimed)

	bob, err := s.UpsertUser(ctx, "bob@acme.com", "Bob")
	require.NoError(t, err)
	_, err = s.ClaimInstance(ctx, "bob@acme.com", "acme.com")
	require.ErrorIs(t, err, ErrAlreadyClaimed)

	owner, err := s.GetUser(ctx, "alice@acme.com")
	require.NoError(t, err)
	require.Equal(t, []string{RoleOwner}, owner.Roles)
	require.Empty(t, bob.Roles, "a later login is not promoted by someone else's failed claim")
}

func TestClaimInstanceDomainMustMatch(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)
	_, err = s.ClaimInstance(ctx, "alice@acme.com", "evil.com")
	require.ErrorIs(t, err, ErrDomainMismatch)
}

func TestUpsertGoogleUserJoinsAsMemberAfterClaim(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)

	unclaimed, err := s.UpsertGoogleUser(ctx, "alice@acme.com", "Alice", "acme.com", "")
	require.NoError(t, err)
	require.Empty(t, unclaimed.Roles)

	_, err = s.ClaimInstance(ctx, "alice@acme.com", "acme.com")
	require.NoError(t, err)

	bob, err := s.UpsertGoogleUser(ctx, "bob@acme.com", "Bob", "acme.com", "")
	require.NoError(t, err)
	require.Equal(t, []string{RoleMember}, bob.Roles)

	_, err = s.UpsertGoogleUser(ctx, "eve@other.com", "Eve", "other.com", "")
	require.ErrorIs(t, err, ErrDomainMismatch)

	stillOwner, err := s.UpsertGoogleUser(ctx, "alice@acme.com", "Alice A.", "acme.com", "https://lh3.googleusercontent.com/a/alice")
	require.NoError(t, err)
	require.Equal(t, []string{RoleOwner}, stillOwner.Roles, "a second sign-in does not demote the owner")
	require.Equal(t, "Alice A.", stillOwner.DisplayName)
	require.Equal(t, "https://lh3.googleusercontent.com/a/alice", stillOwner.PictureURL)
}

func TestSessionRoundTrip(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)

	token, err := s.CreateSession(ctx, "alice@acme.com", time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	got, err := s.GetSession(ctx, token)
	require.NoError(t, err)
	require.Equal(t, "alice@acme.com", got.Login)
	require.True(t, got.ExpiresAt.After(time.Now()))

	_, err = s.GetSession(ctx, "nope")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.DeleteSession(ctx, token))
	_, err = s.GetSession(ctx, token)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.DeleteSession(ctx, token), "logout is idempotent")
}

func TestSessionExpires(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	_, err := s.UpsertUser(ctx, "alice@acme.com", "Alice")
	require.NoError(t, err)

	token, err := s.CreateSession(ctx, "alice@acme.com", time.Millisecond)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	_, err = s.GetSession(ctx, token)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestHostedDomainIsFirstWrite(t *testing.T) {
	ctx := t.Context()
	s := newStore(t)
	first, err := s.UpsertUser(ctx, "bob@alias.com", "Bob")
	require.NoError(t, err)
	require.Equal(t, "alias.com", first.HostedDomain)

	google, err := s.upsertUser(ctx, "bob@alias.com", "Bob", "acme.com", "")
	require.NoError(t, err)
	require.Equal(t, "alias.com", google.HostedDomain, "a later hd does not overwrite the first write")
}
