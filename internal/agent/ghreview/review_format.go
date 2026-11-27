package ghreview

import (
	"strconv"
	"strings"
)

// threadNote is a reply to one of the review's inline threads, from an answer that is not
// the review: "fixed in abc123" under the finding it fixed. Resolve also resolves the thread.
type threadNote struct {
	Path    string
	Line    int
	Body    string
	Resolve bool
}

// splitThreadNotes pulls ```reply and ```resolved fences out of an answer. Each names the
// finding's path:line on its first line, as the review's own inline note did, and the rest is
// the reply. A fence that does not start with path:line stays in the answer.
func splitThreadNotes(body string) (string, []threadNote) {
	var notes []threadNote
	rest := replaceFences(body, []string{"reply", "resolved"}, func(kind, inner string) bool {
		inline, ok := parseInline(inner)
		if !ok {
			return false
		}
		notes = append(notes, threadNote{
			Path: inline.Path, Line: inline.Line, Body: inline.Body, Resolve: kind == "resolved",
		})
		return true
	})
	return strings.TrimSpace(rest), notes
}

// withThreadNotes keeps replies whose thread was not found in the answer, under the finding
// they name, so a reply never disappears.
func withThreadNotes(answer string, notes []threadNote) string {
	var b strings.Builder
	b.WriteString(answer)
	for _, n := range notes {
		b.WriteString("\n\n**")
		b.WriteString(n.Path)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(n.Line))
		b.WriteString("**\n\n")
		b.WriteString(n.Body)
	}
	return strings.TrimSpace(b.String())
}

// inlineNote is a parsed ```inline note: a place in the diff and what is said there.
type inlineNote struct {
	Path      string
	Line      int
	StartLine int
	Body      string
}

// replaceFences finds the fences whose info string is one of kinds and hands each one's
// inside to take; a fence take accepts is cut out of body, and one it refuses stays.
//
// It reads lines rather than matching a pattern, because a note quotes code: an ```inline
// fence holding a ```go block must end at its own closing fence, not at the first three
// backticks inside it. So, as in CommonMark, a fence closes only on a bare line of at least
// as many backticks as opened it, and a fence that opens with an info string inside it
// (```go) is counted and closed before the note's own can be. A nested fence with no info
// string is indistinguishable from the note's close; an outer fence of four backticks is how
// a note quotes one.
func replaceFences(body string, kinds []string, take func(kind, inner string) bool) string {
	lines := strings.SplitAfter(body, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		n, info := fenceLine(lines[i])
		if n == 0 || !contains(kinds, info) {
			out.WriteString(lines[i])
			continue
		}
		end, depth := -1, 0
		for j := i + 1; j < len(lines) && end < 0; j++ {
			m, inner := fenceLine(lines[j])
			switch {
			case m == 0:
			case inner != "":
				depth++
			case depth > 0:
				depth--
			case m >= n:
				end = j
			}
		}
		if end < 0 || !take(info, strings.Join(lines[i+1:end], "")) {
			out.WriteString(lines[i])
			continue
		}
		i = end
	}
	return out.String()
}

// fenceLine is a line's backtick fence: how many backticks open it, and its info string. 0
// when the line is not a fence.
func fenceLine(line string) (int, string) {
	t := strings.TrimSpace(line)
	n := len(t) - len(strings.TrimLeft(t, "`"))
	if n < 3 {
		return 0, ""
	}
	info := strings.TrimSpace(t[n:])
	if strings.Contains(info, "`") {
		return 0, ""
	}
	return n, info
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
