package conductor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alvaroibarguen/podium/internal/agent/store"
)

func TestOnlyASucceededTurnWithAnAnswerIsRetained(t *testing.T) {
	const slack, dev = SourceSlack, KindDev

	assert.True(t, retainable(slack, store.TurnSucceeded, "Bob owns the scheduler."))

	// A turn that did not succeed ends with an apology or a half-answer, and neither is a
	// fact. Max turns is one of these: the runtime exits 3 and the task fails.
	for _, status := range []string{
		store.TurnFailed, store.TurnCancelled, store.TurnLost, store.TurnTimeout, store.TurnRunning,
	} {
		assert.False(t, retainable(slack, status, "an answer"), "status %q", status)
	}

	// Nothing said, nothing to remember.
	assert.False(t, retainable(slack, store.TurnSucceeded, ""))

	// The dev source is test-only and is the only thing that can put the runtime into dry
	// run, so "dry-run turns retain nothing" and "a fake conversation cannot plant a
	// memory" are the same exclusion.
	assert.False(t, retainable(dev, store.TurnSucceeded, "dry run: hello"))
}

func TestRetainedContentIsTheExchange(t *testing.T) {
	got := retainContent("alice", "who owns the scheduler?", "Podium", "Bob does.")
	assert.Equal(t, "alice asked: who owns the scheduler?\n\nPodium answered: Bob does.", got)
}

// A resumed turn has lost the question: the instruction is not persisted. Keeping the
// answer alone beats losing the memory.
func TestRetainedContentSurvivesALostQuestion(t *testing.T) {
	got := retainContent("", "", "Podium", "Bob owns the scheduler.")
	assert.Equal(t, "Podium answered: Bob owns the scheduler.", got)
}

// Extraction works on the gist and the whole transcript is already an artifact, so a very
// long answer is cut rather than sent whole.
func TestARunawayAnswerIsTruncated(t *testing.T) {
	got := retainContent("alice", "why?", "Podium", strings.Repeat("x", 64<<10))
	assert.Less(t, len(got), maxRetainedAnswer+200)
	assert.True(t, strings.HasSuffix(got, truncationNote), "the reader is told it was cut")
}

func TestRetainMetadataIsTheProvenance(t *testing.T) {
	r := &turnRun{
		sess: store.Session{ID: "sess_01", SourceKind: SourceSlack},
		turn: store.Turn{ID: "turn_01", TaskID: "task_01"},
		sink: &sink{ref: "C1/1.1/1.2"},
		url:  "https://example.slack.com/archives/C1/p11",
	}
	assert.Equal(t, map[string]string{
		"session_id": "sess_01",
		"turn_id":    "turn_01",
		"task_id":    "task_01",
		"source_ref": "C1/1.1/1.2",
		"source_url": "https://example.slack.com/archives/C1/p11",
	}, retainMetadata(r))
}

// A turn whose task was never created has no task id and no source url, and neither may
// appear as an empty string: a chip that links nowhere is worse than no chip.
func TestRetainMetadataOmitsWhatItDoesNotKnow(t *testing.T) {
	r := &turnRun{
		sess: store.Session{ID: "sess_01", SourceKind: SourceSlack},
		turn: store.Turn{ID: "turn_01"},
		sink: &sink{ref: "C1/1.1/1.2"},
	}
	meta := retainMetadata(r)
	assert.NotContains(t, meta, "task_id")
	assert.NotContains(t, meta, "source_url")
}
