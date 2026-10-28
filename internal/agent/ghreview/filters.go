package ghreview

import (
	"context"
	"regexp"
	"strings"
)

// Reply modes decide whether a reply in an inline review thread needs the trigger.
// A top-level comment always does.
const (
	// ReplyBotThreads lets a reply through without the trigger when the App started the
	// thread. A reply in a thread a person started still needs it.
	ReplyBotThreads = "bot_threads"
	// ReplyAnyThread lets any reply through on a pull request that already has a session.
	ReplyAnyThread = "any_thread"
	// ReplyAlways asks for the trigger in every reply.
	ReplyAlways = "always"
)

// Filters are the operator's rules for which deliveries start a turn. They are read on
// every delivery, so a saved change applies without a restart. The zero value is the
// default: anyone, the @mention only, bot threads, and the review on open.
type Filters struct {
	// AllowedActors are the only logins that may trigger a turn. Empty is anyone.
	AllowedActors []string
	// IgnoredActors never trigger a turn, whatever else matches.
	IgnoredActors []string
	// IgnoreBots drops every delivery whose sender is a bot account.
	IgnoreBots bool
	// TriggerPhrases start a turn from a top-level comment, beside the @mention. They are
	// plain text, matched without case as whole words.
	TriggerPhrases []string
	// ReplyMode is one of the Reply* constants. Empty is ReplyBotThreads.
	ReplyMode string
	// NoAutoReview stops the review that starts when a pull request opens or is marked
	// ready.
	NoAutoReview bool
}

// FilterLookup returns the filters in force now.
type FilterLookup func(ctx context.Context) (Filters, error)

func (f Filters) replyMode() string {
	switch f.ReplyMode {
	case ReplyAnyThread, ReplyAlways:
		return f.ReplyMode
	default:
		return ReplyBotThreads
	}
}

// actorAllowed reports whether login may trigger a turn, and why not when it may not.
func (f Filters) actorAllowed(login, typ string) (bool, string) {
	if f.IgnoreBots && strings.EqualFold(typ, "Bot") {
		return false, "bot sender"
	}
	if containsLogin(f.IgnoredActors, login) {
		return false, "ignored actor"
	}
	if len(f.AllowedActors) > 0 && !containsLogin(f.AllowedActors, login) {
		return false, "actor not allowed"
	}
	return true, ""
}

func containsLogin(list []string, login string) bool {
	login = normalizeLogin(login)
	if login == "" {
		return false
	}
	for _, l := range list {
		if normalizeLogin(l) == login {
			return true
		}
	}
	return false
}

func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(login), "@"))
}

// phraseRE matches any of the trigger phrases as whole words. Nil when there are none.
// The groups either side are kept so stripping a phrase leaves its neighbours alone.
func (f Filters) phraseRE() *regexp.Regexp {
	var alts []string
	for _, p := range f.TriggerPhrases {
		words := strings.Fields(p)
		if len(words) == 0 {
			continue
		}
		for i, w := range words {
			words[i] = regexp.QuoteMeta(w)
		}
		alts = append(alts, strings.Join(words, `\s+`))
	}
	if len(alts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])(?:` + strings.Join(alts, "|") + `)([^A-Za-z0-9_]|$)`)
}
