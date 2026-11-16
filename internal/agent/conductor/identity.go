package conductor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// Who may start a turn, and as whom (multiplayer sessions, docs/agent.md).
//
// Every surface is one identity: a Slack account or a GitHub account counts as a person only
// once it has been PROVEN to be a Podium login — a Slack workspace email that matches a login
// which has signed in, a GitHub numeric id from that person's own OAuth connect. resolveSpeaker
// turns what a source proved into InboundEvent.Login, and admit decides from it:
//
//   - the web chat: always admitted; its caller is the signed-in person.
//   - Slack: only a speaker who resolved. Anybody else is recorded in the mirror, gets 🚫,
//     and nothing runs.
//   - GitHub: what nobody said (a pull request opened, marked ready, pushed to by this App)
//     runs as it always has. A resolved speaker is answered with their own credentials. A
//     speaker who did not resolve may ask for exactly one thing — a review — and only
//     through the review command's narrow grammar; anything else gets a confused reaction.
//   - Linear and the dev source have no accounts and are unchanged.

// Slack identity resolution is cached: the Slack candidate check is a database read per
// message, and a busy thread repeats the same speakers. A miss is remembered for less time
// than a hit so that somebody who has just signed in is not refused for long.
//
// GitHub resolution is not cached at all. Connecting and disconnecting GitHub are writes to
// the identities table, made by whichever conductor served the request, so an in-process
// cache here could only be invalidated on one of them; a cached hit would keep a
// disconnected account — or one reconnected under somebody else's login — running as its
// old person for up to identityHitTTL. A webhook is one indexed read, and the store is the
// only place that is right about who an account belongs to.
const (
	identityHitTTL  = 10 * time.Minute
	identityMissTTL = time.Minute
)

// reviewCommandEvery is how often one person outside Podium may ask for a review of one
// pull request. A review is the most expensive thing an unauthenticated stranger can make
// this App do, so it is rate limited per person per pull request.
const reviewCommandEvery = 10 * time.Minute

type identityEntry struct {
	login string
	at    time.Time
}

// identityCache is resolveSpeaker's memory, keyed by provider and account.
type identityCache struct {
	mu      sync.Mutex
	entries map[string]identityEntry
	// reviews is when each (pull request, account) last asked for a review.
	reviews map[string]time.Time
}

func (ic *identityCache) get(key string, now time.Time) (string, bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	e, ok := ic.entries[key]
	if !ok {
		return "", false
	}
	ttl := identityHitTTL
	if e.login == "" {
		ttl = identityMissTTL
	}
	if now.Sub(e.at) >= ttl {
		delete(ic.entries, key)
		return "", false
	}
	return e.login, true
}

func (ic *identityCache) put(key, login string, now time.Time) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.entries == nil {
		ic.entries = map[string]identityEntry{}
	}
	ic.entries[key] = identityEntry{login: login, at: now}
}

// forget drops what is remembered about one account, so the next resolution reads the store.
func (ic *identityCache) forget(provider, externalID string) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	prefix := provider + "|" + externalID + "|"
	for k := range ic.entries {
		if k == provider+"|"+externalID || strings.HasPrefix(k, prefix) {
			delete(ic.entries, k)
		}
	}
}

// allowReview records one review command and reports whether it is within the rate limit.
func (ic *identityCache) allowReview(key string, now time.Time) bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.reviews == nil {
		ic.reviews = map[string]time.Time{}
	}
	for k, at := range ic.reviews {
		if now.Sub(at) >= reviewCommandEvery {
			delete(ic.reviews, k)
		}
	}
	if _, ok := ic.reviews[key]; ok {
		return false
	}
	ic.reviews[key] = now
	return true
}

// resolveSpeaker makes ev.Login a Podium person or empty. See InboundEvent.Login for what
// each source hands in.
func (c *Conductor) resolveSpeaker(ctx context.Context, src Source, ev *InboundEvent) {
	switch ev.SpeakerProvider {
	case store.ProviderSlack:
		ev.Login = c.resolveSlack(ctx, ev)
	case store.ProviderGitHub:
		ev.Login = c.resolveGitHub(ctx, ev)
	default:
		// A Slack or GitHub event that names no account proves nobody: whatever Login a
		// source put there is not taken on trust.
		if src != nil && (src.Kind() == SourceSlack || src.Kind() == SourceGitHub) {
			ev.Login = ""
		}
	}
}

