package conductor

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

func watchEvent(kind podiumv1.TaskEventKind, msgType, text string, at time.Time) *podiumv1.TaskEvent {
	e := &podiumv1.TaskEvent{Kind: kind, Ts: timestamppb.New(at)}
	if msgType != "" {
		e.Payload = &podiumv1.TaskEvent_Message{Message: &podiumv1.Message{Type: msgType, Text: text}}
	}
	return e
}

const (
	evStarted = podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED
	evMessage = podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE
)

func TestAWatchdogReplyIsOnlyAWholeMessageThatNamesAChoice(t *testing.T) {
	for text, want := range map[string]watchdogChoice{
		"wait": watchdogWait, "Keep waiting.": watchdogWait, " RESTART ": watchdogRestart,
		"retry!": watchdogRestart, "end": watchdogEnd, "**stop**": watchdogEnd, "cancel": watchdogEnd,
	} {
		got, ok := parseWatchdogChoice(text)
		require.True(t, ok, text)
		require.Equal(t, want, got, text)
	}
	for _, text := range []string{"", "restart the build with -race", "please wait for CI", "why did it end?"} {
		_, ok := parseWatchdogChoice(text)
		require.False(t, ok, text)
	}
}

func TestAWatchAsksOnlyAfterTheQuietThreshold(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}

	ask, _, _ := w.verdict(t0.Add(time.Hour), 10*time.Minute, 3*time.Minute)
	require.False(t, ask, "a task that has not started is pulling its image, not stuck")

	w.observe(watchEvent(evStarted, "", "", t0))
	w.observe(watchEvent(evMessage, MsgActivity, `{"kind":"tool","tool":"bash","title":"make test"}`, t0.Add(time.Minute)))
	for m := 2; m <= 11; m++ {
		w.observe(watchEvent(evMessage, MsgHeartbeat, `{"v":1}`, t0.Add(time.Duration(m)*time.Minute)))
	}

	ask, _, _ = w.verdict(t0.Add(10*time.Minute), 10*time.Minute, 3*time.Minute)
	require.False(t, ask, "nine quiet minutes is under the threshold")

	ask, wedged, quiet := w.verdict(t0.Add(11*time.Minute+30*time.Second), 10*time.Minute, 3*time.Minute)
	require.True(t, ask)
	require.False(t, wedged, "the heartbeat is still arriving")
	require.Equal(t, 10*time.Minute+30*time.Second, quiet)
	require.Equal(t, "make test", w.lastStep)
}

func TestAWatchCallsATaskWithoutAHeartbeatWedged(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}
	w.observe(watchEvent(evStarted, "", "", t0))
	w.observe(watchEvent(evMessage, MsgHeartbeat, `{"v":1}`, t0.Add(time.Minute)))

	ask, wedged, _ := w.verdict(t0.Add(4*time.Minute+time.Second), 10*time.Minute, 3*time.Minute)
	require.True(t, ask, "a stopped heartbeat does not wait for the quiet threshold")
	require.True(t, wedged)
}

func TestAWatchNeverCallsAnOldRuntimeWedged(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}
	w.observe(watchEvent(evStarted, "", "", t0))

	ask, _, _ := w.verdict(t0.Add(5*time.Minute), 10*time.Minute, 3*time.Minute)
	require.False(t, ask, "a runtime from before heartbeats never sent one to stop")
	ask, wedged, _ := w.verdict(t0.Add(11*time.Minute), 10*time.Minute, 3*time.Minute)
	require.True(t, ask)
	require.False(t, wedged)
}

func TestAWatchLeavesAnAnsweredTaskAlone(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}
	w.observe(watchEvent(evStarted, "", "", t0))
	w.observe(watchEvent(evMessage, OutFinal, "done", t0.Add(time.Minute)))

	ask, _, _ := w.verdict(t0.Add(30*time.Minute), 10*time.Minute, 3*time.Minute)
	require.False(t, ask, "an answered task waiting for a follow-up is quiet because it should be")

	// A follow-up injected into it sets it working again.
	w.observe(watchEvent(evMessage, OutProgress, "on it", t0.Add(31*time.Minute)))
	ask, _, _ = w.verdict(t0.Add(42*time.Minute), 10*time.Minute, 3*time.Minute)
	require.True(t, ask)
}

func TestAnAnsweredWatchdogQuestionComesBackOnlyAfterAnotherInterval(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}
	w.observe(watchEvent(evStarted, "", "", t0))

	w.askedAt = t0.Add(11 * time.Minute)
	ask, _, _ := w.verdict(t0.Add(12*time.Minute), 10*time.Minute, 3*time.Minute)
	require.False(t, ask, "one question at a time")

	// wait
	w.askedAt, w.waitFrom = time.Time{}, t0.Add(12*time.Minute)
	ask, _, _ = w.verdict(t0.Add(21*time.Minute), 10*time.Minute, 3*time.Minute)
	require.False(t, ask)
	ask, _, quiet := w.verdict(t0.Add(23*time.Minute), 10*time.Minute, 3*time.Minute)
	require.True(t, ask)
	require.Equal(t, 23*time.Minute, quiet, "the question still says how long it has really been quiet")
}

func TestATaskThatSpeaksAfterTheQuestionLapsesIt(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	w := &watch{taskID: "task_1"}
	w.observe(watchEvent(evStarted, "", "", t0))
	w.askedAt = t0.Add(11 * time.Minute)

	require.False(t, w.observe(watchEvent(evMessage, MsgHeartbeat, `{"v":1}`, t0.Add(12*time.Minute))),
		"a heartbeat is not the task saying anything")
	require.True(t, w.observe(watchEvent(evMessage, MsgActivity, `{"kind":"tool","tool":"bash"}`, t0.Add(13*time.Minute))))
	require.True(t, w.askedAt.IsZero())
}

func TestTheWatchdogQuestionSaysWhichKindOfQuietItIs(t *testing.T) {
	quiet := watchdogQuestion("podium-dev", "make test-integration\nmore", false, 12*time.Minute)
	require.Contains(t, quiet, "The `podium-dev` task has reported nothing for 12 minutes.")
	require.Contains(t, quiet, "`make test-integration`")
	require.NotContains(t, quiet, "more")
	require.Contains(t, quiet, "**restart** to interrupt it")

	wedged := watchdogQuestion("", "", true, 4*time.Minute)
	require.True(t, strings.HasPrefix(wedged, "The task has stopped responding"))
	require.Contains(t, wedged, "**restart** to end it and start it again")
}
