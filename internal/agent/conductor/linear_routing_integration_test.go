//go:build integration

package conductor_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// What answers a Linear agent session (conductor.go, conversational): where there is a host
// runtime, a delegated issue runs the playbook its Linear rules claim and is otherwise the
// assistant's, as an @mention is, and a /playbook prefix in a person's words runs that playbook
// for the turn.
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

// delegationOpening is a delegation's opening prompt, with the issue its rules match.
func delegationOpening(session, activity, text, team string, labels ...string) conductor.InboundEvent {
	ev := delegatedPrompt(session, activity, text)
	ev.DelegatedIssue = &conductor.DelegatedIssue{Team: team, Labels: labels}
	return ev
}

// A delegation no playbook's rules claim is the assistant's, as a mention is: it is told the
// issue was delegated, starts no task of its own, and the follow-ups are its too. A /playbook
// in a follow-up runs that playbook as the conversation's task.
func TestALinearDelegationNoRuleClaimsIsTheAssistants(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	src := linearAppSource()
	t.Cleanup(src.Close)
	assistant := &answeringChatHost{answer: "Which repository?"}
	startWith(t, st, fake, src, withAssistant(t, assistant))

	ctx := context.Background()
	first := delegationOpening("s-30", "a-30", "ENG-1 — Fix the build", "ENG", "bug")
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the assistant's answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 1 })
	require.NoError(t, src.Send(ctx, delegatedPrompt("s-30", "a-31", "podium")))
	waitFor(t, 30*time.Second, "the second answer", func() bool { return len(posts(src.Records(), conductor.OutFinal)) == 2 })

	asked := assistant.prompts()
	require.Len(t, asked, 2)
	assert.True(t, strings.HasPrefix(asked[0], "This Linear issue was delegated to you"), asked[0])
	assert.True(t, strings.HasSuffix(asked[0], "ENG-1 — Fix the build"), "the issue follows")
	assert.Equal(t, "podium", asked[1], "a follow-up is the person's words")
	assert.Empty(t, fake.Specs(), "the assistant starts no task of its own")
	assert.Empty(t, posts(src.Records(), conductor.OutFinal)[0].Playbook, "the assistant is nobody's playbook")
	sess, err := st.GetSessionByKey(ctx, first.SourceKey)
	require.NoError(t, err)
	assert.Empty(t, sess.Playbook, "a conversation runs no playbook")

	require.NoError(t, src.Send(ctx, delegatedPrompt("s-30", "a-32", "/dogfood run the e2e suite")))
	waitFor(t, 30*time.Second, "the named playbook's task", func() bool { return len(fake.Specs()) == 1 })
	brief := decodeBrief(t, fake.Specs()[0])
	assert.Equal(t, "dogfood", brief.Playbook.Name)
	assert.Equal(t, "run the e2e suite", brief.Instruction, "without its prefix")
	assert.Len(t, assistant.prompts(), 2, "the person named the playbook; there was nothing to ask")
}

// A delegation exactly one playbook's rules claim runs that playbook directly, as the
// conversation's task: the assistant is not asked, the task is the session's delegation, and
// the issue is its instruction.
func TestALinearDelegationARuleClaimsRunsThatPlaybook(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	src := linearAppSource()
	t.Cleanup(src.Close)
	assistant := &answeringChatHost{answer: "unused"}
	startWith(t, st, fake, src, withAssistant(t, assistant))

	ctx := context.Background()
	ev := delegationOpening("s-34", "a-40", "OPS-7 — The page is blank", "OPS")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.Specs()) == 1 })
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(st, ev.SourceKey) == store.TurnSucceeded })

	assert.Empty(t, assistant.prompts(), "the rules chose the playbook; there was nothing to ask")
	brief := decodeBrief(t, fake.Specs()[0])
	assert.Equal(t, "looker", brief.Playbook.Name, "looker claims team OPS")
	assert.Equal(t, "OPS-7 — The page is blank", brief.Instruction)
	dlgs := delegationsOf(t, st, ev.SourceKey)
	require.Len(t, dlgs, 1)
	assert.Equal(t, "looker", dlgs[0].Playbook)
	assert.Equal(t, store.TurnRunning, dlgs[0].Status, "the conversation owns it after the turn")

	// Its follow-up is the assistant's, which sees the task running.
	require.NoError(t, src.Send(ctx, delegatedPrompt("s-34", "a-41", "how is it going?")))
	waitFor(t, 30*time.Second, "the assistant", func() bool { return len(assistant.prompts()) == 1 })
	assert.Len(t, fake.Specs(), 1, "the follow-up started nothing by itself")
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
	awaitOutcome(t, src, conductor.ReactionDone)

	// An unknown name is the person's words, and the assistant's to read.
	require.NoError(t, src.Send(ctx, mentionPrompt("s-32", "a-36", "/shrug what now")))
	waitFor(t, 30*time.Second, "the assistant", func() bool { return len(assistant.prompts()) == 1 })
	assert.Equal(t, []string{"/shrug what now"}, assistant.prompts())
}

