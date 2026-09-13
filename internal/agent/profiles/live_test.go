package profiles

import (
	"testing"

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
