package linear

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// A session opened by an @mention, on a conductor with a host runtime, is a conversation: the
// assistant answers it on the host, with no task of its own, and what needs a machine it
// delegates to tasks that work beside it and after it. These are what the session shows of
// that, as the conductor hands it over.

func mentionRef() string { return SessionRef(fixtureIssue, fixtureTeam, "EME-5", mentionSession) }

// startHostTurn posts the placeholder of a turn the assistant answers: no playbook says it.
func (h *appHarness) startHostTurn(t *testing.T) string {
	t.Helper()
	id, err := h.src.Post(context.Background(), mentionRef(),
		conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	return id
}

// delegatedSays is something a task the assistant delegated says, as its relay hands it over.
func delegatedSays(typ, text, taskID, playbook string) conductor.Outbound {
	return conductor.Outbound{Type: typ, Text: text, TaskID: taskID, Speaker: conductor.Speaker{Playbook: playbook}}
}

func (h *appHarness) bodies() []string {
	var out []string
	for _, a := range h.activities() {
		content := a["content"].(map[string]any)
		body, _ := content["body"].(string)
		out = append(out, content["type"].(string)+": "+body)
	}
	return out
}

func (h *appHarness) stopMention(t *testing.T, delivery string) {
	t.Helper()
	body := h.fixture(t, "session_prompted_stop", func(d map[string]any) {
		d["agentSession"].(map[string]any)["id"] = mentionSession
		d["agentActivity"].(map[string]any)["id"] = "stop-" + delivery
	})
	require.Equal(t, http.StatusOK, h.deliver(body, delivery).Code)
	h.settle(t)
}

// The assistant's turn says "Working on it.", its tool calls and reasoning as the actions and
// thoughts any turn's are, and answers with a response — with no task footer and no task link,
// because it has no task.
func TestTheAssistantsAnswerIsAResponseWithNoTask(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	h.deliver(h.fixture(t, "session_created_mention"), "delivery-1")
	h.event(t)
	h.startHostTurn(t)
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.PostActivity(ctx, mentionRef(), toolCall("read", "go.mod")))
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.PostActivity(ctx, mentionRef(),
		conductor.Outbound{Type: conductor.OutActivity, Text: `{"kind":"reasoning","text":"Checking the module"}`}))
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "It is ten."})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"thought: " + acknowledgement,
		"thought: Working on it.",
		"action: ",
		"thought: Checking the module",
		"response: It is ten.",
	}, h.bodies())
	acts := h.activities()
	assert.Equal(t, map[string]any{"type": "action", "action": "Reading", "parameter": "go.mod"}, acts[2]["content"])
	assert.Equal(t, true, acts[2]["ephemeral"])
	assert.Equal(t, true, acts[3]["ephemeral"])
	assert.Empty(t, h.stub.callsOf("PodiumAgentSessionUpdate"), "no task, so no task link")
	assert.Zero(t, h.clock.pending(), "an answered turn with nothing delegated leaves no timer")
}

// Each task the assistant delegates is linked on the session, labelled so that two are told
// apart. What a task says after the assistant has answered is the task working, said as
// thoughts, and its own answer is a response naming its task.
func TestEachTaskTheAssistantDelegatesIsLinkedAndHeard(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "I've started two tasks."})
	require.NoError(t, err)
	// The conductor says each task's announcement once the turn has ended.
	h.clock.advance(ActivityThrottle)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ Working on this in a `fix` task: fix it", "task-aaaaaaaaaa", "fix"))
	require.NoError(t, err)
	h.clock.advance(ActivityThrottle)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ Working on this in a `fix` task: and this", "task-bbbbbbbbbb", "fix"))
	require.NoError(t, err)
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.Edit(ctx, mentionRef(), "x", delegatedSays(conductor.OutProgress, "⏳ running the tests", "task-aaaaaaaaaa", "fix")))
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFinal, "Opened #12.", "task-aaaaaaaaaa", "fix"))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"thought: Working on it.",
		"response: I've started two tasks.",
		"thought: Working on this in a `fix` task: fix it",
		"thought: Working on this in a `fix` task: and this",
		"thought: running the tests",
		"response: Opened #12.\n\n_Podium task task-aaaaaaaaaa_",
	}, h.bodies())
	acts := h.activities()
	assert.Equal(t, true, acts[2]["ephemeral"], "a task working after the answer is a thought, not a lasting response")

	var labels []string
	for _, c := range h.stub.callsOf("PodiumAgentSessionUpdate") {
		for _, u := range c.Vars["input"].(map[string]any)["addedExternalUrls"].([]any) {
			labels = append(labels, u.(map[string]any)["label"].(string))
		}
	}
	assert.Equal(t, []string{"Podium task: fix (task-aaa)", "Podium task: fix (task-bbb)"}, labels)
}

