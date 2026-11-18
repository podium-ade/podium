package conductor

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/store"
)

// threaded is a source whose conversations hold threads, as the Linear app's issues hold agent
// sessions: a ref "<issue>/<session>" answers into thread "session:<session>".
type threaded struct{ hearer }

func (threaded) ThreadKey(ref string) string {
	if _, sess, ok := strings.Cut(ref, "/"); ok && sess != "" {
		return "session:" + sess
	}
	return ""
}

// A prompt from one agent session is never put into another's turn, even from the same person
// on the same surface; a prompt in the same session is, as before.
func TestAPromptFromAnotherAgentSessionIsATurnOfItsOwn(t *testing.T) {
	src := &threaded{}
	c := &Conductor{sources: []Source{src}, sessions: map[string]*sessionState{}}
	first := InboundEvent{Login: "ada@acme.com", Ref: "iss-1/s-1"}
	c.setTurnAsker("sess_1", src, first, "")

	c.mu.Lock()
	st := c.state("sess_1")
	assert.True(t, c.sameAsker(st, src, InboundEvent{Login: "ada@acme.com", Ref: "iss-1/s-1"}), "the same session")
	assert.False(t, c.sameAsker(st, src, InboundEvent{Login: "ada@acme.com", Ref: "iss-1/s-2"}),
		"another agent session on the same issue")
	assert.False(t, c.sameAsker(st, src, InboundEvent{Login: "bo@acme.com", Ref: "iss-1/s-1"}))
	c.mu.Unlock()
}

// A routine's run is a thread of its own: nothing is put into it, and it is put into nothing.
func TestARoutineRunIsNeverPutIntoATurnNorAnythingIntoIt(t *testing.T) {
	web := &hearer{}
	c := &Conductor{sources: []Source{web}, sessions: map[string]*sessionState{}}
	run := InboundEvent{Login: "ada@acme.com", Ref: "chat_1", Routine: &RoutineRef{RoutineID: "rtn_1", RunID: "rrun_1"}}
	person := InboundEvent{Login: "ada@acme.com", Ref: "chat_1"}

	c.setTurnAsker("sess_1", web, person, "")
	c.mu.Lock()
	assert.False(t, c.sameAsker(c.state("sess_1"), web, run), "a run is not the person's message")
	c.mu.Unlock()

	c.setTurnAsker("sess_1", web, run, "")
	c.mu.Lock()
	assert.False(t, c.sameAsker(c.state("sess_1"), web, person), "nor is the person's message the run's")
	c.mu.Unlock()
}

// A thread is read off a ref by whichever source wrote it, so a stop can tell one agent
// session's turns and tasks from another's, and from a routine's, in one conversation.
func TestAThreadIsReadOffARefByTheSourceThatWroteIt(t *testing.T) {
	c := &Conductor{sources: []Source{&hearer{}, &threaded{}}}
	assert.Equal(t, "session:s-1", c.refThread("iss-1/s-1"))
	assert.Empty(t, c.refThread("chat_1"), "a web chat's ref is no thread")
	assert.Empty(t, c.refThread(""))
	queued := InboundEvent{Ref: "iss-1/s-2"}
	assert.Equal(t, "session:s-2", c.threadOf(nil, queued), "a queued event is read without its source")
	assert.Equal(t, "routine:rrun_1", c.threadOf(nil, InboundEvent{Ref: "chat_1", Routine: &RoutineRef{RunID: "rrun_1"}}))
}

// A routine's run is its owner's or nobody's, never the conversation's owner's: a run in a
// Linear issue's conversation is in a chat whoever spoke first owns.
func TestARoutineRunNeverRunsAsTheChatsOwner(t *testing.T) {
	c := &Conductor{}
	run := InboundEvent{Routine: &RoutineRef{RoutineID: "rtn_sys", RunID: "rrun_1"}}
	assert.Empty(t, c.turnLogin(context.Background(), &hearer{}, store.Session{SourceKey: "linear:iss-1"}, run))
	run.Login = "ana@acme.com"
	assert.Equal(t, "ana@acme.com", c.turnLogin(context.Background(), &hearer{}, store.Session{}, run))
}

// A question in a Linear ticket's conversation is answered from the web chat — or the inbox,
// which sends there — by the person whose turn asked it, whichever surface the turn began on: a
// Linear agent session, or their routine's run. Not by anybody else, not by a routine, and not
// in a conversation that is no ticket's.
func TestTheAskedPersonAnswersFromTheWebWhicheverSurfaceAsked(t *testing.T) {
	linear := &threaded{}
	web := chatSource{}
	c := &Conductor{sources: []Source{linear, web}, sessions: map[string]*sessionState{}}
	answer := InboundEvent{Login: "ada@acme.com", Ref: "chat_1", SourceKey: "linear:iss-1"}

	c.setTurnAsker("sess_1", linear, InboundEvent{Login: "ada@acme.com", Ref: "iss-1/s-1"}, "")
	c.mu.Lock()
	st := c.state("sess_1")
	assert.False(t, c.sameAsker(st, web, answer), "the web is another surface")
	assert.True(t, c.answersQuestion(st, web, answer), "but the asked person answers from it")
	bo := answer
	bo.Login = "bo@acme.com"
	assert.False(t, c.answersQuestion(st, web, bo), "somebody else's message is a turn of its own")
	slack := answer
	slack.SourceKey = "slack:C1/1.1"
	assert.False(t, c.answersQuestion(st, web, slack), "only a ticket's conversation")
	assert.False(t, c.answersQuestion(st, linear, answer), "from the web chat only")
	run := answer
	run.Routine = &RoutineRef{RoutineID: "rtn_1", RunID: "rrun_2"}
	assert.False(t, c.answersQuestion(st, web, run), "a routine's run answers nothing")
	c.mu.Unlock()

	// A routine's run asks its owner.
	c.setTurnAsker("sess_1", web, InboundEvent{
		Login: "ada@acme.com", Ref: "chat_1", Routine: &RoutineRef{RoutineID: "rtn_1", RunID: "rrun_1"},
	}, "")
	c.mu.Lock()
	assert.True(t, c.answersQuestion(c.state("sess_1"), web, answer))
	c.mu.Unlock()

	// A turn that ran as nobody is answered by nobody this way.
	c.setTurnAsker("sess_1", web, InboundEvent{Ref: "chat_1", Routine: &RoutineRef{RoutineID: "rtn_s", RunID: "rrun_3"}}, "")
	c.mu.Lock()
	assert.False(t, c.answersQuestion(c.state("sess_1"), web, answer))
	c.mu.Unlock()
}
