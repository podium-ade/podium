package profiles

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileProfile(t *testing.T) *Profile {
	t.Helper()
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	return p
}

func TestAnOverrideReplacesTheFileValueAndAnEmptyOneClearsIt(t *testing.T) {
	files := fileProfile(t)

	got, err := Apply(files, Overrides{DisplayName: "Bot", Model: "claude-haiku-5"})
	require.NoError(t, err)
	assert.Equal(t, "Bot", got.DisplayName)
	assert.Equal(t, "claude-haiku-5", got.Model)
	assert.Equal(t, "general", got.DefaultPlaybook, "a field nobody overrode still comes from the file")

	back, err := Apply(files, Overrides{})
	require.NoError(t, err)
	assert.Equal(t, "Podium", back.DisplayName)
	assert.Equal(t, "claude-opus-5", back.Model)
}

func TestOverridesReportWhichFieldsTheySupply(t *testing.T) {
	assert.Empty(t, Overrides{}.Fields())
	assert.Equal(t,
		[]string{FieldDisplayName, FieldDefaultPlaybook},
		Overrides{DisplayName: "Bot", DefaultPlaybook: "general"}.Fields())
	assert.Empty(t, Overrides{DisplayName: "   "}.Trim().Fields(),
		"a field cleared to whitespace is a cleared override, not an invalid value")
}

func TestAnOverrideConfiguresTheAssistantAndLeavesTheFile(t *testing.T) {
	files := fileProfile(t)
	files.Skills = []string{"review"}
	files.MCPServers = []string{"linear"}
	files.Git = GitPersona{Name: "File", Email: "file@users.noreply.github.com"}
	prompt := "answer as the night shift"
	none := []string{}
	turns := 0
	timeout := "30m"
	git := GitPersona{}

	got, err := Apply(files, Overrides{
		SystemPrompt: &prompt,
		Skills:       &none,
		MCPServers:   &none,
		MaxTurns:     &turns,
		Timeout:      &timeout,
		Git:          &git,
	})
	require.NoError(t, err)
	assert.Equal(t, prompt, got.SystemPrompt)
	assert.Empty(t, got.Assistant().Skills)
	assert.Empty(t, got.Assistant().MCPServers)
	assert.Zero(t, got.Assistant().MaxTurns)
	assert.Equal(t, 30*time.Minute, got.Assistant().Timeout)
	assert.False(t, got.Git.Set())
	assert.Equal(t, "you are Podium", files.SystemPrompt, "the file half is what a revert returns to")
	assert.Equal(t, []string{"review"}, files.Skills)
	assert.Equal(t, "File", files.Git.Name)
}

func TestABlankPromptOverrideClearsRatherThanStoringNothing(t *testing.T) {
	files := fileProfile(t)
	blank := "  "
	got, err := Apply(files, Overrides{SystemPrompt: &blank})
	require.NoError(t, err)
	assert.Equal(t, "you are Podium", got.SystemPrompt)
	assert.NotContains(t, Overrides{SystemPrompt: &blank}.Trim().Fields(), FieldSystemPrompt)
}

func TestABadTimeoutAndAHalfWrittenPersonaAreRefused(t *testing.T) {
	files := fileProfile(t)
	bad := "tomorrow"
	_, err := Apply(files, Overrides{Timeout: &bad})
	require.Error(t, err)

	half := GitPersona{Name: "Only"}
	_, err = Apply(files, Overrides{Git: &half})
	require.Error(t, err)
	assert.Equal(t, "Podium", files.DisplayName)
}

func TestACapOfNoneRoundTripsThroughJSON(t *testing.T) {
	turns := 0
	none := []string{}
	raw, err := json.Marshal(Overrides{MaxTurns: &turns, Skills: &none}.Trim())
	require.NoError(t, err)
	var back Overrides
	require.NoError(t, json.Unmarshal(raw, &back))
	require.NotNil(t, back.MaxTurns)
	assert.Zero(t, *back.MaxTurns)
	require.NotNil(t, back.Skills)
	assert.Empty(t, *back.Skills)
	assert.Equal(t, []string{FieldSkills, FieldMaxTurns}, back.Fields())
}

func TestApplyDoesNotWriteIntoTheFileProfile(t *testing.T) {
	files := fileProfile(t)
	got, err := Apply(files, Overrides{DisplayName: "Bot"})
	require.NoError(t, err)
	assert.Equal(t, "Bot", got.DisplayName)
	assert.Equal(t, "Podium", files.DisplayName)
}

// Live is what makes a change reach a running conductor: readers take Current() per use, so
// a swap is the whole of the reload.
func TestLiveSwapsTheProfileEveryLaterReaderSees(t *testing.T) {
	files := fileProfile(t)
	live := NewLive(files)
	assert.Same(t, files, live.Files())
	assert.Equal(t, []string{"general"}, live.Current().PlaybookNames())

	next, err := Apply(files, Overrides{DisplayName: "Bot"})
	require.NoError(t, err)
	live.Set(next)

	assert.Equal(t, "Bot", live.Current().DisplayName)
	assert.Same(t, files, live.Files(), "the file half never changes while the process runs")

	// A turn takes a Playbook by value, so a swap cannot change one that is already in flight.
	inFlight := live.Current().Playbooks["general"]
	afterFiles := fileProfile(t)
	pb := afterFiles.Playbooks["general"]
	pb.Image = "example.invalid/other:dev"
	afterFiles.Playbooks["general"] = pb
	after, err := Apply(afterFiles, Overrides{})
	require.NoError(t, err)
	live.Set(after)
	assert.Equal(t, "podium-agent-runtime:dev", inFlight.Image)
}

func TestLiveIsSafeWhenThereIsNoProfileAtAll(t *testing.T) {
	var live *Live
	assert.Nil(t, live.Current())
	assert.Nil(t, live.Files())
	live.Set(&Profile{})
}
