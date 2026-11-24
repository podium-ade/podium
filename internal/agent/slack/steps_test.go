package slack

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// planOf is the plan block a steps post or edit carried.
type planTask struct {
	TaskID  string `json:"task_id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Details *struct {
		Elements []struct {
			Elements []struct {
				Text string `json:"text"`
			} `json:"elements"`
		} `json:"elements"`
	} `json:"details"`
}

type planBlock struct {
	Type    string     `json:"type"`
	BlockID string     `json:"block_id"`
	Title   string     `json:"title"`
	Tasks   []planTask `json:"tasks"`
}

func planOf(t *testing.T, f *fakeSlack, method string, n int) planBlock {
	t.Helper()
	var blocks []planBlock
	require.NoError(t, json.Unmarshal([]byte(f.form(t, method, n).Get("blocks")), &blocks))
	require.Len(t, blocks, 1)
	require.Equal(t, "plan", blocks[0].Type)
	return blocks[0]
}

func (p planTask) detail() string {
	if p.Details == nil || len(p.Details.Elements) == 0 || len(p.Details.Elements[0].Elements) == 0 {
		return ""
	}
	return p.Details.Elements[0].Elements[0].Text
}

func titles(p planBlock) []string {
	out := make([]string, 0, len(p.Tasks))
	for _, task := range p.Tasks {
		out = append(out, task.Title)
	}
	return out
}

func toolDoc(tool, field, target string) string {
	input, _ := json.Marshal(map[string]string{field: target})
	doc, _ := json.Marshal(map[string]string{"kind": "tool", "tool": tool, "status": "completed", "input": string(input)})
	return string(doc)
}

func activity(t *testing.T, s *Source, ref, doc string) {
	t.Helper()
	require.NoError(t, s.PostActivity(t.Context(), ref, conductor.Outbound{Type: conductor.OutActivity, Text: doc}))
}

var stepsRef = Ref("C1", "100.1", "100.9")

// The first tool call posts one reply in the thread holding a plan, and the next edits it.
func TestAToolCallPostsTheStepsAndTheNextEditsThem(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, toolDoc("read", "filePath", "app.go"))
	activity(t, s, stepsRef, toolDoc("bash", "command", "go test ./..."))

	assert.Equal(t, []string{"chat.postMessage", "chat.update"}, f.methods())
	post := f.form(t, "chat.postMessage", 0)
	assert.Equal(t, "100.1", post.Get("thread_ts"), "in the thread, not on the trigger")
	assert.Equal(t, "⏳ Working on it", post.Get("text"), "scaffolding to a transcript read (isNoise)")
	assert.True(t, isNoise(post.Get("text")))
	assert.Equal(t, []string{"Reading app.go"}, titles(planOf(t, f, "chat.postMessage", 0)))

	update := f.form(t, "chat.update", 0)
	assert.Equal(t, "200.1", update.Get("ts"))
	plan := planOf(t, f, "chat.update", 0)
	assert.Equal(t, []string{"Reading app.go", "Running go test ./..."}, titles(plan))
	assert.Equal(t, "complete", plan.Tasks[1].Status)
	assert.Equal(t, stepsBlockID, planOf(t, f, "chat.postMessage", 0).BlockID)
	assert.Equal(t, stepsBlockID, plan.BlockID,
		"the same block_id on every edit, or Slack collapses a plan somebody opened")
}

// A run of one kind of call is one card: the newest target, how many more, and the recent
// ones inside it.
func TestARunOfOneKindIsOneCard(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	for _, file := range []string{"a.go", "b.go", "c.go"} {
		activity(t, s, stepsRef, toolDoc("read", "filePath", file))
	}

	plan := planOf(t, f, "chat.update", 1)
	require.Len(t, plan.Tasks, 1)
	assert.Equal(t, "Reading c.go (+2 more)", plan.Tasks[0].Title)
	assert.Equal(t, "a.go\nb.go\nc.go", plan.Tasks[0].detail())
}

// A failed call is a card of its own marked as an error; the run around it is not.
func TestAFailedCallIsItsOwnCard(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, toolDoc("bash", "command", "make"))
	activity(t, s, stepsRef, `{"kind":"tool","tool":"bash","status":"error","input":"{\"command\":\"make test\"}"}`)
	activity(t, s, stepsRef, toolDoc("bash", "command", "make lint"))

	plan := planOf(t, f, "chat.update", 1)
	assert.Equal(t, []string{"Running make", "Running make test", "Running make lint"}, titles(plan))
	assert.Equal(t, []string{"complete", "error", "complete"},
		[]string{plan.Tasks[0].Status, plan.Tasks[1].Status, plan.Tasks[2].Status})
}

// A thought is never a card: it is the plan's title while the turn works, and on its own it
// posts nothing.
func TestAThoughtIsTheTitleNotACard(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, `{"kind":"reasoning","text":"**Checking the tests**\n\nThe build is red because …"}`)
	assert.Empty(t, f.methods(), "a plan with no tasks is not posted")

	activity(t, s, stepsRef, toolDoc("read", "filePath", "app.go"))
	plan := planOf(t, f, "chat.postMessage", 0)
	assert.Equal(t, "Checking the tests", plan.Title)
	assert.Equal(t, []string{"Reading app.go"}, titles(plan))
}

// Hundreds of calls stay a short message: the newest cards, everything before them folded
// into one, and the total said when the turn ends.
func TestManyStepsAreFolded(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	// Alternating kinds, so nothing merges: 300 cards' worth.
	for i := range 300 {
		if i%2 == 0 {
			activity(t, s, stepsRef, toolDoc("read", "filePath", fmt.Sprintf("f%d.go", i)))
		} else {
			activity(t, s, stepsRef, toolDoc("bash", "command", fmt.Sprintf("cmd %d", i)))
		}
	}
	s.endSteps(t.Context(), stepsRef, false, true)

	edits := 0
	for _, m := range f.methods() {
		if m == "chat.update" {
			edits++
		}
	}
	plan := planOf(t, f, "chat.update", edits-1)
	require.Len(t, plan.Tasks, maxShownSteps)
	assert.Equal(t, "earlier", plan.Tasks[0].TaskID)
	assert.Equal(t, "289 earlier steps", plan.Tasks[0].Title)
	assert.Equal(t, "Running cmd 299", plan.Tasks[maxShownSteps-1].Title, "the newest is last")
	assert.Regexp(t, `^Worked for \d+s · 300 steps$`, plan.Title)
}

// A progress line is a card, not a message, and the conductor gets no id to edit; a notice
// the conductor posts as progress (no ⏳) is still a message.
func TestAProgressLineIsACard(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.2"}`)
	s := testSource(t, f)

	id, err := s.Post(t.Context(), stepsRef, conductor.Outbound{
		Type: conductor.OutProgress, Text: conductor.ProgressPrefix + "Found it.\nThe handler drops the label.",
	})
	require.NoError(t, err)
	assert.Empty(t, id)
	plan := planOf(t, f, "chat.postMessage", 0)
	require.Len(t, plan.Tasks, 1)
	assert.Equal(t, "Found it.", plan.Tasks[0].Title)
	assert.Equal(t, "Found it.\nThe handler drops the label.", plan.Tasks[0].detail())

	_, err = s.Post(t.Context(), stepsRef, conductor.Outbound{Type: conductor.OutProgress, Text: "no artifact named `x.png` was produced"})
	require.NoError(t, err)
	assert.Equal(t, "no artifact named `x.png` was produced", f.form(t, "chat.postMessage", 1).Get("text"))
}

