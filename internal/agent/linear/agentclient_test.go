package linear

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appClient is a client signed in as an app, against the stub.
func appClient(t *testing.T, s *stub) *Client {
	t.Helper()
	ts, err := NewTokenSource(TokenSourceOptions{
		TokenURL: s.tokenURL(), ClientID: fakeClientID, ClientSecret: fakeClientSecret,
	})
	require.NoError(t, err)
	c, err := NewClient(ClientOptions{Endpoint: s.endpoint(), Tokens: ts})
	require.NoError(t, err)
	return c
}

func TestAnActionIsPostedWithItsOwnFieldsAndNoBody(t *testing.T) {
	s := newStub(t)
	s.reply("PodiumAgentActivity", map[string]any{"agentActivityCreate": map[string]any{
		"success": true, "agentActivity": map[string]any{"id": "activity-1"},
	}})
	c := appClient(t, s)
	ctx := context.Background()
	_, err := c.CreateAgentActivity(ctx, "session-1",
		ActivityContent{Type: ActivityAction, Action: "Reading", Parameter: "go.mod"}, true)
	require.NoError(t, err)
	_, err = c.CreateAgentActivity(ctx, "session-1",
		ActivityContent{Type: ActivityAction, Action: "Running", Parameter: "make test", Result: "Failed"}, true)
	require.NoError(t, err)
	_, err = c.CreateAgentActivity(ctx, "session-1", ActivityContent{Type: ActivityThought, Body: "hm"}, true)
	require.NoError(t, err)

	calls := s.callsOf("PodiumAgentActivity")
	require.Len(t, calls, 3)
	content := func(i int) any { return calls[i].Vars["input"].(map[string]any)["content"] }
	assert.Equal(t, map[string]any{"type": "action", "action": "Reading", "parameter": "go.mod"}, content(0))
	assert.Equal(t, map[string]any{
		"type": "action", "action": "Running", "parameter": "make test", "result": "Failed",
	}, content(1))
	assert.Equal(t, map[string]any{"type": "thought", "body": "hm"}, content(2))
	assert.Equal(t, true, calls[1].Vars["input"].(map[string]any)["ephemeral"])
}

func TestALinkIsAddedToASessionWithoutReplacingItsOthers(t *testing.T) {
	s := newStub(t)
	s.reply("PodiumAgentSessionUpdate", map[string]any{"agentSessionUpdate": map[string]any{"success": true}})
	c := appClient(t, s)
	ctx := context.Background()
	require.NoError(t, c.AddSessionLinks(ctx, "session-1",
		ExternalURL{Label: "Podium task", URL: "https://podium.example/tasks/task-1"}))
	calls := s.callsOf("PodiumAgentSessionUpdate")
	require.Len(t, calls, 1)
	assert.Equal(t, "session-1", calls[0].Vars["id"])
	assert.Equal(t, map[string]any{"addedExternalUrls": []any{
		map[string]any{"label": "Podium task", "url": "https://podium.example/tasks/task-1"},
	}}, calls[0].Vars["input"], "added, never externalUrls, which would replace the session's links")

	require.NoError(t, c.AddSessionLinks(ctx, "session-1"), "no links is no request")
	assert.Len(t, s.callsOf("PodiumAgentSessionUpdate"), 1)

	s.reply("PodiumAgentSessionUpdate", map[string]any{"agentSessionUpdate": map[string]any{"success": false}})
	assert.Error(t, c.AddSessionLinks(ctx, "session-1", ExternalURL{Label: "x", URL: "https://x"}))
}

func TestAnIssuesStateIsReadWithoutItsComments(t *testing.T) {
	s := newStub(t)
	s.reply("PodiumIssueState", map[string]any{"issue": map[string]any{
		"id":    "issue-1",
		"state": map[string]any{"id": "state-1", "name": "Backlog", "type": StateTypeBacklog},
		"team":  map[string]any{"id": teamID, "key": "ENG"},
	}})
	c := appClient(t, s)
	state, team, err := c.IssueState(context.Background(), "issue-1")
	require.NoError(t, err)
	assert.Equal(t, WorkflowState{ID: "state-1", Name: "Backlog", Type: StateTypeBacklog}, state)
	assert.Equal(t, Team{ID: teamID, Key: "ENG"}, team)
	assert.Equal(t, "issue-1", s.callsOf("PodiumIssueState")[0].Vars["id"])

	s.reply("PodiumIssueState", map[string]any{"issue": nil})
	_, _, err = c.IssueState(context.Background(), "issue-2")
	assert.ErrorContains(t, err, "no issue issue-2")
}
