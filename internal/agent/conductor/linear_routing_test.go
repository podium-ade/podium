package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// What answers a Linear agent session (conductor.go): a delegated issue runs a playbook as a
// task, an @mention is the assistant's where there is a host runtime and a playbook's where
// there is not, and a /playbook in a person's words runs that playbook for the turn. The poller
// is untouched. These run without a database; the turns themselves are in
// linear_routing_integration_test.go.

func routingProfile() *profiles.Profile {
	return &profiles.Profile{
		Name: "podium",
		Playbooks: map[string]profiles.Playbook{
			"linear-work": {Name: "linear-work", Linear: true},
			"fix":         {Name: "fix"},
			"general":     {Name: "general", SlackChannels: []string{"C1"}},
		},
	}
}

func routingConductor(host bool) *Conductor {
	c := &Conductor{profiles: profiles.NewLive(routingProfile())}
	if host {
		c.host = &HostRuntime{}
	}
	return c
}

// mentionPrompt is a prompt in a session opened by @mentioning the app.
func mentionPrompt(text string) InboundEvent {
	ev := sessionPrompt("ada@acme.com", "u-1")
	ev.Text = text
	return ev
}

// delegatedPrompt is a prompt in a session opened by delegating the issue to the app.
func delegatedPrompt(text string) InboundEvent {
	ev := mentionPrompt(text)
	ev.Ref += "/delegated"
	ev.Delegated = true
	return ev
}

func TestALinearMentionIsAConversationOnlyWhereThereIsAnAssistant(t *testing.T) {
	linear := linearSource()
	withHost, without := routingConductor(true), routingConductor(false)

	assert.True(t, withHost.conversational(linear, mentionPrompt("what is it?")))
	assert.False(t, without.conversational(linear, mentionPrompt("what is it?")),
		"with no host runtime a mention runs the linear: true playbook, as it always has")
	assert.False(t, withHost.conversational(linear, delegatedPrompt("EME-5")),
		"a delegated issue is a piece of work, whatever this conductor has")
	assert.False(t, withHost.conversational(linear, polledTicket()), "the poller is untouched")

	// Every other source is what it was.
	assert.True(t, withHost.conversational(kindSource{kind: SourceSlack}, InboundEvent{}))
	assert.True(t, withHost.conversational(kindSource{kind: SourceGitHub}, InboundEvent{}))
	assert.True(t, withHost.conversational(kindSource{kind: SourceChat}, InboundEvent{}))
	assert.False(t, without.conversational(kindSource{kind: SourceSlack}, InboundEvent{}))
}

func TestALinearSessionRunsTheLinearPlaybookUnlessSomebodyNamesAnother(t *testing.T) {
	p := routingProfile()
	linear := linearSource()
	tests := []struct {
		name        string
		ev          InboundEvent
		session     string
		want        string
		instruction string
		named       bool
	}{
		{"a delegation runs the linear: true playbook", delegatedPrompt("EME-5 — fix the build"), "linear-work",
			"linear-work", "EME-5 — fix the build", false},
		{"a /playbook in a follow-up runs that playbook for the turn", delegatedPrompt("/fix the flaky test"), "linear-work",
			"fix", "the flaky test", true},
		{"a follow-up that names nothing runs the session's own", delegatedPrompt("and the docs"), "linear-work",
			"linear-work", "and the docs", false},
		{"a mention with no assistant runs the linear: true playbook", mentionPrompt("what is it?"), "",
			"linear-work", "what is it?", false},
		{"an unknown /name is the person's words", mentionPrompt("/shrug hi"), "linear-work",
			"linear-work", "/shrug hi", false},
		{"a Slack channel's claim does not reach Linear", mentionPrompt("hi"), "",
			"linear-work", "hi", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := linearAgentSession(linear, tc.ev)
			require.True(t, app)
			sel := p.Select(taskRouting(p, tc.ev, app))
			session := tc.session
			if session == "" {
				session = sel.Playbook.Name
			}
			name, named, refused := turnPlaybook(sel, session, app)
			assert.False(t, refused, "a Linear session never refuses a playbook somebody named")
			assert.Equal(t, tc.want, name)
			assert.Equal(t, tc.named, named)
			assert.Equal(t, tc.instruction, sel.Instruction)
		})
	}
}