// The answer finishes the steps above it before it is posted, and steps after it start a
// new message below.
func TestTheAnswerEndsTheStepsAboveIt(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.2"}`)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.3"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, toolDoc("read", "filePath", "app.go"))
	_, err := s.Post(t.Context(), stepsRef, conductor.Outbound{Type: conductor.OutQuestion, Text: "Which one?"})
	require.NoError(t, err)
	activity(t, s, stepsRef, toolDoc("edit", "filePath", "app.go"))

	assert.Equal(t, []string{"chat.postMessage", "chat.update", "chat.postMessage", "chat.postMessage"}, f.methods())
	ended := planOf(t, f, "chat.update", 0)
	assert.Regexp(t, `^Worked for \d+s · 1 step$`, ended.Title)
	assert.Equal(t, "Which one?", f.form(t, "chat.postMessage", 1).Get("text"))
	assert.Equal(t, []string{"Editing app.go"}, titles(planOf(t, f, "chat.postMessage", 2)),
		"a new steps message, under the question")
}

// A failure, or a failed outcome, says the turn stopped.
func TestAFailedTurnSaysItStopped(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, toolDoc("read", "filePath", "app.go"))
	require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionFailed))

	assert.Regexp(t, `^Stopped after \d+s · 1 step$`, planOf(t, f, "chat.update", 0).Title)
	s.stepsMu.Lock()
	assert.Empty(t, s.steps, "an ended turn is forgotten")
	s.stepsMu.Unlock()
}

