package linear

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// ---------------------------------------------------------------------------
// tool calls and reasoning
// ---------------------------------------------------------------------------

func TestAToolCallIsAShortActionAndNeverItsContents(t *testing.T) {
	long := strings.Repeat("a", 300)
	tests := []struct {
		name string
		doc  string
		want ActivityContent
		ok   bool
	}{
		{"a file read", `{"kind":"tool","tool":"read","status":"completed","input":"{\"filePath\":\"internal/agent/linear/app.go\"}","output":"package linear"}`,
			ActivityContent{Type: ActivityAction, Action: "Reading", Parameter: "internal/agent/linear/app.go"}, true},
		{"a command's first line", `{"kind":"tool","tool":"bash","status":"completed","input":"{\"command\":\"make test\\nmake lint\",\"description\":\"run\"}"}`,
			ActivityContent{Type: ActivityAction, Action: "Running", Parameter: "make test"}, true},
		{"a write names the file and not what was written", `{"kind":"tool","tool":"write","status":"completed","input":"{\"filePath\":\"notes.md\",\"content\":\"PRIVATE\"}"}`,
			ActivityContent{Type: ActivityAction, Action: "Writing", Parameter: "notes.md"}, true},
		{"a search", `{"kind":"tool","tool":"grep","status":"completed","input":"{\"pattern\":\"func Post\"}"}`,
			ActivityContent{Type: ActivityAction, Action: "Searching for", Parameter: "func Post"}, true},
		{"a failed call says so", `{"kind":"tool","tool":"bash","status":"error","input":"{\"command\":\"go build ./...\"}","error":"exit 1"}`,
			ActivityContent{Type: ActivityAction, Action: "Running", Parameter: "go build ./...", Result: "Failed"}, true},
		{"a tool nobody told this source about is its name alone", `{"kind":"tool","tool":"linear_create_issue","status":"completed","input":"{\"title\":\"PRIVATE\",\"token\":\"x\"}"}`,
			ActivityContent{Type: ActivityAction, Action: "Using", Parameter: "linear_create_issue"}, true},
		{"a long parameter is cut", `{"kind":"tool","tool":"read","status":"completed","input":"{\"filePath\":\"` + long + `\"}"}`,
			ActivityContent{Type: ActivityAction, Action: "Reading", Parameter: strings.Repeat("a", maxParameterRunes-1) + "…"}, true},
		{"input that is not an object is not shown", `{"kind":"tool","tool":"read","status":"completed","input":"PRIVATE"}`,
			ActivityContent{Type: ActivityAction, Action: "Reading"}, true},
		{"reasoning is its first line", `{"kind":"reasoning","text":"**Checking the tests**\n\nThe build is red because …"}`,
			ActivityContent{Type: ActivityThought, Body: "Checking the tests"}, true},
		{"empty reasoning is nothing", `{"kind":"reasoning","text":"  "}`, ActivityContent{}, false},
		{"a kind this source does not know", `{"kind":"plan"}`, ActivityContent{}, false},
		{"not JSON", `nope`, ActivityContent{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := actionFor(tc.doc)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got.Parameter, "PRIVATE")
		})
	}
}

// A burst of tool calls is one action now and the newest of the rest when the throttle's
// window closes, not one request each.
func TestABurstOfToolCallsIsThrottledAndTheNewestWins(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	h.clock.advance(ActivityThrottle)

	for _, path := range []string{"a.go", "b.go", "c.go", "d.go"} {
		require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("read", path)))
	}
	acts := h.activities()
	require.Len(t, acts, 2, "the placeholder, then the first call")
	assert.Equal(t, map[string]any{"type": "action", "action": "Reading", "parameter": "a.go"}, acts[1]["content"])
	assert.Equal(t, true, acts[1]["ephemeral"])

	h.clock.advance(ActivityThrottle)
	acts = h.activities()
	require.Len(t, acts, 3, "what was held is posted when the window closes")
	assert.Equal(t, "d.go", acts[2]["content"].(map[string]any)["parameter"], "the newest wins")
	h.clock.advance(3 * ActivityThrottle)
	assert.Len(t, h.activities(), 3, "once")
}

// A progress edit shares the window with the tool calls.
func TestAProgressEditIsANewThoughtThrottled(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	id := h.startTurn(t)

	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ one"}))
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("read", "go.mod")))
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ two"}))
	assert.Len(t, h.activities(), 1, "inside the window everything is held")
	h.clock.advance(ActivityThrottle)
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "thought", "body": "two"}, acts[1]["content"])
	assert.Equal(t, true, acts[1]["ephemeral"])
}

