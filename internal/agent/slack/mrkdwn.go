package slack

import (
	"regexp"
	"strings"
)

// Slack does not speak Markdown. Its own dialect, mrkdwn, writes bold as *one* pair of
// asterisks, a link as <url|text> and has no headings at all — so the Markdown a model
// writes arrives in the thread as literal punctuation: `**Answer:**` is read by a human as
// two asterisks, a word and two more.
//
// The conversion belongs HERE and not in a prompt. The same final text is posted to Slack
// and mirrored into the web chat, and the web chat renders Markdown; telling the model to
// write mrkdwn would fix one surface by breaking the other. A model writes Markdown once,
// and each surface is handed the dialect it reads.
//
// Deliberately small. It converts what a report actually contains — bold, links, headings —
// and leaves everything else alone, because a converter that rewrites more than it
// understands corrupts the one thing nobody can afford to have corrupted: the evidence.
var (
	// **bold** and __bold__ both mean bold in Markdown; mrkdwn spells it *bold*. Bounded to
	// one line so an unclosed pair cannot swallow the rest of the message.
	boldStarRE  = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	boldScoreRE = regexp.MustCompile(`__([^_\n]+)__`)
	// [text](url) -> <url|text>. The url may not contain spaces or a closing paren, which
	// is what keeps this from matching prose that merely has brackets near parentheses.
	linkRE = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	// Slack has no headings. A heading becomes a bold line, which is what it was for.
	headingRE = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.+?)[ \t]*$`)
)

// toMrkdwn rewrites Markdown as Slack mrkdwn, leaving code alone.
//
// Single-asterisk *text* is NOT touched. Markdown reads it as italic and mrkdwn reads it as
// bold, so it renders as emphasis either way; rewriting it would mean deciding which the
// author meant, and getting that wrong is worse than a word in the wrong weight.
func toMrkdwn(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, seg := range splitCode(s) {
		if seg.code {
			// A fenced block or an inline span. Slack renders both natively, and their
			// contents are a query or a stack trace: the one place a stray asterisk is
			// data rather than markup.
			b.WriteString(seg.text)
			continue
		}
		t := seg.text
		t = boldStarRE.ReplaceAllString(t, "*$1*")
		t = boldScoreRE.ReplaceAllString(t, "*$1*")
		t = linkRE.ReplaceAllString(t, "<$2|$1>")
		t = headingRE.ReplaceAllString(t, "*$1*")
		b.WriteString(t)
	}
	return b.String()
}

// segment is one run of the message, either code or not.
type segment struct {
	text string
	code bool
}

// splitCode cuts a message into code and not-code. A fence runs to its closing fence or, if
// there is none, to the end of the message — an unclosed fence is a model mid-sentence, and
// treating the remainder as code is the reading that cannot corrupt it.
func splitCode(s string) []segment {
	var out []segment
	plain := strings.Builder{}
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, segment{text: plain.String()})
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "```"):
			flush()
			end := strings.Index(s[i+3:], "```")
			if end < 0 {
				out = append(out, segment{text: s[i:], code: true})
				return out
			}
			stop := i + 3 + end + 3
			out = append(out, segment{text: s[i:stop], code: true})
			i = stop
		case s[i] == '`':
			// An inline span ends at the next backtick on the SAME line: a lone backtick in
			// prose then stays prose instead of eating the paragraph after it.
			rest := s[i+1:]
			nl := strings.IndexByte(rest, '\n')
			limit := len(rest)
			if nl >= 0 {
				limit = nl
			}
			end := strings.IndexByte(rest[:limit], '`')
			if end < 0 {
				plain.WriteByte(s[i])
				i++
				continue
			}
			flush()
			stop := i + 1 + end + 1
			out = append(out, segment{text: s[i:stop], code: true})
			i = stop
		default:
			plain.WriteByte(s[i])
			i++
		}
	}
	flush()
	return out
}
