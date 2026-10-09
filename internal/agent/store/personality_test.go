package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

func TestPreparePersonalityAcceptsATrimmedVoice(t *testing.T) {
	got, err := PreparePersonality(PersonalityDraft{
		Name:         "  whatever the client sent ",
		DisplayName:  "  Night   Owl  ",
		Instructions: "  Speak briefly.\nPrefer examples.  ",
		Agent:        " grok ",
		Model:        " grok-4.6 ",
		Effort:       " low ",
	})
	require.NoError(t, err)
	assert.Empty(t, got.Name)
	assert.Equal(t, "Night Owl", got.DisplayName)
	assert.Equal(t, "Speak briefly.\nPrefer examples.", got.Instructions)
	assert.Equal(t, "grok", got.Agent)
	assert.Equal(t, "grok-4.6", got.Model)
	assert.Equal(t, "low", got.Effort)
}

func TestPersonalityNameFromDisplay(t *testing.T) {
	long := strings.Repeat("a", 32)
	cases := []struct {
		display string
		taken   []string
		want    string
	}{
		{display: "Night Owl", want: "night-owl"},
		{display: "  Night   Owl  ", want: "night-owl"},
		{display: "NightOwl", want: "nightowl"},
		{display: "Podium", want: "podium-2"},
		{display: "podium", want: "podium-2"},
		{display: "Podium", taken: []string{"podium-2"}, want: "podium-3"},
		{display: "1owl", want: "a-1owl"},
		{display: "Émile", want: "mile"},
		{display: "日本語", want: "assistant"},
		{display: "!!!", want: "assistant"},
		{display: "", want: "assistant"},
		{display: "Night Owl", taken: []string{"night-owl"}, want: "night-owl-2"},
		{display: strings.Repeat("A", 40), want: long},
		{display: long, taken: []string{long}, want: strings.Repeat("a", 30) + "-2"},
		{display: strings.Repeat("a", 31) + " b", want: strings.Repeat("a", 31)},
	}
	for _, tc := range cases {
		t.Run(tc.display, func(t *testing.T) {
			got, err := PersonalityName(tc.display, tc.taken)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Regexp(t, profiles.NameRE, got)
			assert.NotEqual(t, reservedPersonalityName, got)
			assert.LessOrEqual(t, len(got), maxPersonalityNameLen)
		})
	}
}

func TestPreparePersonalityRefusesABadDisplayName(t *testing.T) {
	_, err := PreparePersonality(PersonalityDraft{Name: "owl", DisplayName: "  ", Instructions: "Be brief."})
	require.ErrorIs(t, err, ErrInvalidPersonality)
	assert.Contains(t, err.Error(), "display name is required")

	_, err = PreparePersonality(PersonalityDraft{
		Name: "owl", DisplayName: "Ow\u0000l", Instructions: "Be brief.",
	})
	require.ErrorIs(t, err, ErrInvalidPersonality)
	assert.Contains(t, err.Error(), "control")

	ok := strings.Repeat("é", MaxChatTitleRunes)
	got, err := PreparePersonality(PersonalityDraft{Name: "owl", DisplayName: ok, Instructions: "Be brief."})
	require.NoError(t, err)
	assert.Equal(t, ok, got.DisplayName)

	_, err = PreparePersonality(PersonalityDraft{
		Name: "owl", DisplayName: ok + "é", Instructions: "Be brief.",
	})
	require.ErrorIs(t, err, ErrInvalidPersonality)
	assert.Contains(t, err.Error(), "display name")
}

func TestPreparePersonalityCapsInstructionsInRunes(t *testing.T) {
	_, err := PreparePersonality(PersonalityDraft{Name: "owl", DisplayName: "Owl", Instructions: "   "})
	require.ErrorIs(t, err, ErrInvalidPersonality)
	assert.Contains(t, err.Error(), "instructions are required")

	ok := strings.Repeat("é", maxPersonalityInstructionRunes)
	got, err := PreparePersonality(PersonalityDraft{Name: "owl", DisplayName: "Owl", Instructions: ok})
	require.NoError(t, err)
	assert.Equal(t, maxPersonalityInstructionRunes, len([]rune(got.Instructions)))

	_, err = PreparePersonality(PersonalityDraft{
		Name: "owl", DisplayName: "Owl", Instructions: ok + "é",
	})
	require.ErrorIs(t, err, ErrInvalidPersonality)
	assert.Contains(t, err.Error(), "instructions")
}
