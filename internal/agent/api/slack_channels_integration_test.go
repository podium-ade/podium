//go:build integration

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/slack"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeDirectory counts its calls because the point of the cache is how often Slack is asked.
type fakeDirectory struct {
	channels []slack.ChannelInfo
	err      error
	calls    int
}

func (d *fakeDirectory) ListChannels(context.Context) ([]slack.ChannelInfo, error) {
	d.calls++
	return d.channels, d.err
}

func newChannelsService(t *testing.T, dir SlackDirectory) *AgentService {
	t.Helper()
	svc := NewAgentService(AgentServiceOptions{
		Store:  newStore(t),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if dir != nil {
		svc.SetRunning(svc.runningConfig(), dir)
	}
	return svc
}

func listChannels(t *testing.T, svc *AgentService, refresh bool) *agentv1.ListSlackChannelsResponse {
	t.Helper()
	res, err := svc.ListSlackChannels(loginCtx("alice"),
		connect.NewRequest(&agentv1.ListSlackChannelsRequest{Refresh: refresh}))
	require.NoError(t, err)
	return res.Msg
}

func TestListSlackChannelsAsksSlackOnceThenServesTheMirror(t *testing.T) {
	dir := &fakeDirectory{channels: []slack.ChannelInfo{{ID: "C1", Name: "support"}}}
	svc := newChannelsService(t, dir)

	first := listChannels(t, svc, false)
	require.Len(t, first.GetChannels(), 1)
	assert.NotNil(t, first.GetRefreshedAt(), "the first listing fills the mirror and says when")
	assert.Equal(t, 1, dir.calls)

	// Slack now has a second channel; an ordinary listing must not go and look.
	dir.channels = append(dir.channels, slack.ChannelInfo{ID: "C2", Name: "eng"})
	second := listChannels(t, svc, false)
	assert.Len(t, second.GetChannels(), 1)
	assert.Equal(t, 1, dir.calls, "a listing without refresh must not call Slack")
	assert.True(t, second.GetRefreshedAt().AsTime().Equal(first.GetRefreshedAt().AsTime()))

	refreshed := listChannels(t, svc, true)
	assert.Len(t, refreshed.GetChannels(), 2)
	assert.Equal(t, 2, dir.calls)
	assert.False(t, refreshed.GetRefreshedAt().AsTime().Before(first.GetRefreshedAt().AsTime()))
}

func TestListSlackChannelsRefreshFailureIsAnErrorAndKeepsTheMirror(t *testing.T) {
	dir := &fakeDirectory{channels: []slack.ChannelInfo{{ID: "C1", Name: "support"}}}
	svc := newChannelsService(t, dir)
	first := listChannels(t, svc, false)

	dir.err = errors.New("slack is down")
	_, err := svc.ListSlackChannels(loginCtx("alice"),
		connect.NewRequest(&agentv1.ListSlackChannelsRequest{Refresh: true}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	again := listChannels(t, svc, false)
	assert.Len(t, again.GetChannels(), 1)
	assert.True(t, again.GetRefreshedAt().AsTime().Equal(first.GetRefreshedAt().AsTime()),
		"a failed refresh must not read as fresh")
}

func TestListSlackChannelsRetriesTheFirstFillUntilItWorks(t *testing.T) {
	dir := &fakeDirectory{err: errors.New("slack is down")}
	svc := newChannelsService(t, dir)

	res := listChannels(t, svc, false)
	assert.Empty(t, res.GetChannels())
	assert.Nil(t, res.GetRefreshedAt())

	dir.err, dir.channels = nil, []slack.ChannelInfo{{ID: "C1", Name: "support"}}
	res = listChannels(t, svc, false)
	assert.Len(t, res.GetChannels(), 1)
	assert.NotNil(t, res.GetRefreshedAt())
}

func TestListSlackChannelsRefreshNeedsSlack(t *testing.T) {
	svc := newChannelsService(t, nil)
	_, err := svc.ListSlackChannels(loginCtx("alice"),
		connect.NewRequest(&agentv1.ListSlackChannelsRequest{Refresh: true}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	res := listChannels(t, svc, false)
	assert.False(t, res.GetSlackConnected())
}