// The poller names its playbook and nobody, and a prefix in a ticket is the ticket's words.
func TestAPolledTicketRunsItsPlaybookByteForByte(t *testing.T) {
	p := routingProfile()
	ev := polledTicket()
	ev.Playbook = "linear-work"
	ev.Text = "/fix ENG-1 — the build"
	app := linearAgentSession(linearSource(), ev)
	assert.False(t, app)
	sel := p.Select(taskRouting(p, ev, app))
	assert.Equal(t, "linear-work", sel.Playbook.Name)
	assert.Equal(t, "/fix ENG-1 — the build", sel.Instruction)
	name, named, refused := turnPlaybook(sel, "linear-work", app)
	assert.Equal(t, "linear-work", name)
	assert.False(t, named)
	assert.False(t, refused)
}

// A Slack thread keeps the playbook it started with, as it always has.
func TestASlackThreadStillRefusesAnotherPlaybook(t *testing.T) {
	p := routingProfile()
	ev := InboundEvent{SourceKind: SourceSlack, Channel: "C1", Text: "/fix it"}
	app := linearAgentSession(kindSource{kind: SourceSlack}, ev)
	assert.False(t, app)
	sel := p.Select(taskRouting(p, ev, app))
	_, _, refused := turnPlaybook(sel, "general", app)
	assert.True(t, refused)
	name, named, refused := turnPlaybook(p.Select(taskRouting(p, InboundEvent{Channel: "C1", Text: "hi"}, false)), "general", false)
	assert.Equal(t, "general", name)
	assert.False(t, named)
	assert.False(t, refused)
}

// A /playbook in a session the assistant answers is delegated directly; anything else is the
// assistant's to read, prefix and all.
func TestAPlaybookNamedInAMentionIsDelegatedDirectly(t *testing.T) {
	c := routingConductor(true)
	p := c.profiles.Current()

	ev := mentionPrompt("/fix the flaky test")
	j, ok := c.namedPlaybook(linearSource(), p, &ev)
	require.True(t, ok)
	assert.True(t, j.direct)
	assert.Equal(t, "fix", j.playbook.Name)
	assert.Equal(t, "the flaky test", ev.Text)
	assert.Equal(t, "fix", ev.Playbook, "a queued turn still runs it")

	for _, text := range []string{"/shrug hi", "what is it?", "/etc/hosts is wrong"} {
		ev := mentionPrompt(text)
		_, ok := c.namedPlaybook(linearSource(), p, &ev)
		assert.False(t, ok, text)
		assert.Equal(t, text, ev.Text, "the person's words are left alone")
		assert.Empty(t, ev.Playbook)
	}
}

// A queued Linear prompt runs what accept chose for it when it starts, whatever the turn
// before it ran: the assistant, a playbook named for it, or the session's playbook.
func TestAQueuedLinearPromptRunsWhatWasChosenForIt(t *testing.T) {
	c := routingConductor(true)
	src := linearSource()
	previous := playbookJob(routingProfile().Playbooks["fix"])

	_, j := c.sourceAndJob(mentionPrompt("what is it?"), src, previous)
	assert.True(t, j.onHost, "a question is the assistant's")

	named := mentionPrompt("the flaky test")
	named.Playbook = "fix"
	_, j = c.sourceAndJob(named, src, assistantJob(c.profiles.Current().Assistant()))
	assert.True(t, j.direct)
	assert.Equal(t, "fix", j.playbook.Name)

	followUp := delegatedPrompt("and the docs")
	followUp.Playbook = "linear-work"
	_, j = c.sourceAndJob(followUp, src, previous)
	assert.False(t, j.onHost)
	assert.False(t, j.direct)
	assert.Equal(t, "linear-work", j.playbook.Name, "not the playbook the turn before it was named")

	// A message queued with no playbook keeps the job the session was running, as before.
	slack := InboundEvent{SourceKind: SourceSlack, Text: "hi"}
	_, j = routingConductor(false).sourceAndJob(slack, kindSource{kind: SourceSlack}, previous)
	assert.Equal(t, "fix", j.playbook.Name)
}
