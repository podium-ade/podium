//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A mirrored chat its owner deletes is tombstoned (0036): it is in nobody's lists and nobody's
// role reaches it, the owner's included, but the row stays so the conversation's next message
// finds it rather than opening a new record. A web chat is still removed outright.
func TestDeletingAMirroredChatTombstonesIt(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	member(t, s, alice, bob, carol)

	key := "slack:C1:500.1"
	c, err := s.CreateMirrorChat(ctx, key, OriginSlack, "alice", "a thread")
	require.NoError(t, err)
	claimed, err := s.ClaimChatOwner(ctx, c.ID, alice)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = s.AddChatParticipant(ctx, c.ID, bob, JoinedSlack, time.Now())
	require.NoError(t, err)
	_, err = s.SetChatGeneralAccess(ctx, c.ID, AccessMembers, RoleViewer)
	require.NoError(t, err)
	require.Contains(t, listed(t, s, alice, ""), c.ID)
	require.Contains(t, listed(t, s, bob, ""), c.ID)
	require.Contains(t, listed(t, s, carol, "workspace"), c.ID)

	require.NoError(t, s.DeleteChat(ctx, c.ID, alice, false))
	require.NoError(t, s.DeleteChat(ctx, c.ID, alice, false), "deleting a tombstone again is not an error")

	for _, who := range []string{alice, bob, carol} {
		assert.Equal(t, "", roleOf(t, s, c.ID, who), who+" has no role in a deleted chat")
		assert.NotContains(t, listed(t, s, who, ""), c.ID)
		assert.NotContains(t, listed(t, s, who, "shared"), c.ID)
		assert.NotContains(t, listed(t, s, who, "workspace"), c.ID)
	}
	// Speaking again does not bring it back.
	_, err = s.AddChatParticipant(ctx, c.ID, carol, JoinedSlack, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "", roleOf(t, s, c.ID, carol))
	assert.NotContains(t, listed(t, s, carol, ""), c.ID)

	// The record is still there for the mirror, and the dev token still sees it.
	again, err := s.ChatBySourceKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, c.ID, again.ID)
	require.NotNil(t, again.DeletedAt)
	all, _, err := s.ListChats(ctx, "", true, 100, "")
	require.NoError(t, err)
	assert.Contains(t, chatIDs(all), c.ID)

	// Somebody else cannot delete it, tombstone or not.
	assert.ErrorIs(t, s.DeleteChat(ctx, c.ID, bob, false), ErrNotFound)

	// A web chat is still removed.
	w, err := s.CreateChat(ctx, alice, "mine")
	require.NoError(t, err)
	require.NoError(t, s.DeleteChat(ctx, w.ID, alice, false))
	_, err = s.GetChat(ctx, w.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}
