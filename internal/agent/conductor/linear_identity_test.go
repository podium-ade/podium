package conductor

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// Who may start a turn from Linear (identity.go): the agent app is held to Slack's rule, the
// poller is not. These run without a database; what needs one — a candidate that has signed
// in, the identity row it writes — is in gate_integration_test.go.

// reactingSource is a source of one kind that remembers what it was asked to react.
type reactingSource struct {
	kindSource
	mu        sync.Mutex
	reactions []Reaction
}

func (r *reactingSource) React(_ context.Context, _ string, kind Reaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reactions = append(r.reactions, kind)
	return nil
}

func (r *reactingSource) reacted() []Reaction {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Reaction(nil), r.reactions...)
}

func linearSource() *reactingSource {
	return &reactingSource{kindSource: kindSource{kind: SourceLinear}}
}

func identityConductor(now *time.Time) *Conductor {
	return &Conductor{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*sessionState{},
		clock:    func() time.Time { return *now },
	}
}

// sessionPrompt is a prompt in a Linear agent session, as the app hands it over: a candidate
// email and the account that wrote it.
func sessionPrompt(email, user string) InboundEvent {
	return InboundEvent{
		SourceKind:      SourceLinear,
		SourceKey:       "linear:session:s1",
		Ref:             "issue-1/team-1/ENG-1/s1",
		Author:          "Ada",
		Mentioned:       true,
		Text:            "fix the build",
		Login:           email,
		SpeakerProvider: SpeakerLinear,
		SpeakerID:       "org-1/" + user,
	}
}

// polledTicket is a ticket the poller saw: no account at all.
func polledTicket() InboundEvent {
	return InboundEvent{
		SourceKind: SourceLinear,
		SourceKey:  "linear:issue-1",
		Ref:        "issue-1/team-1/ENG-1",
		Author:     "Ada",
		Text:       "ENG-1 — fix the build",
	}
}

func TestSpeakerLinearIsTheStoresLinearProvider(t *testing.T) {
	assert.Equal(t, store.ProviderLinear, SpeakerLinear)
	assert.Equal(t, "org-1/u-1", store.LinearExternalID("org-1", "u-1"))
}

// A prompt in an agent session from somebody who did not resolve is refused, and the source
// is asked to say so; nothing is admitted.
func TestALinearSessionPromptFromSomebodyNotInPodiumIsRefused(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	ev := sessionPrompt("mallory@acme.com", "u-666")

	c.resolveSpeaker(context.Background(), src, &ev)
	assert.Empty(t, ev.Login, "a candidate is never taken on trust")
	assert.False(t, c.admit(context.Background(), src, &ev))
	assert.Equal(t, []Reaction{ReactionRefused}, src.reacted())
}

// A prompt the app could not name a person for — it still names the provider — is refused
// too, rather than mistaken for the poller's.
func TestALinearSessionPromptWithNoAccountIsRefused(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	ev := sessionPrompt("", "")
	ev.SpeakerID = ""

	c.resolveSpeaker(context.Background(), src, &ev)
	assert.False(t, c.admit(context.Background(), src, &ev))
	assert.Equal(t, []Reaction{ReactionRefused}, src.reacted())
}

// A speaker who resolved is admitted, and nothing is reacted.
func TestAResolvedLinearSpeakerIsAdmitted(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	ev := sessionPrompt("ada@acme.com", "u-1")
	assert.True(t, c.admit(context.Background(), src, &ev))
	assert.Empty(t, src.reacted())
}

// The poller is the unauthenticated mode: a polled ticket names no account and is admitted
// as it always was, as nobody. A Login it might have set is not believed.
func TestAPolledLinearTicketIsAdmittedWithNoSpeaker(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	ev := polledTicket()
	ev.Login = "ada@acme.com"

	c.resolveSpeaker(context.Background(), src, &ev)
	assert.Empty(t, ev.Login, "the poller proves nobody")
	assert.True(t, c.admit(context.Background(), src, &ev))
	assert.Empty(t, src.reacted())
	assert.Empty(t, c.turnLogin(context.Background(), src, store.Session{SourceKind: SourceLinear}, ev),
		"and runs as nobody")
}

