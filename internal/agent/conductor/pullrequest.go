package conductor

import (
	"regexp"
	"strconv"
	"strings"
)

// PullRequest is one pull request, described by nothing but its own URL.
//
// There is no title and no state here on purpose. Learning either means calling GitHub, and
// that would couple the conductor to a vendor for a convenience feature. Owner, Repo and
// Number are enough to render owner/repo#number, which is what a human scanning a chat reads.
//
// On the personal-access-token path the conductor cannot make the call at all:
// podium.agent.github_token is a secret attached to TASKS, so this process cannot read it.
// With a GitHub App configured it could — gitcred.go mints from a key on this host — and
// deliberately does not. The reason above is the reason, not the missing credential.
type PullRequest struct {
	// URL is canonical — https://github.com/<owner>/<repo>/pull/<number> — whatever the
	// text actually said. It is the identity of a link, so /pull/12/files, /pull/12 and
	// /pull/12#discussion_r1 are one pull request rather than three.
	URL    string
	Owner  string
	Repo   string
	Number int
}

// maxPullRequestNumberDigits is what stops a run of digits that is not a pull-request
// number from being parsed as one. GitHub's largest is five digits; nine is generous and
// keeps the value inside the int32 the column stores.
const maxPullRequestNumberDigits = 9

// pullRequestRE matches a GitHub pull-request URL wherever it appears — bare, inside a
// markdown link, or in angle brackets.
//
// It is deliberately the WHOLE URL and nothing looser. A bare "#123" is ambiguous across
// repositories, and an issue, a commit and a pull request on another host all look alike
// once you stop reading the path, so /pull/ on github.com is the only shape that says
// exactly one thing.
var pullRequestRE = regexp.MustCompile(
	`https://github\.com/([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9_.-]+)/pull/([0-9]+)`)

// FindPullRequests returns every pull request named in text, in the order it named them,
// each one once. A turn that mentions the pull request it opened three times has opened
// one pull request.
func FindPullRequests(text string) []PullRequest {
	var out []PullRequest
	seen := map[string]bool{}
	for _, m := range pullRequestRE.FindAllStringSubmatch(text, -1) {
		pr, ok := pullRequestFrom(m)
		if !ok || seen[pr.URL] {
			continue
		}
		seen[pr.URL] = true
		out = append(out, pr)
	}
	return out
}

// ParsePullRequestURL reads one pull-request URL, which is what a human hands the manual
// attach. Anything else — an issue, a commit, a repository, another host — is not one.
func ParsePullRequestURL(raw string) (PullRequest, bool) {
	raw = strings.TrimSpace(raw)
	loc := pullRequestRE.FindStringSubmatchIndex(raw)
	// The match has to start at the beginning and run to the end of the URL: the caller
	// gave a URL, not a sentence with one in it. What may follow is what a browser's
	// address bar adds — /files, ?w=1, #discussion — and a trailing slash.
	if loc == nil || loc[0] != 0 {
		return PullRequest{}, false
	}
	if rest := raw[loc[1]:]; rest != "" && !strings.ContainsAny(rest[:1], "/?#") {
		return PullRequest{}, false
	}
	m := make([]string, 0, 4)
	for i := 0; i < len(loc); i += 2 {
		m = append(m, raw[loc[i]:loc[i+1]])
	}
	return pullRequestFrom(m)
}

// pullRequestFrom turns one regexp match into a canonical PullRequest.
//
// The URL is REBUILT from the captures rather than copied out of the text, which is what
// makes it safe to render as a link. Owner and repo come from character classes with no
// "/", ":" or quote in them and the number is digits, so what comes out is an https URL on
// github.com and cannot be anything else — whatever a task wrote in its answer.
func pullRequestFrom(m []string) (PullRequest, bool) {
	if len(m) != 4 || len(m[3]) > maxPullRequestNumberDigits {
		return PullRequest{}, false
	}
	number, err := strconv.Atoi(m[3])
	if err != nil || number <= 0 {
		return PullRequest{}, false
	}
	owner, repo := m[1], m[2]
	return PullRequest{
		URL:    "https://github.com/" + owner + "/" + repo + "/pull/" + strconv.Itoa(number),
		Owner:  owner,
		Repo:   repo,
		Number: number,
	}, true
}