// What the throttle held is older than the answer, and is never said after it.
func TestTheAnswerDropsWhatTheThrottleHeld(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("read", "go.mod")))
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done."})
	require.NoError(t, err)
	h.clock.advance(time.Hour)
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, "response", acts[1]["content"].(map[string]any)["type"])
	assert.Zero(t, h.clock.pending(), "an answered turn leaves no timer behind")

	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("read", "late.go")))
	assert.Len(t, h.activities(), 2, "a tool call after the answer would reopen the session")
}

// Progress said after the answer — a file that was not produced — is part of the answer.
func TestProgressAfterTheAnswerIsSaidAsTheAnswer(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done."})
	require.NoError(t, err)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutProgress, Text: "no artifact named `x.png` was produced"})
	require.NoError(t, err)
	acts := h.activities()
	require.Len(t, acts, 3)
	assert.Equal(t, map[string]any{"type": "response", "body": "no artifact named `x.png` was produced"}, acts[2]["content"])
	assert.NotContains(t, acts[2], "ephemeral")
}

// A turn the poller started has no session to draw activity in.
func TestAPolledTurnIsToldNoActivity(t *testing.T) {
	h := newAppHarness(t)
	require.NoError(t, h.src.PostActivity(context.Background(), Ref(fixtureIssue, fixtureTeam, "EME-5"), toolCall("read", "go.mod")))
	assert.Empty(t, h.stub.seen())
}

// ---------------------------------------------------------------------------
// links
// ---------------------------------------------------------------------------

// The placeholder has no task; the first thing said once the task exists links it, once.
func TestTheTaskIsLinkedOnTheSessionOnce(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	id := h.startTurn(t)
	assert.Empty(t, h.stub.callsOf("PodiumAgentSessionUpdate"), "the placeholder comes before the task")

	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ one", TaskID: "task-1"}))
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutActivity, Text: `{"kind":"reasoning","text":"hm"}`, TaskID: "task-1"}))
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done.", TaskID: "task-1"})
	require.NoError(t, err)
	calls := h.stub.callsOf("PodiumAgentSessionUpdate")
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"addedExternalUrls": []any{
		map[string]any{"label": "Podium task", "url": "https://podium.example/tasks/task-1"},
	}}, calls[0].Vars["input"])

	// The next turn in the session is another task, and another link.
	h.startTurn(t)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Again.", TaskID: "task-2"})
	require.NoError(t, err)
	assert.Len(t, h.stub.callsOf("PodiumAgentSessionUpdate"), 2)
}

func TestAConductorWithNoWebAddressLinksNoTask(t *testing.T) {
	h := newAppHarness(t, func(o *AppOptions) { o.TaskURL = func(string) string { return "" } })
	_, err := h.src.Post(context.Background(), sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done.", TaskID: "task-1"})
	require.NoError(t, err)
	assert.Empty(t, h.stub.callsOf("PodiumAgentSessionUpdate"))
}

// A link that did not land costs a click: the answer still goes out.
func TestALinkThatFailsDoesNotStopTheAnswer(t *testing.T) {
	h := newAppHarness(t)
	h.stub.on("PodiumAgentSessionUpdate", func(map[string]any) stubReply {
		return stubReply{Message: "Entity not found", Code: "BAD_USER_INPUT"}
	})
	_, err := h.src.Post(context.Background(), sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done.", TaskID: "task-1"})
	require.NoError(t, err)
	assert.Len(t, h.activities(), 1)
}

func TestThePullRequestsATurnNamedAreLinkedOnTheSession(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	pr := conductor.PullRequest{URL: "https://github.com/acme/app/pull/12", Owner: "acme", Repo: "app", Number: 12}
	require.NoError(t, h.src.LinkPullRequests(ctx, sessionRef(), []conductor.PullRequest{pr}))
	require.NoError(t, h.src.LinkPullRequests(ctx, sessionRef(), []conductor.PullRequest{pr}))
	calls := h.stub.callsOf("PodiumAgentSessionUpdate")
	require.Len(t, calls, 1, "a pull request named twice is linked once")
	assert.Equal(t, map[string]any{"addedExternalUrls": []any{
		map[string]any{"label": "Pull request", "url": "https://github.com/acme/app/pull/12"},
	}}, calls[0].Vars["input"])

	require.NoError(t, h.src.LinkPullRequests(ctx, Ref(fixtureIssue, fixtureTeam, "EME-5"), []conductor.PullRequest{pr}))
	assert.Len(t, h.stub.callsOf("PodiumAgentSessionUpdate"), 1, "a polled turn has no session to link on")
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

// Stop in the middle of a task: the conductor is asked to stop the session, the session is
// closed with one response, and nothing the turn says as it unwinds is posted.
func TestAStopMidTaskStopsTheTurnAndClosesTheSession(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	id := h.startTurn(t)
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ building", TaskID: "task-1"}))
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("bash", "make test")))
	before := len(h.activities())

	require.Equal(t, http.StatusOK, h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1").Code)
	h.noEvent(t)
	assert.Equal(t, []string{SessionKey(delegationSession)}, h.stopped())
	acts := h.activities()
	require.Len(t, acts, before+1, "what the throttle held is not said after the stop")
	assert.Equal(t, map[string]any{"type": "response", "body": "Stopped."}, acts[before]["content"])
	assert.NotContains(t, acts[before], "ephemeral")
	assert.Equal(t, 1.0, h.events(t, EventStop))

	// The conductor unwinds the cancelled turn and hands over what it says about it.
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "This was cancelled. Task `task-1`.", TaskID: "task-1"})
	require.NoError(t, err)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ late", TaskID: "task-1"}))
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("read", "late.go")))
	require.NoError(t, h.src.Attach(ctx, sessionRef(), conductor.Attachment{Name: "a.txt", Size: 1, Body: strings.NewReader("a"), TaskID: "task-1"}))
	require.NoError(t, h.src.LinkPullRequests(ctx, sessionRef(), []conductor.PullRequest{{URL: "https://github.com/acme/app/pull/1"}}))
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionWorking))
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionFailed))
	h.clock.advance(time.Hour)
	assert.Len(t, h.activities(), before+1, "nothing more is said for the stopped turn")
	assert.Len(t, h.stub.callsOf("PodiumAgentSessionUpdate"), 1, "only the task, linked before the stop")
	assert.Empty(t, h.stub.seenUploads())

	// The next turn in the session is somebody asking again, and is answered.
	h.startTurn(t)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Answer.", TaskID: "task-2"})
	require.NoError(t, err)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "late", TaskID: "task-1"})
	require.NoError(t, err)
	acts = h.activities()
	require.Len(t, acts, before+3)
	assert.Equal(t, "Answer.\n\n_Podium task task-2_", acts[before+2]["content"].(map[string]any)["body"],
		"and the stopped task's last words stay unsaid even then")
}

