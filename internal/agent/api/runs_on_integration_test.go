//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func TestWhereTurnsRunIsSavedAndNodesUntilThen(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	got, err := f.svc.GetRunsOn(context.Background(), connect.NewRequest(&agentv1.GetRunsOnRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "nodes", got.Msg.GetRunsOn().GetRunsOn())
	assert.Nil(t, got.Msg.GetRunsOn().GetUpdatedAt())

	saved, err := f.svc.SetRunsOn(loginCtx("ada"), connect.NewRequest(&agentv1.SetRunsOnRequest{RunsOn: "boat"}))
	require.NoError(t, err)
	assert.Equal(t, "ada", saved.Msg.GetRunsOn().GetUpdatedBy())
	got, err = f.svc.GetRunsOn(context.Background(), connect.NewRequest(&agentv1.GetRunsOnRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "boat", got.Msg.GetRunsOn().GetRunsOn())

	_, err = f.svc.SetRunsOn(loginCtx("ada"), connect.NewRequest(&agentv1.SetRunsOnRequest{RunsOn: "boat,modal"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