// resolveSlack keeps the source's candidate email only if it is on an allowed domain and is
// a login that has signed in to Podium, and records the Slack account as that person's.
func (c *Conductor) resolveSlack(ctx context.Context, ev *InboundEvent) string {
	candidate := strings.ToLower(strings.TrimSpace(ev.Login))
	if candidate == "" || ev.SpeakerID == "" || c.store == nil {
		return ""
	}
	key := store.ProviderSlack + "|" + ev.SpeakerID + "|" + candidate
	now := c.now()
	if login, ok := c.idents.get(key, now); ok {
		return login
	}
	login := ""
	if c.domainAllowed(candidate) {
		// The Slack email is lowercased; the person's login is however podium-server spells
		// it (0040), which is what every other comparison uses.
		canonical, ok, err := c.store.CanonicalWebLogin(ctx, candidate)
		if err != nil {
			// Not cached: a database blip must not refuse somebody for a minute.
			c.logger.WarnContext(ctx, "checking whether a slack speaker is in podium failed",
				"slack_user", ev.SpeakerID, "error", err)
			return ""
		}
		if ok {
			login = canonical
		}
	}
	if login != "" {
		if err := c.store.UpsertIdentity(ctx, store.Identity{
			Login: login, Provider: store.ProviderSlack, ExternalID: ev.SpeakerID,
			DisplayName: ev.Author, AvatarURL: ev.SpeakerAvatar, VerifiedAt: now.UTC(),
		}); err != nil {
			c.logger.WarnContext(ctx, "recording a slack identity failed",
				"slack_user", ev.SpeakerID, "login", login, "error", err)
		}
	}
	c.idents.put(key, login, now)
	return login
}

// resolveGitHub is the login whose GitHub connect proved this numeric id, or empty.
func (c *Conductor) resolveGitHub(ctx context.Context, ev *InboundEvent) string {
	if ev.SpeakerID == "" || c.store == nil {
		return ""
	}
	login, err := c.store.IdentityLogin(ctx, store.ProviderGitHub, ev.SpeakerID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		login = ""
	case err != nil:
		c.logger.WarnContext(ctx, "resolving a github speaker failed",
			"github_id", ev.SpeakerID, "error", err)
		return ""
	}
	return login
}

