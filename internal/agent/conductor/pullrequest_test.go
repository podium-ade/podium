package conductor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestATurnsAnswerNamesThePullRequestItOpened(t *testing.T) {
	found := FindPullRequests(
		"Done. I opened https://github.com/podium-ade/podium/pull/41 with the fix.")

	require.Len(t, found, 1)
	assert.Equal(t, PullRequest{
		URL:    "https://github.com/podium-ade/podium/pull/41",
		Owner:  "podium-ade",
		Repo:   "podium",
		Number: 41,
	}, found[0])
}

func TestTheSamePullRequestSaidThreeWaysIsOnePullRequest(t *testing.T) {
	// Bare, as a markdown link, and with the /files a browser was on. All one thing: the
	// URL that is stored is canonical, so the trailing path never makes a second link.
	found := FindPullRequests(strings.Join([]string{
		"Opened https://github.com/podium-ade/podium/pull/41.",
		"See [#41](https://github.com/podium-ade/podium/pull/41) for the diff,",
		"and <https://github.com/podium-ade/podium/pull/41/files> for the files.",
	}, "\n"))

	require.Len(t, found, 1)
	assert.Equal(t, "https://github.com/podium-ade/podium/pull/41", found[0].URL)
}

func TestAnAnswerThatNamesNoPullRequestLinksNone(t *testing.T) {
	assert.Empty(t, FindPullRequests(
		"The ETL failed because the source table was empty. Nothing to open a PR about."))
	assert.Empty(t, FindPullRequests(""))
}

func TestWhatLooksLikeAPullRequestAndIsNot(t *testing.T) {
	// Every one of these is a URL a turn genuinely says. An issue is not a pull request, a
	// commit is not a pull request, a repository is not a pull request, and a pull request
	// on some other host is not one this feature can render.
	for _, text := range []string{
		"https://github.com/podium-ade/podium/issues/41",
		"https://github.com/podium-ade/podium/commit/aa519fc",
		"https://github.com/podium-ade/podium",
		"https://github.com/podium-ade/podium/pulls",
		"https://gitlab.com/podium-ade/podium/pull/41",
		"https://github.example.com/podium-ade/podium/pull/41",
		"https://github.com/podium-ade/podium/pull/",
		"https://github.com/podium-ade/podium/pull/0",
		// #41 on its own means nothing without a repository to read it against, which is
		// exactly why bare references are not matched.
		"I pushed the fix to #41.",
	} {
		assert.Empty(t, FindPullRequests(text), "should not be a pull request: %s", text)
	}
}

func TestSeveralPullRequestsArriveInTheOrderTheyWereNamed(t *testing.T) {
	found := FindPullRequests(strings.Join([]string{
		"Split into two: https://github.com/acme/api/pull/7 first,",
		"then https://github.com/acme/web/pull/12 on top of it.",
	}, "\n"))

	require.Len(t, found, 2)
	assert.Equal(t, "acme/api", found[0].Owner+"/"+found[0].Repo)
	assert.Equal(t, 7, found[0].Number)
	assert.Equal(t, "acme/web", found[1].Owner+"/"+found[1].Repo)
	assert.Equal(t, 12, found[1].Number)
}

func TestARunOfDigitsThatCannotBeAPullRequestNumber(t *testing.T) {
	// Nine digits is the cap, and it is what keeps the value inside the int32 the column
	// stores rather than silently truncating something that was never a number anyway.
	assert.Empty(t, FindPullRequests("https://github.com/acme/api/pull/1234567890123"))
	require.Len(t, FindPullRequests("https://github.com/acme/api/pull/999999999"), 1)
}

func TestParsingTheURLAHumanPasted(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/acme/api/pull/7",
		"  https://github.com/acme/api/pull/7  ",
		"https://github.com/acme/api/pull/7/files",
		"https://github.com/acme/api/pull/7#discussion_r1",
		"https://github.com/acme/api/pull/7?w=1",
		"https://github.com/acme/api/pull/7/",
	} {
		pr, ok := ParsePullRequestURL(raw)
		require.True(t, ok, "should parse: %q", raw)
		assert.Equal(t, "https://github.com/acme/api/pull/7", pr.URL,
			"whatever was pasted, the link stored is canonical")
	}

	for _, raw := range []string{
		"",
		"acme/api#7",
		"https://github.com/acme/api/issues/7",
		"https://github.com/acme/api/pull/7x",
		"see https://github.com/acme/api/pull/7",
		"https://gitlab.com/acme/api/pull/7",
	} {
		_, ok := ParsePullRequestURL(raw)
		assert.False(t, ok, "should not parse: %q", raw)
	}
}
