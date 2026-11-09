package ghreview

import (
	"regexp"
	"strings"
)

// The review command is the ONE thing somebody who has not linked GitHub to Podium may ask
// this App for (docs/agent.md, "GitHub review commands").
//
//	@podium review
//	/review
//
// Matched case-insensitively at the start of a line. The mention is the App's own slug
// (with or without [bot]); "@podium" stands for whatever this App is called.
//
// Whatever follows the command on its line is ignored, not passed on: an unlinked person
// gets a review of the pull request and nothing else of theirs reaches the model as a request.
// There used to be a "focus" note here, cut, stripped, deny-listed and put to a model
// classifier before it was allowed near the review; dropping the note removed the only free
// text a stranger could aim at the model, and with it the classifier and its API key. What
// a stranger can still write — the pull request's title, description, diff and comments — is
// what any review reads, and the review is contained instead: the review playbook only, read
// only, the App's own credentials and nobody's secrets.

// slashReview is `/review`, alone or followed by whitespace and anything at all.
var slashReview = regexp.MustCompile(`(?i)^/review(?:\s.*)?$`)

// IsReviewCommand reports whether a comment body holds a review command. mention is the App's
// own mention pattern (compileMention), nil when the App's slug is unknown — then only the
// slash form is recognised.
func IsReviewCommand(body string, mention *regexp.Regexp) bool {
	var atReview *regexp.Regexp
	if mention != nil {
		// The mention pattern ends in a captured separator; anchoring it and requiring the
		// word review right after keeps "@podium-other review" and "@podium reviewer" out.
		atReview = regexp.MustCompile(`(?i)^` + strings.TrimSuffix(mentionCore(mention), `([^A-Za-z0-9_-]|$)`) +
			`\s+review(?:\s.*)?$`)
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if slashReview.MatchString(line) {
			return true
		}
		if atReview != nil && atReview.MatchString(line) {
			return true
		}
	}
	return false
}

// mentionCore is the mention pattern's source without its case-insensitivity flag, so it can
// be embedded in a larger anchored pattern.
func mentionCore(mention *regexp.Regexp) string {
	return strings.TrimPrefix(mention.String(), "(?i)")
}

// reviewRequestInstruction is what the review playbook is told when a commenter outside
// Podium asks for a review. It is fixed: nothing the requester wrote is in it.
func reviewRequestInstruction(author, prURL, commit, head string) string {
	who := firstNonEmpty(author, "someone")
	return followUpInstruction(who+" asked for a review of "+prURL+". They are not a Podium member, "+
		"so this is a review only: read the pull request and review it, and change nothing.", commit, head)
}