// A message somebody deleted is not edited again.
func TestADeletedStepsMessageIsLeftAlone(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	f.reply("chat.update", `{"ok":false,"error":"message_not_found"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, toolDoc("read", "filePath", "a.go"))
	err := s.PostActivity(t.Context(), stepsRef, conductor.Outbound{Text: toolDoc("bash", "command", "ls")})
	require.Error(t, err)
	activity(t, s, stepsRef, toolDoc("read", "filePath", "b.go"))
	s.endSteps(t.Context(), stepsRef, false, true)

	assert.Equal(t, []string{"chat.postMessage", "chat.update"}, f.methods())
}

// Edits wait out the interval; what arrived meanwhile goes in one edit.
func TestStepsAreEditedNoOftenerThanTheInterval(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)
	s.stepsEvery = 50 * time.Millisecond

	activity(t, s, stepsRef, toolDoc("read", "filePath", "a.go"))
	activity(t, s, stepsRef, toolDoc("bash", "command", "ls"))
	activity(t, s, stepsRef, toolDoc("grep", "pattern", "TODO"))
	assert.Equal(t, []string{"chat.postMessage"}, f.methods(), "the edits are held")

	require.Eventually(t, func() bool { return len(f.methods()) == 2 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"Reading a.go", "Running ls", "Searching for TODO"}, titles(planOf(t, f, "chat.update", 0)))
}

// The thinking status is set when work starts, and cleared at the end only when the bot
// never posted in the thread — a post clears it on Slack's side.
func TestTheThinkingStatusIsClearedOnlyWhenNothingWasPosted(t *testing.T) {
	t.Run("posted", func(t *testing.T) {
		f := newFakeSlack(t)
		f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
		s := testSource(t, f)

		require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionWorking))
		_, err := s.Post(t.Context(), stepsRef, conductor.Outbound{Type: conductor.OutFinal, Text: "done"})
		require.NoError(t, err)
		require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionDone))

		assert.Equal(t, []string{"reactions.add", "assistant.threads.setStatus", "chat.postMessage",
			"reactions.remove", "reactions.add"}, f.methods())
		assert.Equal(t, "is thinking…", f.form(t, "assistant.threads.setStatus", 0).Get("status"))
		assert.Equal(t, "100.1", f.form(t, "assistant.threads.setStatus", 0).Get("thread_ts"))
	})
	t.Run("silent", func(t *testing.T) {
		f := newFakeSlack(t)
		s := testSource(t, f)

		require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionWorking))
		require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionDone))

		assert.Equal(t, []string{"reactions.add", "assistant.threads.setStatus",
			"reactions.remove", "reactions.add", "assistant.threads.setStatus"}, f.methods(),
			"cleared after the outcome is showing")
		assert.Empty(t, f.form(t, "assistant.threads.setStatus", 1).Get("status"))
	})
}

// A refused status is not asked for again.
func TestARefusedStatusIsNotAskedAgain(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("assistant.threads.setStatus", `{"ok":false,"error":"missing_scope"}`)
	s := testSource(t, f)

	require.NoError(t, s.React(t.Context(), stepsRef, conductor.ReactionWorking))
	require.NoError(t, s.React(t.Context(), Ref("C1", "300.1", "300.1"), conductor.ReactionWorking))

	assert.Equal(t, []string{"reactions.add", "assistant.threads.setStatus", "reactions.add"}, f.methods())
}

func TestDuration(t *testing.T) {
	assert.Equal(t, "42s", duration(42*time.Second))
	assert.Equal(t, "3m 5s", duration(3*time.Minute+5*time.Second))
	assert.Equal(t, "1h 12m", duration(72*time.Minute))
}

func TestCardHeadingWithoutTargets(t *testing.T) {
	c := card{verb: "Using"}
	c.push("")
	c.push("")
	assert.Equal(t, "Using ×2", c.heading())
	assert.Equal(t, "Reading "+strings.Repeat("a", 10), card{verb: "Reading", targets: []string{strings.Repeat("a", 10)}, count: 1}.heading())
}

// A failure folded into the earlier steps is still counted there.
func TestAFoldedFailureIsCounted(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	activity(t, s, stepsRef, `{"kind":"tool","tool":"bash","status":"error","input":"{\"command\":\"make\"}"}`)
	for i := range maxShownSteps + 2 {
		if i%2 == 0 {
			activity(t, s, stepsRef, toolDoc("read", "filePath", fmt.Sprintf("f%d.go", i)))
		} else {
			activity(t, s, stepsRef, toolDoc("grep", "pattern", fmt.Sprintf("p%d", i)))
		}
	}

	plan := planOf(t, f, "chat.update", maxShownSteps)
	assert.Equal(t, "earlier", plan.Tasks[0].TaskID)
	assert.Contains(t, plan.Tasks[0].Title, "· 1 failed")
}

// A steps message is scaffolding to a transcript read, however its text reads back.
func TestATranscriptSkipsTheStepsMessage(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[
		{"type":"message","user":"U1","text":"<@UBOT> why?","ts":"100.1"},
		{"type":"message","user":"UBOT","bot_id":"BBOT","text":"Worked for 22s","ts":"100.2",
		 "blocks":[{"type":"plan","title":"Worked for 22s","tasks":[{"task_id":"s1","title":"Reading a.go","status":"complete"}]}]},
		{"type":"message","user":"UBOT","bot_id":"BBOT","text":"Because.","ts":"100.3"}]}`)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","name":"ann","profile":{"display_name":"Ann"}}}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.9"))
	require.NoError(t, err)
	var said []string
	for _, e := range entries {
		said = append(said, e.Text)
	}
	assert.NotContains(t, said, "Worked for 22s")
	assert.Contains(t, said, "Because.")
}
