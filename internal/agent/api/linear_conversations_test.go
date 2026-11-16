package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// TestTheLinearBoardAsksForAColumnLinearHas refuses a column that is no Linear state type before
// the store is asked: this service has none, so reaching it would panic.
func TestTheLinearBoardAsksForAColumnLinearHas(t *testing.T) {
	s := NewAgentService(AgentServiceOptions{Chat: struct{ ChatSource }{}, Logger: quietLogger()})
	list := func(ctx context.Context, stateType string) error {
		_, err := s.ListLinearConversations(ctx, connect.NewRequest(&agentv1.ListLinearConversationsRequest{
			LinearStateType: stateType,
		}))
		return err
	}

	for _, bad := range []string{"in progress", "done", "started;drop", "all"} {
		err := list(loginCtx("ana@acme.com"), bad)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), bad)
	}
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(list(context.Background(), "started")),
		"nobody signed in learns nothing, not even which columns there are")
}

func TestLinearStateTypeFilterIsLinearsTypesOrNone(t *testing.T) {
	for _, in := range []string{"", "triage", "backlog", "unstarted", " Started ", "completed", "canceled", "none"} {
		_, ok := store.LinearStateTypeFilter(in)
		assert.True(t, ok, in)
	}
	got, _ := store.LinearStateTypeFilter(" Started ")
	assert.Equal(t, "started", got)
	_, ok := store.LinearStateTypeFilter("cancelled")
	assert.False(t, ok, "Linear spells it canceled")
}