// Stop while the assistant is thinking ends its turn, and the turn is recorded cancelled.
func TestAStopWhileTheAssistantThinksOnALinearSession(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := linearAppSource()
	t.Cleanup(src.Close)
	host := &blockingChatHost{}
	r := startWith(t, st, fake, src, withAssistant(t, host))

	ctx := context.Background()
	ev := mentionPrompt("s-33", "a-37", "think hard")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 20*time.Second, "the assistant to be thinking", func() bool {
		started, _ := host.counts()
		return started == 1
	})

	stopped, err := r.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	assert.True(t, stopped)
	waitFor(t, 20*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})
	_, interrupted := host.counts()
	assert.Equal(t, 1, interrupted)
	assert.Empty(t, fake.Cancels(), "there was no task")
}

// stopDelegated starts n delegated tasks in one mention session, one playbook each, and stops
// the session. It returns the source, the task ids and what the Stop said.
func stopDelegated(t *testing.T, playbooks ...string) (*fakesource.Source, *fakePodium, []string, bool) {
	t.Helper()
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	fake.terminal = podiumv1.TaskStatus_TASK_STATUS_CANCELLED
	src := linearAppSource()
	t.Cleanup(src.Close)
	r := startWith(t, st, fake, src, withAssistant(t, &answeringChatHost{answer: "unused"}))

	ctx := context.Background()
	key := ""
	for i, name := range playbooks {
		ev := mentionPrompt("s-34", "a-4"+string(rune('0'+i)), "/"+name+" do the thing")
		key = ev.SourceKey
		require.NoError(t, src.Send(ctx, ev))
		waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.TaskIDs()) == i+1 })
	}
	waitFor(t, 30*time.Second, "the turns", func() bool { return len(startedBy(t, st, key)) == len(playbooks) })
	stopped, err := r.cond.CancelSessionTurn(ctx, key, stopReason)
	require.NoError(t, err)
	waitFor(t, 30*time.Second, "every delegation to end", func() bool {
		for _, d := range delegationsOf(t, st, key) {
			if d.Status == store.TurnRunning {
				return false
			}
		}
		return true
	})
	return src, fake, fake.TaskIDs(), stopped
}

// Stop while a task the conversation delegated runs: the task is announced, cancelled for the
// stop's reason, and what it posts as it ends carries that reason, so the source refuses it.
func TestAStopWhileADelegatedTaskRunsOnALinearSession(t *testing.T) {
	src, fake, tasks, stopped := stopDelegated(t, "dogfood")
	assert.True(t, stopped, "a running delegated task is something to stop")
	cancels := fake.Cancels()
	require.Len(t, cancels, 1)
	assert.Equal(t, tasks[0], cancels[0].GetTaskId())
	assert.Equal(t, stopReason, cancels[0].GetReason())
	assert.Equal(t, tasks, heardStopped(src), "the source is told the task it stopped")
	for _, f := range posts(src.Records(), conductor.OutFailure) {
		assert.Equal(t, stopReason, f.CancelReason, "everything it says about the cancel is the stop's")
	}
}

// Stop with two delegated tasks: each is announced and cancelled.
func TestAStopWithTwoDelegatedTasksOnALinearSession(t *testing.T) {
	src, fake, tasks, stopped := stopDelegated(t, "dogfood", "looker")
	assert.True(t, stopped)
	var cancelled []string
	for _, c := range fake.Cancels() {
		cancelled = append(cancelled, c.GetTaskId())
		assert.Equal(t, stopReason, c.GetReason())
	}
	assert.ElementsMatch(t, tasks, cancelled)
	assert.ElementsMatch(t, tasks, heardStopped(src))
}

func heardStopped(src *fakesource.Source) []string {
	var out []string
	for _, r := range src.Records() {
		if r.Action == fakesource.ActionStoppedElsewhere && strings.HasPrefix(r.Ref, "linear:session:") {
			out = append(out, r.Text)
		}
	}
	return out
}
