package profiles

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func savedAssistant(files *Profile) AssistantDefinition {
	return AssistantDefinition{
		Name:         files.Name,
		DisplayName:  files.DisplayName,
		SystemPrompt: files.SystemPrompt,
		Model:        files.Model,
		Agent:        files.Agent,
		Effort:       files.Effort,
		Git:          files.Git,
		Skills:       append([]string(nil), files.Skills...),
		MCPServers:   append([]string(nil), files.MCPServers...),
		MaxTurns:     files.MaxTurns,
		Timeout:      "15m",
	}
}

func TestADefinitionReplacesTheAssistantAndLeavesPlaybooks(t *testing.T) {
	files := fileProfile(t)
	def := savedAssistant(files)
	def.Name = "night"
	def.DisplayName = "Night"
	def.SystemPrompt = "answer as the night shift"
	def.Model = "claude-haiku-5"
	def.Skills = []string{}
	def.MaxTurns = 0
	def.Timeout = "30m"
	def.Git = GitPersona{}

	got, err := ApplyDefinition(files, def)
	require.NoError(t, err)
	assert.Equal(t, "night", got.Name)
	assert.Equal(t, "Night", got.DisplayName)
	assert.Equal(t, "answer as the night shift", got.SystemPrompt)
	assert.Equal(t, "claude-haiku-5", got.Model)
	assert.Empty(t, got.Skills)
	assert.Zero(t, got.MaxTurns)
	assert.Equal(t, 30*time.Minute, got.Assistant().Timeout)
	assert.Equal(t, "general", got.DefaultPlaybook, "routing's unused default stays on the file")
	assert.Equal(t, []string{"general"}, got.PlaybookNames())
	assert.Equal(t, "podium", files.Name, "the file half is not rewritten")
	assert.Equal(t, "you are Podium", files.SystemPrompt)
}

func TestASavedPromptWinsOverALaterFile(t *testing.T) {
	files := fileProfile(t)
	def := savedAssistant(files)
	def.SystemPrompt = "saved prompt"

	got, err := ApplyDefinition(files, def)
	require.NoError(t, err)

	files.SystemPrompt = "edited on disk"
	again, err := ApplyDefinition(files, def)
	require.NoError(t, err)
	assert.Equal(t, "saved prompt", got.SystemPrompt)
	assert.Equal(t, "saved prompt", again.SystemPrompt)
}

func TestADefinitionRefusesAnIncompleteAssistant(t *testing.T) {
	files := fileProfile(t)
	_, err := ApplyDefinition(files, AssistantDefinition{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system_prompt")

	huge := savedAssistant(files)
	huge.SystemPrompt = strings.Repeat("a", MaxSystemPromptBytes+1)
	_, err = ApplyDefinition(files, huge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system_prompt")

	bad := savedAssistant(files)
	bad.Timeout = "tomorrow"
	_, err = ApplyDefinition(files, bad)
	require.Error(t, err)

	half := savedAssistant(files)
	half.Git = GitPersona{Name: "Only"}
	_, err = ApplyDefinition(files, half)
	require.Error(t, err)
	assert.Equal(t, "Podium", files.DisplayName)
}

func TestTheCatalogKeepsEveryDefinitionAndRunsOne(t *testing.T) {
	var cat AssistantCatalog
	first, err := cat.Add(AssistantDefinition{
		Name: "podium", DisplayName: "Podium", SystemPrompt: "prompt", Model: "claude-opus-5", Timeout: "15m",
	})
	require.NoError(t, err)
	require.Len(t, first.Definitions, 1)
	assert.Equal(t, first.Definitions[0].ID, first.ActiveID)
	assert.True(t, strings.HasPrefix(first.ActiveID, "ast_"))

	second, err := first.Add(AssistantDefinition{
		Name: "night", DisplayName: "Night", SystemPrompt: "prompt", Model: "claude-opus-5", Timeout: "15m",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ActiveID, second.ActiveID, "adding one does not change which assistant answers")
	assert.Len(t, second.Definitions, 2)

	_, err = second.Add(AssistantDefinition{
		Name: "podium", DisplayName: "Again", SystemPrompt: "prompt", Model: "claude-opus-5", Timeout: "15m",
	})
	require.Error(t, err)

	active, ok := second.Active()
	require.True(t, ok)
	active.DisplayName = "Podium Night"
	replaced, err := second.Upsert(active)
	require.NoError(t, err)
	got, ok := replaced.Active()
	require.True(t, ok)
	assert.Equal(t, "Podium Night", got.DisplayName)
	assert.Equal(t, active.ID, got.ID)
	assert.Len(t, replaced.Definitions, 2)

	_, err = replaced.Upsert(AssistantDefinition{
		ID: "ast_missing", Name: "other", DisplayName: "Other",
		SystemPrompt: "prompt", Model: "claude-opus-5", Timeout: "15m",
	})
	require.Error(t, err)

	switched, err := replaced.SetActive(second.Definitions[1].ID)
	require.NoError(t, err)
	now, ok := switched.Active()
	require.True(t, ok)
	assert.Equal(t, "night", now.Name)
}

func TestACapOfNoneRoundTripsThroughADefinition(t *testing.T) {
	raw, err := json.Marshal(AssistantDefinition{
		ID: "ast_1", Name: "podium", MaxTurns: 0, Skills: []string{}, MCPServers: []string{},
	})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"max_turns":0`)
	assert.Contains(t, string(raw), `"skills":[]`)

	var back AssistantDefinition
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Zero(t, back.MaxTurns)
	assert.Empty(t, back.Skills)
	assert.NotNil(t, back.Skills)
}
