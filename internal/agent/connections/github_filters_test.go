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
		"bad regexp":  {ExcludedBranches: []string{"/release-(/"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeGitHubFilters(f)
			require.Error(t, err)
		})
	}
}

func TestNormalizeGitHubFiltersKeepsBranches(t *testing.T) {
	got, err := NormalizeGitHubFilters(GitHubFilters{
		ExcludedBranches: []string{" dev ", "feature/*", "dev", "", "/^release-\\d+$/"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"dev", "feature/*", "/^release-\\d+$/"}, got.ExcludedBranches)
}

func TestBranchPatterns(t *testing.T) {
	cases := []struct {
		pattern, branch string
		want            bool
	}{
		{"dev", "dev", true},
		{"dev", "develop", false},
		{"dev", "Dev", false},
		{"feature/*", "feature/login", true},
		{"feature/*", "feature/login/ui", false},
		{"feature/**", "feature/login/ui", true},
		{"feature/*", "feature", false},
		{"release-?", "release-1", true},
		{"release-?", "release-10", false},
		{"v1.0", "v1x0", false},
		{"/^release-\\d+$/", "release-42", true},
		{"/^release-\\d+$/", "release-x", false},
		{"/hotfix/", "team/hotfix-1", true},
	}
	for _, tc := range cases {
		re, err := CompileBranchPattern(tc.pattern)
		require.NoError(t, err, tc.pattern)
		assert.Equal(t, tc.want, re.MatchString(tc.branch), "%s against %s", tc.pattern, tc.branch)
	}
}
