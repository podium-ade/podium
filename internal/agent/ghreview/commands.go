package ghreview

import (
	"regexp"
	"slices"
	"strings"
)

// A command is a comment on a pull request that asks the App for something by name
// (docs/agent.md, "Asked with a command"):
//
//	/review
//	@podium review
//
// at the start of a line, in any case. The mention is the App's own slug, with or without
// [bot]; "@podium" stands for whatever this App is called. Nothing here knows what any word
// means: a routine on the "pull_request.command" event names the words it answers to, and a
// comment no routine answers is an ordinary comment.

// slashCommand is `/<word>` at the start of a line, and the word.
var slashCommand = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9_-]*)(?:\s|$)`)

// Commands are the command words a comment asks with, lowercase, in the order written, each
// once. mention is the App's mention pattern (compileMention), nil when its slug is unknown,
// and then only the slash form is a command. A line inside a fenced code block is code, not
// a command: `/usr/bin` in a pasted log asks for nothing.
func Commands(body string, mention *regexp.Regexp) []string {
	var atCommand *regexp.Regexp
	if mention != nil {
		// The mention pattern ends in a captured separator; anchoring it and requiring a word
		// right after keeps "@podium-other review" out.
		atCommand = regexp.MustCompile(`(?i)^` + strings.TrimSuffix(mentionCore(mention), `([^A-Za-z0-9_-]|$)`) +
			`\s+([A-Za-z0-9][A-Za-z0-9_-]*)(?:\s|$)`)
	}
	var out []string
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		var m []string
		if m = slashCommand.FindStringSubmatch(line); m == nil && atCommand != nil {
			m = atCommand.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		if word := strings.ToLower(m[1]); !slices.Contains(out, word) {
			out = append(out, word)
		}
	}
	return out
}

// mentionCore is the mention pattern's source without its case-insensitivity flag, so it can
// be embedded in a larger anchored pattern.
func mentionCore(mention *regexp.Regexp) string {
	return strings.TrimPrefix(mention.String(), "(?i)")
}
