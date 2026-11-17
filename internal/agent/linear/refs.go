package linear

// Issue references in what people write (docs/agent.md, "Tickets in a chat"): "EME-6", or a
// linear.app link to it. A chat draws each one it can show as a chip, and the assistant is given
// the tickets a person names. Which of the candidates are tickets at all is decided against the
// workspace's team keys, which is what keeps UTF-8 and ISO-8601 from being read as issues.

import (
	"regexp"
	"strings"
)

// IssueRef is an issue as somebody named it: its identifier ("ENG-12"), or its Linear id when
// only that is known. Identifier is upper-case.
type IssueRef struct {
	Identifier string
	ID         string
}

// TeamKey is the team part of the identifier, "" for a ref by id.
func (r IssueRef) TeamKey() string {
	if i := strings.LastIndexByte(r.Identifier, '-'); i > 0 {
		return r.Identifier[:i]
	}
	return ""
}

// String is how the ref is asked for: the identifier, or the id.
func (r IssueRef) String() string {
	if r.Identifier != "" {
		return r.Identifier
	}
	return r.ID
}

// identifierPattern is a candidate identifier: an upper-case team key of up to ten letters and
// digits, starting with a letter, then a number with no leading zero. Linear's keys are
// upper-case and so are the identifiers people copy out of it; lower case is not matched, so
// "covid-19" is never a ticket of a team keyed COVID. What surrounds a match is checked in
// FindIssueRefs, because Go's regexp has no look-behind.
var identifierPattern = regexp.MustCompile(`[A-Z][A-Z0-9]{0,9}-[1-9][0-9]{0,6}`)

// issueURLPattern is a link to an issue in Linear: https://linear.app/<workspace>/issue/ENG-12,
// with or without the title slug after it.
var issueURLPattern = regexp.MustCompile(`(?i)https?://linear\.app/[a-z0-9_-]+/issue/([a-z][a-z0-9]{0,9}-[1-9][0-9]{0,6})\b`)

// uuidPattern is a Linear id, which is a UUID.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ParseIssueRef reads one ref as a client sends it: an identifier, a linear.app issue link, or
// a Linear id. ok is false for anything else.
func ParseIssueRef(s string) (IssueRef, bool) {
	s = strings.TrimSpace(s)
	if m := issueURLPattern.FindStringSubmatch(s); m != nil {
		return IssueRef{Identifier: strings.ToUpper(m[1])}, true
	}
	if uuidPattern.MatchString(s) {
		return IssueRef{ID: strings.ToLower(s)}, true
	}
	up := strings.ToUpper(s)
	if loc := identifierPattern.FindStringIndex(up); loc != nil && loc[0] == 0 && loc[1] == len(up) {
		return IssueRef{Identifier: up}, true
	}
	return IssueRef{}, false
}

// FindIssueRefs is the issues text names, in the order it first names them, each once, at most
// limit of them (limit <= 0 is no limit). known says whether a team key is one of the workspace's;
// a candidate of any other team is not an issue. A link to an issue counts as its identifier.
//
// A bare identifier counts only on its own: not inside a word, a path or a version ("v1-2",
// "/EME-6", "EME-6.1"), and not glued to another token by a hyphen ("EME-6-x"). A link's
// identifier is read from the link.
func FindIssueRefs(text string, known func(teamKey string) bool, limit int) []IssueRef {
	type found struct {
		at  int
		ref IssueRef
	}
	var all []found
	for _, m := range issueURLPattern.FindAllStringSubmatchIndex(text, -1) {
		all = append(all, found{at: m[0], ref: IssueRef{Identifier: strings.ToUpper(text[m[2]:m[3]])}})
	}
	for _, loc := range identifierPattern.FindAllStringIndex(text, -1) {
		if !standsAlone(text, loc[0], loc[1]) {
			continue
		}
		all = append(all, found{at: loc[0], ref: IssueRef{Identifier: text[loc[0]:loc[1]]}})
	}
	// In the order the text names them: links and bare identifiers were found apart.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].at < all[j-1].at; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	var out []IssueRef
	seen := map[string]bool{}
	for _, f := range all {
		if seen[f.ref.Identifier] || known == nil || !known(f.ref.TeamKey()) {
			continue
		}
		seen[f.ref.Identifier] = true
		out = append(out, f.ref)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// standsAlone reports whether text[start:end] is a token of its own: nothing word-like, and no
// path, version or hyphenated continuation, touching it.
func standsAlone(text string, start, end int) bool {
	if start > 0 {
		switch b := text[start-1]; {
		case isWordByte(b), b == '-', b == '/', b == '.', b == '@', b == '#', b == '=':
			return false
		}
	}
	if end < len(text) {
		b := text[end]
		switch {
		case isWordByte(b):
			return false
		case (b == '-' || b == '.' || b == '/') && end+1 < len(text) && isWordByte(text[end+1]):
			return false
		}
	}
	return true
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}
