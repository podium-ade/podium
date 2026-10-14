package ghreview

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	eventComment        = "COMMENT"
	eventApprove        = "APPROVE"
	eventRequestChanges = "REQUEST_CHANGES"
)

// pullComment is one inline note on a submitted review. Side is the new file:
// a review of a diff reads the result, not the lines that were deleted.
type pullComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// inlineNote is a parsed note before it becomes a pullComment.
type inlineNote struct {
	Path      string
	Line      int
	StartLine int
	Body      string
}

var inlineFence = regexp.MustCompile("(?s)```inline[ \t]*\r?\n(.*?)```")

// reviewEvent is the GitHub event for a final answer. The playbook's first
// line is the verdict. Approve, and a review that is only nits, submit an
// approval. Anything else stays a comment, which is also the fail-safe when
// the line is missing: a review must not approve by accident.
func reviewEvent(body string) string {
	switch verdictLine(body) {
	case "approve", "approved", "nit", "nits", "nits only", "only nits":
		return eventApprove
	default:
		return eventComment
	}
}

func verdictLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.Trim(line, "*_`")
		line = strings.TrimSpace(line)
		line = strings.TrimRight(line, ".:!")
		return strings.ToLower(line)
	}
	return ""
}

// splitInline pulls ```inline fences out of the review. Each fence is one
// comment on the diff. A fence that does not start with path:line stays in
// the summary, so a malformed note is still part of the review instead of
// disappearing. The summary is what GitHub shows above the inline comments.
func splitInline(body string) (string, []inlineNote) {
	var notes []inlineNote
	summary := inlineFence.ReplaceAllStringFunc(body, func(block string) string {
		m := inlineFence.FindStringSubmatch(block)
		if len(m) < 2 {
			return block
		}
		note, ok := parseInline(m[1])
		if !ok {
			return block
		}
		notes = append(notes, note)
		return ""
	})
	return strings.TrimSpace(summary), notes
}

func parseInline(inner string) (inlineNote, bool) {
	inner = strings.TrimSpace(inner)
	loc, rest, found := strings.Cut(inner, "\n")
	if !found {
		return inlineNote{}, false
	}
	path, start, end, ok := parseLoc(strings.TrimSpace(loc))
	body := strings.TrimSpace(rest)
	if !ok || body == "" {
		return inlineNote{}, false
	}
	note := inlineNote{Path: path, Line: end, Body: body}
	if start != end {
		note.StartLine = start
	}
	return note, true
}

func parseLoc(s string) (path string, start, end int, ok bool) {
	colon := strings.LastIndex(s, ":")
	if colon <= 0 || colon == len(s)-1 {
		return "", 0, 0, false
	}
	path = s[:colon]
	if path == "" || strings.ContainsAny(path, " \t") {
		return "", 0, 0, false
	}
	span := s[colon+1:]
	if a, b, found := strings.Cut(span, "-"); found {
		start, err1 := strconv.Atoi(a)
		end, err2 := strconv.Atoi(b)
		if err1 != nil || err2 != nil || start <= 0 || end < start {
			return "", 0, 0, false
		}
		return path, start, end, true
	}
	n, err := strconv.Atoi(span)
	if err != nil || n <= 0 {
		return "", 0, 0, false
	}
	return path, n, n, true
}

func (n inlineNote) comment() pullComment {
	c := pullComment{Path: n.Path, Line: n.Line, Side: "RIGHT", Body: n.Body}
	if n.StartLine > 0 {
		c.StartLine = n.StartLine
		c.StartSide = "RIGHT"
	}
	return c
}

func commentsOf(notes []inlineNote) []pullComment {
	out := make([]pullComment, len(notes))
	for i, n := range notes {
		out[i] = n.comment()
	}
	return out
}

// summaryWithNotes keeps notes that GitHub would not attach to the diff, so a
// rejected line does not vanish from the review.
func summaryWithNotes(summary string, notes []inlineNote) string {
	var b strings.Builder
	b.WriteString(summary)
	for _, n := range notes {
		b.WriteString("\n\n**")
		b.WriteString(n.Path)
		b.WriteByte(':')
		if n.StartLine > 0 {
			b.WriteString(strconv.Itoa(n.StartLine))
			b.WriteByte('-')
		}
		b.WriteString(strconv.Itoa(n.Line))
		b.WriteString("**\n\n")
		b.WriteString(n.Body)
	}
	return strings.TrimSpace(b.String())
}