// A task said after the assistant's answer that it could not attach a file says it as part of
// the task's own answer: after its response, a response.
func TestATaskSaysWhatFollowsItsAnswerAsItsAnswer(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started."})
	require.NoError(t, err)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFailure, "The tests fail.", "task-1", "fix"))
	require.NoError(t, err)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "no artifact named `x.log` was produced", "task-1", "fix"))
	require.NoError(t, err)
	acts := h.activities()
	require.Len(t, acts, 4)
	assert.Equal(t, map[string]any{"type": "error", "body": "no artifact named `x.log` was produced"}, acts[3]["content"])
	assert.NotContains(t, acts[3], "ephemeral")
}

// A file a delegated task attaches is the task's, said as the type its answer was.
func TestADelegatedTasksFileIsSaidAsTheTask(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.stub.reply("PodiumUploadFile", map[string]any{"fileUpload": map[string]any{"success": false}})
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started."})
	require.NoError(t, err)
	require.NoError(t, h.src.Attach(ctx, mentionRef(), conductor.Attachment{
		Name: "huge.bin", Size: MaxUploadBytes + 1, Body: strings.NewReader(""), TaskID: "task-1",
	}))
	acts := h.activities()
	require.Len(t, acts, 3)
	content := acts[2]["content"].(map[string]any)
	assert.Equal(t, "response", content["type"])
	assert.Contains(t, content["body"], "https://podium.example/tasks/task-1", "it links the task it belongs to")
}

// The session is kept alive while a task the assistant delegated works, after the assistant
// has answered, and stops being kept alive when the task answers.
func TestADelegatedTaskIsKeptAliveAfterTheAssistantAnswered(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionWorking))
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started a task."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionDone))
	assert.Zero(t, h.clock.pending(), "nothing is working yet")

	h.clock.advance(ActivityThrottle)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ building", "task-1", "fix"))
	require.NoError(t, err)
	h.clock.advance(KeepAliveAfter)
	acts := h.activities()
	require.Len(t, acts, 4)
	assert.Equal(t, map[string]any{"type": "thought", "body": "Still working."}, acts[3]["content"],
		"said for the task, so the assistant's answer does not silence it")

	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFinal, "Built.", "task-1", "fix"))
	require.NoError(t, err)
	assert.Zero(t, h.clock.pending(), "nothing is working once the task has answered")
	h.clock.advance(3 * time.Hour)
	assert.Len(t, h.activities(), 5)
}

// A delegated interactive task's question is an elicitation, and waiting for the answer is not
// working. The answer reaches the task through the conductor, and the task working again is
// heard as it says so.
func TestADelegatedTasksQuestionIsAnElicitation(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started a task."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionDone))
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutQuestion, "Which branch?", "task-1", "fix"))
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionAwaiting))
	h.clock.advance(3 * time.Hour)
	assert.Equal(t, "elicitation: Which branch?", h.bodies()[2])
	assert.Len(t, h.activities(), 3, "an elicitation is the last word while nobody has answered")

	// The reply was injected; the conductor marks the session working and the task speaks.
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionWorking))
	require.NoError(t, h.src.Edit(ctx, mentionRef(), "x", delegatedSays(conductor.OutProgress, "⏳ on main", "task-1", "fix")))
	h.clock.advance(KeepAliveAfter)
	assert.Equal(t, []string{"thought: on main", "thought: Still working."}, h.bodies()[3:])
}

