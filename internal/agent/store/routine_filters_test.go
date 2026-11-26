package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestARoutinesGitHubConditionsTurnEventsAway(t *testing.T) {
	pr := func(actor, base string) GitHubEventFacts {
		return GitHubEventFacts{Actor: actor, Pull: true, Base: base}
	}
	for name, tc := range map[string]struct {
		f    RoutineGitHubFilters
		e    GitHubEventFacts
		want string // "" admits
	}{
		"nothing turns nothing away":           {RoutineGitHubFilters{}, GitHubEventFacts{}, ""},
		"an allowed person":                    {RoutineGitHubFilters{AllowedPeople: []string{"Alice"}}, pr("@alice", "main"), ""},
		"anybody else":                         {RoutineGitHubFilters{AllowedPeople: []string{"alice"}}, pr("bob", "main"), "person not allowed"},
		"nobody known, while some are allowed": {RoutineGitHubFilters{AllowedPeople: []string{"alice"}}, pr("", "main"), "person not allowed"},
		"an ignored person":                    {RoutineGitHubFilters{IgnoredPeople: []string{"bob"}}, pr("Bob", "main"), "ignored person"},
		"ignored wins over allowed": {
			RoutineGitHubFilters{AllowedPeople: []string{"bob"}, IgnoredPeople: []string{"bob"}}, pr("bob", "main"), "ignored person",
		},
		"a bot":                {RoutineGitHubFilters{IgnoreBots: true}, GitHubEventFacts{Actor: "renovate", ActorBot: true}, "bot account"},
		"an App's account":     {RoutineGitHubFilters{IgnoreBots: true}, GitHubEventFacts{Actor: "dependabot[bot]"}, "bot account"},
		"a draft":              {RoutineGitHubFilters{SkipDrafts: true}, GitHubEventFacts{Pull: true, Draft: true}, "draft"},
		"a draft, not skipped": {RoutineGitHubFilters{}, GitHubEventFacts{Pull: true, Draft: true}, ""},
		"an excluded base":     {RoutineGitHubFilters{ExcludedBranches: []string{"release/*"}}, pr("a", "release/1"), "excluded branch"},
		"a glob stays in its segment": {
			RoutineGitHubFilters{ExcludedBranches: []string{"release/*"}}, pr("a", "release/1/hotfix"), "",
		},
		"a regular expression": {RoutineGitHubFilters{ExcludedBranches: []string{"/^hotfix-/"}}, pr("a", "hotfix-12"), "excluded branch"},
		"an unknown base, while branches are excluded": {
			RoutineGitHubFilters{ExcludedBranches: []string{"main"}}, pr("a", ""), "base branch unknown",
		},
		"no pull request, no branch to exclude": {
			RoutineGitHubFilters{ExcludedBranches: []string{"main"}, SkipDrafts: true}, GitHubEventFacts{Actor: "a"}, "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ok, why := tc.f.Admits(tc.e)
			assert.Equal(t, tc.want == "", ok)
			assert.Equal(t, tc.want, why)
		})
	}
}

func TestARoutinesPlaybookAndConditionsAreChecked(t *testing.T) {
	github := RoutineDraft{
		Name: "Review", Kind: RoutineKindGitHub, Events: []string{"pull_request.opened"}, Timezone: "UTC",
		Instruction: "Review it.", Playbook: " " + RoutineReviewPlaybook + " ",
		GitHubFilters: RoutineGitHubFilters{AllowedPeople: []string{"@alice", "Alice", " bob "}, ExcludedBranches: []string{" main ", "main"}},
	}
	d, err := PrepareRoutine(github)
	require.NoError(t, err)
	assert.Equal(t, RoutineReviewPlaybook, d.Playbook)
	assert.Equal(t, []string{"alice", "bob"}, d.GitHubFilters.AllowedPeople)
	assert.Equal(t, []string{"main"}, d.GitHubFilters.ExcludedBranches)

	bad := github
	bad.GitHubFilters = RoutineGitHubFilters{IgnoredPeople: []string{"not a login"}}
	_, err = PrepareRoutine(bad)
	require.ErrorIs(t, err, ErrInvalidRoutine)
	bad.GitHubFilters = RoutineGitHubFilters{ExcludedBranches: []string{"/(/"}}
	_, err = PrepareRoutine(bad)
	require.ErrorIs(t, err, ErrInvalidRoutine)

	schedule := RoutineDraft{
		Name: "Digest", Schedule: "0 9 * * *", Timezone: "UTC", Instruction: "Sum up.",
		NextRunAt: time.Now().Add(time.Hour), Playbook: "triage",
	}
	d, err = PrepareRoutine(schedule)
	require.NoError(t, err, "any routine may start a playbook")
	assert.Equal(t, "triage", d.Playbook)
	schedule.Playbook = RoutineReviewPlaybook
	_, err = PrepareRoutine(schedule)
	require.ErrorIs(t, err, ErrInvalidRoutine, "only a routine on GitHub events knows a pull request's repository")
	schedule.Playbook = ""
	schedule.GitHubFilters = RoutineGitHubFilters{SkipDrafts: true}
	_, err = PrepareRoutine(schedule)
	require.ErrorIs(t, err, ErrInvalidRoutine, "conditions are a routine on GitHub events'")
}

func TestAStoredConditionReadsBack(t *testing.T) {
	f := RoutineGitHubFilters{AllowedPeople: []string{"alice"}, IgnoreBots: true, SkipDrafts: true}
	assert.Equal(t, f, githubFiltersFrom(githubFiltersJSON(f)))
	assert.Equal(t, `{}`, string(githubFiltersJSON(RoutineGitHubFilters{})))
	assert.True(t, githubFiltersFrom(nil).IsZero())
	assert.True(t, githubFiltersFrom([]byte("not json")).IsZero())
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
