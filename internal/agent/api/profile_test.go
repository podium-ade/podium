package api

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

func TestDefinitionFromRequestStoresTheWholeAssistant(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	def, err := definitionFromRequest(&agentv1.UpdateProfileRequest{
		Id:           " ast_1 ",
		Name:         " night ",
		DisplayName:  " Night ",
		SystemPrompt: "  answer as the night shift  ",
		Model:        " claude-haiku-5 ",
		Skills:       []string{" review ", ""},
		MaxTurns:     0,
		Timeout:      " 30m ",
		GitName:      " File ",
		GitEmail:     " file@users.noreply.github.com ",
	}, "ada", now)
	require.NoError(t, err)

	assert.Equal(t, "ast_1", def.ID)
	assert.Equal(t, "night", def.Name)
	assert.Equal(t, "Night", def.DisplayName)
	assert.Equal(t, "answer as the night shift", def.SystemPrompt)
	assert.Equal(t, "claude-haiku-5", def.Model)
	assert.Equal(t, []string{"review"}, def.Skills)
	assert.Empty(t, def.MCPServers)
	assert.NotNil(t, def.MCPServers)
	assert.Zero(t, def.MaxTurns)
	assert.Equal(t, "30m", def.Timeout)
	assert.Equal(t, "File", def.Git.Name)
	assert.Equal(t, "file@users.noreply.github.com", def.Git.Email)
	assert.Equal(t, "ada", def.UpdatedBy)
	assert.Equal(t, now, def.UpdatedAt)
}

func TestAnIncompleteAssistantIsRefused(t *testing.T) {
	_, err := definitionFromRequest(&agentv1.UpdateProfileRequest{DisplayName: "Night"}, "ada", time.Now())
	require.Error(t, err)

	_, err = definitionFromRequest(&agentv1.UpdateProfileRequest{SystemPrompt: "   "}, "ada", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system_prompt")

	_, err = definitionFromRequest(nil, "ada", time.Now())
	require.Error(t, err)
}

func TestAPromptThatWouldNotFitTheBriefIsRefused(t *testing.T) {
	_, err := definitionFromRequest(&agentv1.UpdateProfileRequest{
		Name:         "podium",
		DisplayName:  "Podium",
		Model:        "claude-opus-5",
		SystemPrompt: strings.Repeat("a", profiles.MaxSystemPromptBytes+1),
		Timeout:      "15m",
	}, "ada", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system_prompt")
}

func TestProfileToProtoReportsTheAssistantInForce(t *testing.T) {
	files := &profiles.Profile{
		Name:         "podium",
		DisplayName:  "Podium",
		SystemPrompt: "you are Podium",
		Model:        "claude-opus-5",
		Skills:       []string{"review"},
		MCPServers:   []string{"linear"},
		MaxTurns:     12,
		Git:          profiles.GitPersona{Name: "File", Email: "file@users.noreply.github.com"},
	}
	cur := *files
	cur.Name = "night"
	cur.DisplayName = "Night"
	cur.SystemPrompt = "answer as the night shift"
	cur.Skills = []string{}
	cur.MCPServers = []string{}
	cur.MaxTurns = 0
	cur.Timeout = spec.Duration(30 * time.Minute)
	cur.Git = profiles.GitPersona{}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	msg := profileToProto(&cur, files, profileSave{ID: "ast_1", UpdatedBy: "ada", UpdatedAt: when})

	assert.Equal(t, "ast_1", msg.GetId())
	assert.Equal(t, "night", msg.GetName())
	assert.Equal(t, "Night", msg.GetDisplayName())
	assert.Equal(t, "Podium", msg.GetFileDisplayName())
	assert.Equal(t, "answer as the night shift", msg.GetSystemPrompt())
	assert.Equal(t, "you are Podium", msg.GetFileSystemPrompt())
	assert.Empty(t, msg.GetSkills())
	assert.Equal(t, []string{"review"}, msg.GetFileSkills())
	assert.Empty(t, msg.GetMcpServers())
	assert.Equal(t, []string{"linear"}, msg.GetFileMcpServers())
	assert.EqualValues(t, 0, msg.GetMaxTurns())
	assert.EqualValues(t, 12, msg.GetFileMaxTurns())
	assert.Equal(t, "30m0s", msg.GetTimeout())
	assert.Empty(t, msg.GetFileTimeout())
	assert.Equal(t, profiles.DefaultAgent, msg.GetAgent())
	assert.Empty(t, msg.GetGitName())
	assert.Empty(t, msg.GetGitEmail())
	assert.Equal(t, "File", msg.GetFileGitName())
	assert.Equal(t, "file@users.noreply.github.com", msg.GetFileGitEmail())
	assert.Equal(t, "ada", msg.GetUpdatedBy())
	assert.Empty(t, msg.GetOverridden())
}