// A question asked while a delegated task works is not an error that would end the session
// under it: somebody who is not in Podium is told so with a thought.
func TestARefusalWhileADelegatedTaskWorksDoesNotEndTheSession(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started a task."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionDone))
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ building", "task-1", "fix"))
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionRefused))
	acts := h.activities()
	assert.Equal(t, map[string]any{"type": "thought", "body": refusedText}, acts[len(acts)-1]["content"])
}

// A pull request a delegated task opened is linked on the session, after the assistant's
// answer and with no task of the turn's own.
func TestAPullRequestADelegatedTaskOpenedIsLinked(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started a task."})
	require.NoError(t, err)
	require.NoError(t, h.src.LinkPullRequests(ctx, mentionRef(), []conductor.PullRequest{{URL: "https://github.com/acme/app/pull/7"}}))
	calls := h.stub.callsOf("PodiumAgentSessionUpdate")
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"addedExternalUrls": []any{
		map[string]any{"label": "Pull request", "url": "https://github.com/acme/app/pull/7"},
	}}, calls[0].Vars["input"])
}

// A mention is a question, and does not move the issue, whoever answers it.
func TestAnAssistantsTurnDoesNotMoveTheIssue(t *testing.T) {
	h := newAppHarness(t)
	h.issueIn(StateTypeBacklog)
	h.startHostTurn(t)
	require.NoError(t, h.src.React(context.Background(), mentionRef(), conductor.ReactionWorking))
	assert.Empty(t, h.stub.callsOf("PodiumIssueState"))
	assert.Empty(t, h.stub.callsOf("PodiumMoveIssue"))
}

// ---------------------------------------------------------------------------
// Stop on a conversation
// ---------------------------------------------------------------------------

// Stop while the assistant is thinking: one "Stopped.", and nothing the dying turn says.
func TestAStopWhileTheAssistantThinks(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionWorking))
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.PostActivity(ctx, mentionRef(), toolCall("read", "go.mod")))

	h.stopMention(t, "delivery-1")
	assert.Equal(t, []string{SessionKey(mentionSession)}, h.stopped())

	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "Stopped."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionFailed))
	h.clock.advance(3 * time.Hour)
	assert.Equal(t, []string{"thought: Working on it.", "action: ", "response: Stopped."}, h.bodies())
	assert.Zero(t, h.clock.pending())
}

// Stop while a task the assistant delegated runs: the conductor stops it, announcing its task,
// and nothing it says as it dies — its cancellation, its progress, its files — is said.
func TestAStopWhileADelegatedTaskRuns(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started a task."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionDone))
	h.clock.advance(ActivityThrottle)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ building", "task-1", "fix"))
	require.NoError(t, err)

	h.stopAnswer = func(key string) (bool, error) {
		// The conductor announces the task it stops, here as on every conductor.
		h.src.StoppedElsewhere(key, "task-1")
		return true, nil
	}
	h.stopMention(t, "delivery-1")

	stopped := delegatedSays(conductor.OutFailure, "This was cancelled.", "task-1", "fix")
	stopped.CancelReason = StopReason
	_, err = h.src.Post(ctx, mentionRef(), stopped)
	require.NoError(t, err)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ late", "task-1", "fix"))
	require.NoError(t, err)
	require.NoError(t, h.src.Attach(ctx, mentionRef(), conductor.Attachment{Name: "a.txt", Size: 1, Body: strings.NewReader("a"), TaskID: "task-1"}))
	h.clock.advance(3 * time.Hour)
	assert.Equal(t, []string{
		"thought: Working on it.", "response: Started a task.", "thought: building", "response: Stopped.",
	}, h.bodies())
	assert.Zero(t, h.clock.pending())

	// The next question is answered, and the stopped task stays unsaid even then.
	h.startHostTurn(t)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFinal, "late", "task-1", "fix"))
	require.NoError(t, err)
	_, err = h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Again."})
	require.NoError(t, err)
	assert.Equal(t, []string{"thought: Working on it.", "response: Again."}, h.bodies()[4:])
}

