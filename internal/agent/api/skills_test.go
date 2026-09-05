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

func TestListSkillsReportsWhatTheChipNeeds(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Profiles: profiles.NewLive(&profiles.Profile{
		Name:             "podium",
		DisplayName:      "Podium",
		Model:            "claude-opus-5",
		DefaultSkill:     "general",
		ChatDefaultSkill: "analyst",
		Skills: map[string]profiles.Skill{
			"general": {Name: "general", Image: "podium-agent-runtime:dev",
				SystemPrompt: "# The general skill\n\nAnswer the question in the thread.\n"},
			"analyst": {Name: "analyst", Image: "podium-agent-runtime-data:dev",
				SystemPrompt: "Answer questions about the data warehouse.\n"},
		},
	})})

	res, err := svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "Podium", res.Msg.GetProfileDisplayName())
	require.Len(t, res.Msg.GetSkills(), 2)

	// Sorted by name, so the chip cycles in a stable order.
	assert.Equal(t, "analyst", res.Msg.GetSkills()[0].GetName())
	assert.Equal(t, "podium-agent-runtime-data:dev", res.Msg.GetSkills()[0].GetImage())
	assert.True(t, res.Msg.GetSkills()[0].GetChatDefault())
	assert.Equal(t, "Answer questions about the data warehouse.", res.Msg.GetSkills()[0].GetHint())

	assert.Equal(t, "general", res.Msg.GetSkills()[1].GetName())
	assert.False(t, res.Msg.GetSkills()[1].GetChatDefault())
	// The markdown heading is skipped: "# The general skill" says less than the line under it.
	assert.Equal(t, "Answer the question in the thread.", res.Msg.GetSkills()[1].GetHint())
}

func TestTheChatDefaultFallsBackToTheProfileDefault(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Profiles: profiles.NewLive(&profiles.Profile{
		DisplayName:  "Podium",
		DefaultSkill: "general",
		Skills:       map[string]profiles.Skill{"general": {Name: "general", SystemPrompt: "Answer."}},
	})})
	res, err := svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetSkills(), 1)
	assert.True(t, res.Msg.GetSkills()[0].GetChatDefault())
}

func TestListSkillsWithNoProfileSaysSo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{})
	_, err := svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestPromptHintIsOneCappedLine(t *testing.T) {
	assert.Empty(t, promptHint(""))
	assert.Empty(t, promptHint("# Only a heading\n\n## And another\n"))
	assert.Equal(t, "first line", promptHint("\n\n  first line  \nsecond line\n"))

	long := strings.Repeat("é", maxSkillHintChars+40)
	// Capped on a rune boundary: a hint cut mid-character renders as a replacement glyph.
	assert.Equal(t, strings.Repeat("é", maxSkillHintChars)+"…", promptHint(long))
}
