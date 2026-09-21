//go:build integration

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertSlackChannelNamePreservesDescription(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertSlackChannelName(ctx, "C1", "support"))
	row, err := s.SetSlackChannelDescription(ctx, "C1", "Customer complaints.")
	require.NoError(t, err)
	assert.Equal(t, "support", row.Name)
	assert.Equal(t, "Customer complaints.", row.Description)

	require.NoError(t, s.UpsertSlackChannelName(ctx, "C1", "help"))
	got, err := s.SlackChannel(ctx, "C1")
	require.NoError(t, err)
	assert.Equal(t, "help", got.Name)
	assert.Equal(t, "Customer complaints.", got.Description, "a name refresh must not wipe the note")
}

func TestUpsertSlackChannelNameIgnoresAnEmptyRefresh(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertSlackChannelName(ctx, "C1", "support"))
	require.NoError(t, s.UpsertSlackChannelName(ctx, "C1", ""))
	got, err := s.SlackChannel(ctx, "C1")
	require.NoError(t, err)
	assert.Equal(t, "support", got.Name)
}

func TestSetSlackChannelDescriptionRefusesAMissingChannel(t *testing.T) {
	s := newStore(t)
	_, err := s.SetSlackChannelDescription(context.Background(), "C-missing", "note")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSetSlackChannelDescriptionCapsLength(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.UpsertSlackChannelName(ctx, "C1", "support"))
	_, err := s.SetSlackChannelDescription(ctx, "C1", strings.Repeat("x", MaxSlackChannelDescriptionRunes+1))
	require.ErrorIs(t, err, ErrInvalidSlackChannelDescription)
}

func TestSetChatChannel(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	chat, err := s.CreateMirrorChat(ctx, slackKey, OriginSlack, "alice", "hello")
	require.NoError(t, err)
	require.NoError(t, s.SetChatChannel(ctx, chat.ID, "support"))
	got, err := s.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, "support", got.Channel)
}
