//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, err := s.GetUser(ctx, "alvaro@affiniti.com")
	require.ErrorIs(t, err, ErrNotFound)

	first, err := s.UpsertUser(ctx, "alvaro@affiniti.com", "Alvaro Ibarguen")
	require.NoError(t, err)
	require.Equal(t, "alvaro@affiniti.com", first.Login)
	require.Equal(t, "Alvaro Ibarguen", first.DisplayName)
	require.Empty(t, first.Roles, "roles start empty; RBAC is a later slice")
	require.False(t, first.FirstSeenAt.IsZero())

	// Seeing the same person again is idempotent and does not move first_seen_at.
	again, err := s.UpsertUser(ctx, "alvaro@affiniti.com", "Alvaro Ibarguen")
	require.NoError(t, err)
	require.Equal(t, first.FirstSeenAt, again.FirstSeenAt)

	// A refreshed display name lands; an empty one does not erase what is known.
	renamed, err := s.UpsertUser(ctx, "alvaro@affiniti.com", "Alvaro I.")
	require.NoError(t, err)
	require.Equal(t, "Alvaro I.", renamed.DisplayName)
	blank, err := s.UpsertUser(ctx, "alvaro@affiniti.com", "")
	require.NoError(t, err)
	require.Equal(t, "Alvaro I.", blank.DisplayName)

	got, err := s.GetUser(ctx, "alvaro@affiniti.com")
	require.NoError(t, err)
	require.Equal(t, renamed.Login, got.Login)

	_, err = s.UpsertUser(ctx, "", "nobody")
	require.ErrorContains(t, err, "login is required")
}

func TestNodeTailscaleDeviceBinding(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// A node enrolled over the local transport has no device to bind to.
	dev, err := s.CreateNode(ctx, NewNode{Name: "local", NodeKeyHash: HashToken("k-local")})
	require.NoError(t, err)
	require.Empty(t, dev.TSStableID)

	// A node enrolled over the tailnet records the device it came from, at insert time, so the
	// row is never briefly unbound.
	bound, err := s.CreateNode(ctx, NewNode{
		Name:        "podiumbot1",
		NodeKeyHash: HashToken("k-podiumbot1"),
		TSStableID:  "nPODIUMBOT1CNTRL",
	})
	require.NoError(t, err)
	require.Equal(t, "nPODIUMBOT1CNTRL", bound.TSStableID)

	got, err := s.GetNode(ctx, bound.ID)
	require.NoError(t, err)
	require.Equal(t, "nPODIUMBOT1CNTRL", got.TSStableID)

	byKey, err := s.GetNodeByKeyHash(ctx, HashToken("k-podiumbot1"))
	require.NoError(t, err)
	require.Equal(t, "nPODIUMBOT1CNTRL", byKey.TSStableID, "the authentication lookup must carry the binding")

	// Binding an unbound node, then rekeying it, then binding it somewhere else.
	require.NoError(t, s.SetNodeTSStableID(ctx, dev.ID, "nLAPTOPCNTRL"))
	got, err = s.GetNode(ctx, dev.ID)
	require.NoError(t, err)
	require.Equal(t, "nLAPTOPCNTRL", got.TSStableID)

	require.NoError(t, s.SetNodeTSStableID(ctx, dev.ID, ""))
	got, err = s.GetNode(ctx, dev.ID)
	require.NoError(t, err)
	require.Empty(t, got.TSStableID, "rekey clears the binding")

	require.NoError(t, s.SetNodeTSStableID(ctx, dev.ID, "nREPLACEMENTCNTRL"))
	got, err = s.GetNode(ctx, dev.ID)
	require.NoError(t, err)
	require.Equal(t, "nREPLACEMENTCNTRL", got.TSStableID)

	require.ErrorIs(t, s.SetNodeTSStableID(ctx, "node_does_not_exist", "nX"), ErrNotFound)
}