// The task a stop found may not have said anything to the source yet: whatever arrives from
// it while the session is stopped is the stopped turn's.
func TestAStopBeforeTheTaskSpokeStillSilencesIt(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "This was cancelled.", TaskID: "task-9"})
	require.NoError(t, err)
	h.startTurn(t)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "late", TaskID: "task-9"})
	require.NoError(t, err)
	var bodies []string
	for _, a := range h.activities() {
		bodies = append(bodies, a["content"].(map[string]any)["body"].(string))
	}
	assert.Equal(t, []string{"Working on it.", "Stopped.", "Working on it."}, bodies)
}

// A Stop with nothing running — or in a session this source and the conductor have never
// seen, which the conductor answers the same way — still closes the session, in words.
func TestAStopWithNothingRunningSaysSo(t *testing.T) {
	h := newAppHarness(t)
	h.stopAnswer = func(string) (bool, error) { return false, nil }
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	acts := h.activities()
	require.Len(t, acts, 1)
	assert.Equal(t, map[string]any{"type": "response", "body": idleStopText}, acts[0]["content"])

	// Nothing was stopped, so nothing is silenced.
	_, err := h.src.Post(context.Background(), sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Hello."})
	require.NoError(t, err)
	assert.Len(t, h.activities(), 2)
}

// Each Stop is answered, as Linear asks; the conductor says whether there was still anything
// to stop. A redelivered Stop is the same Stop.
func TestAStopPressedTwiceIsAnsweredTwice(t *testing.T) {
	h := newAppHarness(t)
	h.startTurn(t)
	running := true
	h.stopAnswer = func(string) (bool, error) {
		was := running
		running = false
		return was, nil
	}
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-2")
	h.settle(t)
	second := h.fixture(t, "session_prompted_stop", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["id"] = "stop-again"
	})
	h.deliver(second, "delivery-3")
	h.settle(t)

	assert.Len(t, h.stopped(), 2, "the redelivery is not a second Stop")
	var bodies []string
	for _, a := range h.activities()[1:] {
		bodies = append(bodies, a["content"].(map[string]any)["body"].(string))
	}
	assert.Equal(t, []string{"Stopped.", idleStopText}, bodies)
	assert.Equal(t, 1.0, h.deliveries(t, TypeAgentSession, DeliveryDuplicate))
}

