package connections

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGitHubFiltersTrimsAndDedupes(t *testing.T) {
	got, err := NormalizeGitHubFilters(GitHubFilters{
		AllowedActors:  []string{" @alice ", "Alice", "", "dependabot[bot]"},
		TriggerPhrases: []string{"  /review ", "/REVIEW", "take   a\tlook", " "},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "dependabot[bot]"}, got.AllowedActors)
	assert.Equal(t, []string{"/review", "take a look"}, got.TriggerPhrases)
	assert.Equal(t, ReplyBotThreads, got.ReplyMode, "an empty mode is the default")
}

func TestNormalizeGitHubFiltersRefuses(t *testing.T) {
	for name, f := range map[string]GitHubFilters{
		"bad login":   {AllowedActors: []string{"not a login"}},
		"bad ignored": {IgnoredActors: []string{"-dash"}},
		"long phrase": {TriggerPhrases: []string{strings.Repeat("x", maxPhraseRunes+1)}},
		"bad mode":    {ReplyMode: "sometimes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeGitHubFilters(f)
			require.Error(t, err)
		})
	}
}