// Stop with two delegated tasks running, one the source has heard from and one it has not:
// both are refused for good, whichever announcement arrives, and "Stopped." is the last word.
func TestAStopWithTwoDelegatedTasks(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	_, err := h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Started two tasks."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, mentionRef(), conductor.ReactionDone))
	h.clock.advance(ActivityThrottle)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ one", "task-1", "fix"))
	require.NoError(t, err)

	h.stopAnswer = func(key string) (bool, error) {
		h.src.StoppedElsewhere(key, "task-1")
		h.src.StoppedElsewhere(key, "task-2")
		return true, nil
	}
	h.stopMention(t, "delivery-1")
	before := len(h.activities())

	for _, id := range []string{"task-1", "task-2"} {
		out := delegatedSays(conductor.OutFailure, "This was cancelled.", id, "fix")
		out.CancelReason = StopReason
		_, err = h.src.Post(ctx, mentionRef(), out)
		require.NoError(t, err)
		require.NoError(t, h.src.PostActivity(ctx, mentionRef(), delegatedSays(conductor.OutActivity,
			`{"kind":"reasoning","text":"late"}`, id, "fix")))
	}
	h.startHostTurn(t)
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFinal, "late", "task-2", "fix"))
	require.NoError(t, err)
	assert.Equal(t, "response: Stopped.", h.bodies()[before-1])
	assert.Equal(t, []string{"thought: Working on it."}, h.bodies()[before:],
		"neither task is heard again, not even after the next turn starts")
}

// A conductor that did not hear the Stop, following one of the delegated tasks, is told the
// task by id: it refuses that task and leaves the turn alone, which is announced on its own.
func TestAStopHeardElsewhereSilencesOneDelegatedTask(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startHostTurn(t)
	h.clock.advance(ActivityThrottle)
	_, err := h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutProgress, "⏳ one", "task-1", "fix"))
	require.NoError(t, err)

	h.src.StoppedElsewhere(SessionKey(mentionSession), "task-1")
	_, err = h.src.Post(ctx, mentionRef(), delegatedSays(conductor.OutFinal, "late", "task-1", "fix"))
	require.NoError(t, err)
	_, err = h.src.Post(ctx, mentionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "The assistant's answer."})
	require.NoError(t, err)
	assert.Equal(t, []string{"thought: Working on it.", "thought: one", "response: The assistant's answer."}, h.bodies())
}

// A session the assistant answers starts each turn with the placeholder, so a Stop in one turn
// does not silence the next, and a playbook's turn in a delegated session is still a task turn.
func TestOnlyAPlaceholderNoPlaybookSaysIsAnAssistantsTurn(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	h.startTurn(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done.", TaskID: "task-1",
		Speaker: conductor.Speaker{Playbook: "linear-work"}})
	require.NoError(t, err)
	calls := h.stub.callsOf("PodiumAgentSessionUpdate")
	require.Len(t, calls, 1)
	assert.Equal(t, "Podium task", calls[0].Vars["input"].(map[string]any)["addedExternalUrls"].([]any)[0].(map[string]any)["label"],
		"a turn's own task is the session's task")
}

// A follow-up in a session opened by a mention is not a delegation, and a /playbook prefix in
// what the person wrote reaches the conductor as they wrote it.
func TestAMentionsFollowUpIsStillAQuestionAndKeepsItsPrefix(t *testing.T) {
	h := newAppHarness(t)
	body := h.fixture(t, "session_prompted_reply", func(d map[string]any) {
		d["agentSession"].(map[string]any)["id"] = mentionSession
		d["agentSession"].(map[string]any)["sourceMetadata"] = map[string]any{"type": "comment"}
		d["agentActivity"].(map[string]any)["content"].(map[string]any)["body"] = "/fix the flaky test"
	})
	h.deliver(body, "delivery-1")
	ev := h.event(t)
	assert.False(t, ev.Delegated)
	assert.Equal(t, "/fix the flaky test", ev.Text, "the conductor reads the prefix, not the source")
	assert.Empty(t, ev.Playbook)

	mention := h.fixture(t, "session_created_mention", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["content"].(map[string]any)["body"] = "@podium /fix the flaky test"
	})
	h.deliver(mention, "delivery-2")
	ev = h.event(t)
	assert.Equal(t, "/fix the flaky test", ev.Text)
	assert.Equal(t, "@podium /fix the flaky test", ev.RecordText)
}
