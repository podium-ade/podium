package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// MaxTitleRunes caps a generated chat title. Long enough for a short phrase, short enough
// that the rail does not wrap.
const MaxTitleRunes = 48

// ChatTitleArtifact is the file a first chat turn writes so the conductor can name the
// conversation from the model rather than from a truncation of the query. It is runtime-
// owned: it is never attached as a download.
const ChatTitleArtifact = "chat-title.txt"

// TitleFromQuery is the name a chat gets the moment its first message lands, before the
// turn's model has had a chance to write ChatTitleArtifact. It is the first line of the
// query, playbook prefix stripped, bounded — not a model, so a conductor with no credential
// still leaves "New chat" behind.
func TitleFromQuery(query string) string {
	q := strings.TrimSpace(query)
	if m := profiles.PlaybookPrefixRE.FindStringSubmatch(q); m != nil {
		q = strings.TrimSpace(q[len(m[0]):])
	}
	if i := strings.IndexByte(q, '\n'); i >= 0 {
		q = strings.TrimSpace(q[:i])
	}
	return clampTitle(q)
}

// SanitizeTitle is what a model-written title has to survive before it is stored. One line,
// no quotes wrapping the whole thing, no control characters, the same rune cap as a query
// title. Empty means the model wrote nothing we can use.
func SanitizeTitle(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'“”‘’`)
	s = strings.TrimRight(strings.TrimSpace(s), ".,;:!?")
	return clampTitle(s)
}

func clampTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || s == store.DefaultChatTitle {
		return ""
	}
	if utf8.RuneCountInString(s) <= MaxTitleRunes {
		return s
	}
	runes := []rune(s)
	cut := runes[:MaxTitleRunes]
	if i := lastSpace(cut); i >= MaxTitleRunes/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(string(cut))
}

func lastSpace(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			return i
		}
	}
	return -1
}