func TestAStopThatFailsClosesTheSessionWithAnError(t *testing.T) {
	h := newAppHarness(t)
	h.startTurn(t)
	h.stopAnswer = func(string) (bool, error) { return false, errors.New("the control plane is down") }
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "error", "body": cantStopText}, acts[1]["content"])
	assert.NotContains(t, acts[1]["content"].(map[string]any)["body"], "control plane", "the reason is the log's")
	assert.Contains(t, h.log.String(), "the control plane is down")

	_, err := h.src.Post(context.Background(), sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "late"})
	require.NoError(t, err)
	assert.Len(t, h.activities(), 2, "the person asked for nothing more to happen")
}

// A source with no way to stop a turn says it could not, rather than claiming it did.
func TestASourceWithNoStopSaysItCouldNot(t *testing.T) {
	h := newAppHarness(t, func(o *AppOptions) { o.Stop = nil })
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	acts := h.activities()
	require.Len(t, acts, 1)
	assert.Equal(t, "error", acts[0]["content"].(map[string]any)["type"])
}

// ---------------------------------------------------------------------------
// keep-alive
// ---------------------------------------------------------------------------

func TestALongTurnIsKeptAlive(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionWorking))

	h.clock.advance(KeepAliveAfter - time.Second)
	assert.Len(t, h.activities(), 1, "not before twenty minutes of silence")
	h.clock.advance(time.Second)
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "thought", "body": "Still working."}, acts[1]["content"])
	assert.Equal(t, true, acts[1]["ephemeral"])

	// Something said resets the silence.
	h.clock.advance(10 * time.Minute)
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("bash", "make test")))
	require.Len(t, h.activities(), 3)
	h.clock.advance(KeepAliveAfter - time.Second)
	assert.Len(t, h.activities(), 3)
	h.clock.advance(time.Second)
	assert.Len(t, h.activities(), 4)

	// The answer ends it, and leaves nothing armed.
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done."})
	require.NoError(t, err)
	assert.Zero(t, h.clock.pending())
	h.clock.advance(3 * time.Hour)
	assert.Len(t, h.activities(), 5)
}

// A question waits for a person, which is not the agent working, and is not kept alive until
// the turn works again.
func TestAQuestionIsNotKeptAlive(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutQuestion, Text: "Which branch?"})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionAwaiting))
	h.clock.advance(3 * time.Hour)
	assert.Len(t, h.activities(), 2, "an elicitation is the last word while nobody has answered")

	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionWorking))
	h.clock.advance(KeepAliveAfter)
	acts := h.activities()
	require.Len(t, acts, 3)
	assert.Equal(t, "Still working.", acts[2]["content"].(map[string]any)["body"])
}

func TestAFailedOrStoppedTurnIsNotKeptAlive(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		h := newAppHarness(t)
		h.startTurn(t)
		require.NoError(t, h.src.React(context.Background(), sessionRef(), conductor.ReactionFailed))
		h.clock.advance(time.Hour)
		assert.Len(t, h.activities(), 1)
		assert.Zero(t, h.clock.pending())
	})
	t.Run("stopped", func(t *testing.T) {
		h := newAppHarness(t)
		h.startTurn(t)
		h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
		h.settle(t)
		h.clock.advance(time.Hour)
		assert.Len(t, h.activities(), 2, "the placeholder and Stopped.")
		assert.Zero(t, h.clock.pending())
	})
}

// A source that stops leaves no timer to post into a session after it.
func TestAStoppedSourceKeepsNothingAlive(t *testing.T) {
	h := newAppHarness(t)
	h.startTurn(t)
	require.NoError(t, h.src.PostActivity(context.Background(), sessionRef(), toolCall("read", "go.mod")))
	require.Positive(t, h.clock.pending())
	h.src.stop()
	assert.Zero(t, h.clock.pending())
	h.clock.advance(time.Hour)
	assert.Len(t, h.activities(), 1)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// startTurn posts a turn's placeholder into the delegation's session, as the conductor does
// first thing, and returns its id.
func (h *appHarness) startTurn(t *testing.T) string {
	t.Helper()
	return h.startTurnOn(t, sessionRef())
}

func (h *appHarness) startTurnOn(t *testing.T, ref string) string {
	t.Helper()
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	return id
}

func (h *appHarness) stopped() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.stops...)
}

func (h *appHarness) replyLinks() {
	h.stub.reply("PodiumAgentSessionUpdate", map[string]any{"agentSessionUpdate": map[string]any{"success": true}})
}

// toolCall is the runtime's activity document for one finished tool call.
func toolCall(tool, arg string) conductor.Outbound {
	field := map[string]string{"bash": "command", "grep": "pattern"}[tool]
	if field == "" {
		field = "filePath"
	}
	return conductor.Outbound{
		Type: conductor.OutActivity,
		Text: `{"kind":"tool","tool":"` + tool + `","status":"completed","input":"{\"` + field + `\":\"` + arg + `\"}"}`,
	}
}
