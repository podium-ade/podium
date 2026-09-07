package api

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

func TestListPlaybooksReportsWhatTheChipNeeds(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Profiles: profiles.NewLive(&profiles.Profile{
		Name:                "podium",
		DisplayName:         "Podium",
		Model:               "claude-opus-5",
		DefaultPlaybook:     "general",
		ChatDefaultPlaybook: "analyst",
		Playbooks: map[string]profiles.Playbook{
			"general": {Name: "general", Image: "podium-agent-runtime:dev",
				SystemPrompt: "# The general playbook\n\nAnswer the question in the thread.\n"},
			// An image built FROM podium-agent-runtime: Podium ships one image and a
			// playbook needing more tools names one of your own.
			"analyst": {Name: "analyst", Image: "local/agent-warehouse:dev",
				SystemPrompt: "Answer questions about the data warehouse.\n"},
		},
	})})

	res, err := svc.ListPlaybooks(loginCtx("alice"), connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "Podium", res.Msg.GetProfileDisplayName())
	require.Len(t, res.Msg.GetPlaybooks(), 2)

	// Sorted by name, so the chip cycles in a stable order.
	assert.Equal(t, "analyst", res.Msg.GetPlaybooks()[0].GetName())
	assert.Equal(t, "local/agent-warehouse:dev", res.Msg.GetPlaybooks()[0].GetImage())
	assert.True(t, res.Msg.GetPlaybooks()[0].GetChatDefault())
	assert.Equal(t, "Answer questions about the data warehouse.", res.Msg.GetPlaybooks()[0].GetHint())

	assert.Equal(t, "general", res.Msg.GetPlaybooks()[1].GetName())
	assert.False(t, res.Msg.GetPlaybooks()[1].GetChatDefault())
	// The markdown heading is skipped: "# The general playbook" says less than the line under it.
	assert.Equal(t, "Answer the question in the thread.", res.Msg.GetPlaybooks()[1].GetHint())
}

func TestTheChatDefaultFallsBackToTheProfileDefault(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Profiles: profiles.NewLive(&profiles.Profile{
		DisplayName:     "Podium",
		DefaultPlaybook: "general",
		Playbooks:       map[string]profiles.Playbook{"general": {Name: "general", SystemPrompt: "Answer."}},
	})})
	res, err := svc.ListPlaybooks(loginCtx("alice"), connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetPlaybooks(), 1)
	assert.True(t, res.Msg.GetPlaybooks()[0].GetChatDefault())
}

func TestListPlaybooksWithNoProfileSaysSo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{})
	_, err := svc.ListPlaybooks(loginCtx("alice"), connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestPromptHintIsOneCappedLine(t *testing.T) {
	assert.Empty(t, promptHint(""))
	assert.Empty(t, promptHint("# Only a heading\n\n## And another\n"))
	assert.Equal(t, "first line", promptHint("\n\n  first line  \nsecond line\n"))

	long := strings.Repeat("é", maxPlaybookHintChars+40)
	// Capped on a rune boundary: a hint cut mid-character renders as a replacement glyph.
	assert.Equal(t, strings.Repeat("é", maxPlaybookHintChars)+"…", promptHint(long))
}