// domainAllowed reports whether an email is on one of the instance's Workspace domains. No
// configured domains means no domain check: the email must still be a login that signed in
// through podium-server, which only admits its own workspace.
func (c *Conductor) domainAllowed(email string) bool {
	if len(c.allowedDomains) == 0 {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := email[at+1:]
	for _, d := range c.allowedDomains {
		if strings.EqualFold(domain, d) {
			return true
		}
	}
	return false
}

// admit decides whether ev may start a turn, after resolveSpeaker. The event is already
// recorded in the mirror by then (accept records first) — the conversation is the whole
// thread, not only the parts Podium acted on — and a refused speaker who addressed the bot
// is told, by a reaction, that it will not.
//
// A review command from somebody outside Podium is rewritten here into the review it is
// allowed to be: ev.Text becomes ev.ReviewRequest and the event becomes Automatic, which runs
// the github: true playbook directly, as the App, with no assistant turn and no person's
// credentials.
func (c *Conductor) admit(ctx context.Context, src Source, ev *InboundEvent) bool {
	kind := src.Kind()
	switch kind {
	case SourceSlack:
		if ev.Login != "" {
			c.gateDecision(kind, "admitted")
			return true
		}
		c.refuse(ctx, src, *ev, "not in podium")
		return false
	case SourceGitHub:
		switch {
		case ev.Automatic:
			c.gateDecision(kind, "automatic")
			return true
		case ev.Login != "":
			c.gateDecision(kind, "admitted")
			return true
		case ev.SpeakerProvider == store.ProviderSlack:
			// A Slack mention that named a pull request follows Slack's rule.
			c.refuse(ctx, src, *ev, "not in podium")
			return false
		case ev.ReviewRequest != "":
			// Joining the review already running first: it starts nothing, so it must not
			// spend the person's one review in reviewCommandEvery either.
			if c.reviewRunning(ctx, ev.SourceKey) {
				c.logger.InfoContext(ctx, "a review command from outside podium joined the review already running",
					"source_key", ev.SourceKey, "github_id", ev.SpeakerID)
				c.gateDecision(kind, "review_collapsed")
				return false
			}
			if !c.idents.allowReview(ev.SourceKey+"|"+ev.SpeakerID, c.now()) {
				c.logger.InfoContext(ctx, "a review command from outside podium was rate limited",
					"source_key", ev.SourceKey, "github_id", ev.SpeakerID)
				c.gateDecision(kind, "review_rate_limited")
				return false
			}
			ev.Text = ev.ReviewRequest
			ev.Automatic = true
			ev.Role = ""
			c.gateDecision(kind, "review_command")
			return true
		default:
			c.refuse(ctx, src, *ev, "not linked")
			return false
		}
	default:
		return true
	}
}

// reviewRunning reports whether this pull request's review playbook is already working.
func (c *Conductor) reviewRunning(ctx context.Context, sourceKey string) bool {
	if c.store == nil {
		return false
	}
	sess, err := c.store.GetSessionByKey(ctx, sourceKey)
	if err != nil {
		return false
	}
	running, err := c.store.RunningDelegationsForSession(ctx, sess.ID)
	if err != nil {
		return false
	}
	name := ""
	if p := c.profiles.Current(); p != nil {
		name = p.GitHubPlaybookFor(pullRequestRepo(sourceKey))
	}
	for _, d := range running {
		if name == "" || d.Playbook == name {
			return true
		}
	}
	return false
}

func (c *Conductor) refuse(ctx context.Context, src Source, ev InboundEvent, reason string) {
	c.logger.InfoContext(ctx, "an inbound message was not acted on", "source", src.Kind(),
		"source_key", ev.SourceKey, "reason", reason)
	c.gateDecision(src.Kind(), "refused")
	if !ev.Mentioned {
		return
	}
	if err := src.React(ctx, ev.Ref, ReactionRefused); err != nil {
		c.logger.WarnContext(ctx, "reacting to a refused message failed", "ref", ev.Ref, "error", err)
	}
}

func (c *Conductor) gateDecision(source, outcome string) {
	if c.metrics != nil && c.metrics.GateDecisions != nil {
		c.metrics.GateDecisions.WithLabelValues(source, outcome).Inc()
	}
}

// recordParticipant makes a resolved speaker a participant of the mirrored chat, and its
// owner when it has none yet. Both writes are guarded in SQL, so two conductors recording
// two speakers at once end with both participants and exactly one owner.
func (c *Conductor) recordParticipant(ctx context.Context, chat store.Chat, ev InboundEvent) {
	if ev.Login == "" || c.store == nil {
		return
	}
	via := store.JoinedWeb
	switch ev.SpeakerProvider {
	case store.ProviderSlack:
		via = store.JoinedSlack
	case store.ProviderGitHub:
		via = store.JoinedGitHub
	}
	at := ev.TS
	if at.IsZero() {
		at = c.now()
	}
	if _, err := c.store.AddChatParticipant(ctx, chat.ID, ev.Login, via, at); err != nil {
		c.logger.WarnContext(ctx, "recording a participant failed",
			"chat_id", chat.ID, "login", ev.Login, "error", err)
		return
	}
	if chat.Login == "" {
		if _, err := c.store.ClaimChatOwner(ctx, chat.ID, ev.Login); err != nil {
			c.logger.WarnContext(ctx, "giving a mirrored chat its owner failed",
				"chat_id", chat.ID, "login", ev.Login, "error", err)
		}
	}
}

// turnLogin is whose credentials a turn runs with: the person who started it, on every
// surface. Never the chat's owner on somebody else's behalf — in a conversation several
// people speak in, the owner is only whoever happened to be first. A turn with no resolved
// speaker (Linear, the dev source, an automatic review) falls back to asker, which is the
// web chat's owner and nobody for every other source.
func (c *Conductor) turnLogin(ctx context.Context, src Source, sess store.Session, ev InboundEvent) string {
	if l := strings.TrimSpace(ev.Login); l != "" && !ev.Automatic {
		return l
	}
	return c.asker(ctx, src, sess)
}

// turnStartedBy is what a turn records as whose it was (turns.started_by): turnLogin, or
// store.TurnStartedByApp for a GitHub turn nobody in Podium is behind — an automatic review,
// or a review command from somebody who has not linked GitHub. Usage is broken down by it.
func (c *Conductor) turnStartedBy(ctx context.Context, src Source, sess store.Session, ev InboundEvent) string {
	if l := turnOwner(c.turnLogin(ctx, src, sess, ev)); l != "" {
		return l
	}
	kind := sess.SourceKind
	if src != nil {
		kind = src.Kind()
	}
	if kind == SourceGitHub {
		return store.TurnStartedByApp
	}
	if ev.Routine != nil && ev.Routine.RoutineID != "" {
		// A system routine's run: nobody is behind it but the routine itself, which Usage
		// shows as its own line.
		return store.TurnStartedByRoutine(ev.Routine.RoutineID)
	}
	return ""
}

func (c *Conductor) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}