// A candidate on a domain the instance does not allow is refused without asking the store,
// and the refusal is remembered under Linear's own key for identityMissTTL — not for as long
// as a hit, so somebody who fixes it is not refused for long.
func TestALinearCandidateOnAnotherDomainIsRememberedAsAMiss(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	c.allowedDomains = []string{"acme.com"}
	// Non-nil, and never read: a domain refusal must not reach the database.
	c.store = &store.Store{}
	src := linearSource()
	ev := sessionPrompt("Mallory@Evil.example", "u-666")

	c.resolveSpeaker(context.Background(), src, &ev)
	assert.Empty(t, ev.Login)
	key := store.ProviderLinear + "|org-1/u-666|mallory@evil.example"
	login, ok := c.idents.get(key, now)
	require.True(t, ok, "the miss is remembered")
	assert.Empty(t, login)
	_, ok = c.idents.get(key, now.Add(identityMissTTL))
	assert.False(t, ok, "for a minute")
}

// A hit is remembered under Linear's key, apart from Slack's, for identityHitTTL; forgetting
// the account — what stillAdmitted does — drops it.
func TestALinearHitIsCachedApartFromSlackAndCanBeForgotten(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	c.store = &store.Store{}
	src := linearSource()
	c.idents.put(store.ProviderLinear+"|org-1/u-1|ada@acme.com", "Ada@acme.com", now)

	ev := sessionPrompt("ada@acme.com", "u-1")
	c.resolveSpeaker(context.Background(), src, &ev)
	assert.Equal(t, "Ada@acme.com", ev.Login, "as podium-server spells it")

	_, ok := c.idents.get(store.ProviderSlack+"|org-1/u-1|ada@acme.com", now)
	assert.False(t, ok, "a Linear account proves nothing about a Slack one")
	_, ok = c.idents.get(store.ProviderLinear+"|org-1/u-1|ada@acme.com", now.Add(identityHitTTL-time.Second))
	assert.True(t, ok)

	c.idents.forget(store.ProviderLinear, "org-1/u-1")
	_, ok = c.idents.get(store.ProviderLinear+"|org-1/u-1|ada@acme.com", now)
	assert.False(t, ok)
}

// A queued Linear turn whose person no longer resolves when it starts is refused then, as a
// Slack one is.
func TestAQueuedLinearTurnWhosePersonLeftIsRefusedWhenItStarts(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	ev := sessionPrompt("ada@acme.com", "u-1")
	assert.False(t, c.stillAdmitted(context.Background(), src, &ev))
	assert.Equal(t, []Reaction{ReactionRefused}, src.reacted())

	polled := polledTicket()
	assert.True(t, c.stillAdmitted(context.Background(), src, &polled), "the poller's turn has nobody to lose")
}

// A Linear turn runs as the person who asked: their secrets, MCP servers and GitHub account
// all key off turnLogin, and Usage counts it as theirs.
func TestALinearTurnRunsAsItsSpeaker(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	sess := store.Session{SourceKind: SourceLinear, SourceKey: "linear:session:s1"}
	ev := sessionPrompt("ada@acme.com", "u-1")
	assert.Equal(t, "ada@acme.com", c.turnLogin(context.Background(), src, sess, ev))
	assert.Equal(t, "ada@acme.com", c.turnStartedBy(context.Background(), src, sess, ev))
}

// Somebody else's reply while a turn is up — parked on a question, or working — is not the
// same asker, so it is queued as a turn of its own rather than put into somebody else's.
// Before Linear had people every speaker was the same nobody.
func TestAReplyFromAnotherLinearPersonIsNotTheSameAsker(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	src := linearSource()
	c.setTurnAsker("s1", src, sessionPrompt("ada@acme.com", "u-1"), "linear-work")

	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state("s1")
	assert.True(t, c.sameAsker(st, src, sessionPrompt("ada@acme.com", "u-1")))
	assert.False(t, c.sameAsker(st, src, sessionPrompt("bob@acme.com", "u-2")))
}

// A Linear session is a thread anybody who can see the issue may reply in, so it spends the
// company's model key, as a Slack thread does; the asker's secrets still apply.
func TestALinearConversationIsShared(t *testing.T) {
	now := time.Now()
	c := identityConductor(&now)
	for _, key := range []string{"linear:session:s1", "linear:issue-1"} {
		assert.True(t, c.sharedConversation(context.Background(), store.Session{SourceKind: SourceLinear, SourceKey: key}), key)
		assert.Empty(t, c.turnCredentialOwner(context.Background(),
			store.Session{SourceKind: SourceLinear, SourceKey: key}, "ada@acme.com", "claude", false), key)
	}
}
