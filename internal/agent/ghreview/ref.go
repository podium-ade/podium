package ghreview

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// The kinds a Ref may name. issue_comment and review_comment are GitHub webhook types;
// review is a submitted pull-request review body; slack is a mention forwarded from Slack.
const (
	KindIssueComment  = "issue_comment"
	KindReviewComment = "review_comment"
	KindReview        = "review"
	KindSlack         = "slack"
)

// Parsed is one Ref, split. Slack fields are empty when the trigger was GitHub.
type Parsed struct {
	Owner       string
	Repo        string
	Number      int
	InstallID   int64
	Kind        string
	CommentID   int64
	SlackChan   string
	SlackThread string
	SlackTS     string
}

// SourceKey is a session's identity: the pull request, for the life of the review.
func SourceKey(owner, repo string, number int) string {
	return fmt.Sprintf("%s:%s/%s#%d", Kind, owner, repo, number)
}

// ParseSourceKey splits github:owner/repo#N. False for anything else.
func ParseSourceKey(key string) (owner, repo string, number int, ok bool) {
	rest, found := strings.CutPrefix(key, Kind+":")
	if !found {
		return "", "", 0, false
	}
	owner, rest, found = strings.Cut(rest, "/")
	if !found || owner == "" {
		return "", "", 0, false
	}
	repo, num, found := strings.Cut(rest, "#")
	if !found || repo == "" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	return owner, repo, n, true
}

// SourceKeyPR is SourceKey from a parsed pull request.
func SourceKeyPR(pr conductor.PullRequest) string {
	return SourceKey(pr.Owner, pr.Repo, pr.Number)
}

// SlackSurfaceRef is what review_surfaces.ref stores for a Slack thread: channel/thread,
// never the triggering message, because the bind is of the conversation.
func SlackSurfaceRef(channel, thread string) string {
	return channel + "/" + thread
}

// Ref is what the conductor carries for one turn. Six parts name the PR and the triggering
// GitHub comment; an optional /slack/channel/thread/ts suffix is how a Slack-originated
// turn still knows which message to react on.
func Ref(p Parsed) string {
	base := strings.Join([]string{
		p.Owner, p.Repo, strconv.Itoa(p.Number), strconv.FormatInt(p.InstallID, 10),
		p.Kind, strconv.FormatInt(p.CommentID, 10),
	}, "/")
	if p.SlackChan == "" || p.SlackThread == "" || p.SlackTS == "" {
		return base
	}
	return base + "/slack/" + p.SlackChan + "/" + p.SlackThread + "/" + p.SlackTS
}

// ParseRef splits a Ref.
func ParseRef(ref string) (Parsed, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 6 && len(parts) != 10 {
		return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
	}
	if parts[0] == "" || parts[1] == "" || parts[4] == "" {
		return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
	}
	num, err := strconv.Atoi(parts[2])
	if err != nil || num <= 0 {
		return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
	}
	install, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || install < 0 {
		return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
	}
	comment, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil || comment < 0 {
		return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
	}
	p := Parsed{
		Owner: parts[0], Repo: parts[1], Number: num, InstallID: install,
		Kind: parts[4], CommentID: comment,
	}
	if len(parts) == 10 {
		if parts[6] != KindSlack || parts[7] == "" || parts[8] == "" || parts[9] == "" {
			return Parsed{}, fmt.Errorf("github: %q is not a pull-request ref", ref)
		}
		p.SlackChan, p.SlackThread, p.SlackTS = parts[7], parts[8], parts[9]
	}
	return p, nil
}

// PRURL is the canonical pull-request URL the rest of Podium already stores.
func PRURL(owner, repo string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, number)
}
