//go:build integration

package conductor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// What answers a Linear agent session (conductor.go, conversational): a delegated issue runs a
// playbook as a task, an @mention is a question the assistant answers where there is a host
// runtime, and a /playbook prefix in a person's words runs that playbook for the turn.
//
// The assistant is a chat host here (chathost_cancel_integration_test.go): a fake runtime child
// is driven by its event's environment, which the conductor honours for the dev source only.

// answeringChatHost answers every turn at once with the same words, and records the prompts.
type answeringChatHost struct {
	answer string
	mu     sync.Mutex
	asked  []string
}

func (a *answeringChatHost) Open(context.Context, chathost.Location) error { return nil }
func (a *answeringChatHost) Close(string) error                            { return nil }
func (a *answeringChatHost) Steer(context.Context, string, string, string) error {
	return nil
}

func (a *answeringChatHost) Answer(_ context.Context, t chathost.Turn) (chathost.Result, error) {
	a.mu.Lock()
	a.asked = append(a.asked, t.Prompt)
	a.mu.Unlock()
	return chathost.Result{Text: a.answer, Outcome: "succeeded"}, nil
}

func (a *answeringChatHost) prompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// withAssistant gives the conductor a host runtime whose assistant is host.
func withAssistant(t *testing.T, host conductor.ChatHost) func(*conductor.Options) {
	return func(o *conductor.Options) {
		o.Host = hostRuntime(t, "sk-test")
		o.ChatHost = host
		o.ChatHostURL = "http://127.0.0.1:1"
		o.ChatHostRender = func(context.Context, string) (string, error) { return "You are the test assistant.", nil }
	}
}

// mentionPrompt is a prompt in a session opened by @mentioning the app.
func mentionPrompt(session, activity, text string) conductor.InboundEvent {
	return sessionPrompt(session, activity, text)
}

// delegatedPrompt is a prompt in a session opened by delegating the issue to the app.
func delegatedPrompt(session, activity, text string) conductor.InboundEvent {
	ev := sessionPrompt(session, activity, text)
	ev.Ref += "/delegated"
	ev.Delegated = true
	return ev
}

func briefPlaybooks(t *testing.T, fake *fakePodium) []string {
	var out []string
	for _, spec := range fake.Specs() {
		out = append(out, decodeBrief(t, spec).Playbook.Name)
	}
	return out
}

// A delegation is a piece of work: it runs the linear: true playbook as a task even on a
// conductor with an assistant, a follow-up stays with it, and a /playbook in a follow-up runs
// that playbook for that turn alone.
func TestALinearDelegationRunsAPlaybookEvenWithAnAssistant(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "done in "+taskID)}
	}
	src := linearAppSource()
	t.Cleanup(src.Close)
	assistant := &answeringChatHost{answer: "I am the assistant."}
	startWith(t, st, fake, src, withAssistant(t, assistant))

	ctx := context.Background()
	first := delegatedPrompt("s-30", "a-30", "ENG-1 — Fix the build")
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the delegation's task", func() bool { return len(fake.Specs()) == 1 })
	waitFor(t, 30*time.Second, "its answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 1 })

	require.NoError(t, src.Send(ctx, delegatedPrompt("s-30", "a-31", "/dogfood run the e2e suite")))
	waitFor(t, 30*time.Second, "the named playbook's task", func() bool { return len(fake.Specs()) == 2 })
	waitFor(t, 30*time.Second, "its answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 2 })

	require.NoError(t, src.Send(ctx, delegatedPrompt("s-30", "a-32", "and the docs")))
	waitFor(t, 30*time.Second, "the follow-up's task", func() bool { return len(fake.Specs()) == 3 })

	assert.Equal(t, []string{"coder", "dogfood", "coder"}, briefPlaybooks(t, fake),
		"the linear: true playbook, the one named for a turn, and the session's own again")
	assert.Equal(t, "run the e2e suite", decodeBrief(t, fake.Specs()[1]).Instruction, "without its prefix")
	assert.Empty(t, assistant.prompts(), "the assistant is never asked")
	sess, err := st.GetSessionByKey(ctx, first.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "coder", sess.Playbook, "a playbook named for one turn does not become the session's")
}

// A mention is a question: on a conductor with an assistant it is answered on the host, with no
// task, and its follow-ups are too. The chat shows the answers.
func TestALinearMentionIsAnsweredByTheAssistant(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := linearAppSource()
	t.Cleanup(src.Close)
	assistant := &answeringChatHost{answer: "It is ten."}
	startWith(t, st, fake, src, withAssistant(t, assistant))

	ctx := context.Background()
	ev := mentionPrompt("s-31", "a-33", "what time is it?")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 1 })
	require.NoError(t, src.Send(ctx, mentionPrompt("s-31", "a-34", "and tomorrow?")))
	waitFor(t, 30*time.Second, "the second answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 2 })

	assert.Empty(t, fake.Specs(), "the assistant answers on the host and starts no task of its own")
	assert.Equal(t, []string{"what time is it?", "and tomorrow?"}, assistant.prompts())
	finals := posts(src.Records(), conductor.OutFinal)
	assert.Equal(t, "It is ten.", finals[0].Text)
	assert.Empty(t, finals[0].Playbook, "the assistant is nobody's playbook")
	placeholders := 0
	for _, r := range posts(src.Records(), conductor.OutProgress) {
		if r.Text == conductor.Placeholder {
			placeholders++
			assert.Empty(t, r.Playbook, "an assistant's turn starts with a placeholder no playbook says")
		}
	}
	assert.Equal(t, 2, placeholders)

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "It is ten.", answerRow(t, st, chat.ID).Text)
	sess, err := st.GetSessionByKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Empty(t, sess.Playbook, "a conversation runs no playbook")
}

// A /playbook in a mention, where the assistant answers, runs that playbook as the
// conversation's task without asking the assistant, and the task is the session's to stop.
func TestALinearMentionThatNamesAPlaybookDelegatesIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	src := linearAppSource()
	t.Cleanup(src.Close)
	assistant := &answeringChatHost{answer: "unused"}
	startWith(t, st, fake, src, withAssistant(t, assistant))

	ctx := context.Background()
	ev := mentionPrompt("s-32", "a-35", "/dogfood run the e2e suite")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.Specs()) == 1 })
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(st, ev.SourceKey) == store.TurnSucceeded })

	assert.Empty(t, assistant.prompts(), "the person named the playbook; there was nothing to ask")
	brief := decodeBrief(t, fake.Specs()[0])
	assert.Equal(t, "dogfood", brief.Playbook.Name)
	assert.Equal(t, "run the e2e suite", brief.Instruction)
	dlgs := delegationsOf(t, st, ev.SourceKey)
	require.Len(t, dlgs, 1)
	assert.Equal(t, "dogfood", dlgs[0].Playbook)
	assert.Equal(t, store.TurnRunning, dlgs[0].Status, "the conversation owns it after the turn")
	assert.Equal(t, []string{testSpeaker}, startedBy(t, st, ev.SourceKey), "it is the person's turn")
	assert.Contains(t, reactions(src.Records()), string(conductor.ReactionDone))

	// An unknown name is the person's words, and the assistant's to read.
	require.NoError(t, src.Send(ctx, mentionPrompt("s-32", "a-36", "/shrug what now")))
	waitFor(t, 30*time.Second, "the assistant", func() bool { return len(assistant.prompts()) == 1 })
	assert.Equal(t, []string{"/shrug what now"}, assistant.prompts())
}
