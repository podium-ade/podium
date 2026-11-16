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
