package api

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func TestListPlaybooksReportsTheAssistantAndWhatItCanDelegateTo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Profiles: profiles.NewLive(&profiles.Profile{
		Name:            "podium",
		DisplayName:     "Podium",
		Model:           "claude-opus-5",
		Effort:          "high",
		DefaultPlaybook: "general",
		Playbooks: map[string]profiles.Playbook{
			"general": {Name: "general", Image: "podium-agent-runtime:dev",
				SystemPrompt: "# The general playbook\n\nAnswer the question in the thread.\n"},
			// An image built FROM podium-agent-runtime: Podium ships one image and a
			// playbook needing more tools names one of your own.
			"analyst": {Name: "analyst", Image: "local/agent-warehouse:dev",
				SystemPrompt: "Answer questions about the data warehouse.\n",
				Agent:        "grok", Model: "grok-4.6"},
		},
	})})

	res, err := svc.ListPlaybooks(loginCtx("alice"), connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.NoError(t, err)

	// Who answers the conversation, resolved: profile.yaml's own triple, with the backend
	// filled in. This is what the composer shows as the thing it is about to override.
	a := res.Msg.GetAssistant()
	require.NotNil(t, a)
	assert.Equal(t, "Podium", a.GetDisplayName())
	assert.Equal(t, "claude", a.GetAgent())
	assert.Equal(t, "claude-opus-5", a.GetModel())
	assert.Equal(t, "high", a.GetEffort())

	// And what it may delegate to. Sorted by name, so the list is stable.
	require.Len(t, res.Msg.GetPlaybooks(), 2)
	assert.Equal(t, "analyst", res.Msg.GetPlaybooks()[0].GetName())
	assert.Equal(t, "local/agent-warehouse:dev", res.Msg.GetPlaybooks()[0].GetImage())
	assert.Equal(t, "Answer questions about the data warehouse.", res.Msg.GetPlaybooks()[0].GetHint())
	// A playbook's own model, which the assistant's does not change and is not changed by.
	assert.Equal(t, "grok-4.6", res.Msg.GetPlaybooks()[0].GetModel())

	assert.Equal(t, "general", res.Msg.GetPlaybooks()[1].GetName())
	assert.Equal(t, "claude-opus-5", res.Msg.GetPlaybooks()[1].GetModel(), "inherited from the profile")
	// The markdown heading is skipped: "# The general playbook" says less than the line under it.
	assert.Equal(t, "Answer the question in the thread.", res.Msg.GetPlaybooks()[1].GetHint())
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
