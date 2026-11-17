package chat

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a web message has to say to start a turn in a multiplayer chat
// (multiplayer sessions, docs/agent.md), and who it names.
//
// The composer's mention chips insert exact tokens, and the server matches those tokens and
// nothing cleverer, so what the UI promises ("Send to Podium") and what the server does
// cannot drift apart:
//
//   - PodiumMention, "@Podium", puts the message to Podium.
//   - "@<assistant name>" does the same in a chat bound to a personal assistant.
//   - "@[Name](mention:<login>)" names a Podium person. It is a Markdown link, so the record
//     renders it as one anywhere, and phase 5 tells the login.
//
// Matching is case-insensitive and needs a boundary on both sides, so an email address
// (alice@podium.dev) or a longer word (@podiums) is not a mention.

// PodiumMention is the token the composer's Podium chip inserts.
const PodiumMention = "@Podium"

// personMention is the composer's person chip: @[Display Name](mention:login).
var personMention = regexp.MustCompile(`@\[[^\]\n]{1,200}\]\(mention:([^)\s]{1,320})\)`)

// MentionsPodium reports whether text puts the message to Podium, or to the chat's
// personal assistant when personality is its name.
func MentionsPodium(text, personality string) bool {
	// A person chip is never Podium, even one whose display name is "Podium".
	text = personMention.ReplaceAllString(text, " ")
	if hasMention(text, strings.TrimPrefix(PodiumMention, "@")) {
		return true
	}
	if p := strings.TrimSpace(personality); p != "" && hasMention(text, p) {
		return true
	}
	return false
}

// MentionedLogins is every Podium login text names with a person chip, once each, in order.
func MentionedLogins(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range personMention.FindAllStringSubmatch(text, -1) {
		login := strings.ToLower(strings.TrimSpace(m[1]))
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out
}

// hasMention reports whether text contains "@"+name with a boundary before the @ and after
// the name. Case-insensitive.
func hasMention(text, name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(text)
	want := "@" + strings.ToLower(name)
	for i := 0; ; {
		j := strings.Index(lower[i:], want)
		if j < 0 {
			return false
		}
		at := i + j
		end := at + len(want)
		if boundaryBefore(lower, at) && boundaryAfter(lower, end) {
			return true
		}
		i = at + 1
	}
}

func boundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWordRune(r)
}

func boundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return !isWordRune(r)
}

// isWordRune is what may not touch a mention: a letter, a digit, and the characters a
// handle or an email address is made of. A full stop or a comma may: "thanks, @Podium."
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '@'
}
