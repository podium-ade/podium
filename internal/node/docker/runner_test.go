package docker

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainLines feeds the runner's decoded lines through drain and returns the executor events
// it produced, plus whatever it logged.
func drainLines(t *testing.T, lines ...runnerEvent) ([]Event, string) {
	t.Helper()
	var logged bytes.Buffer
	l := &runnerLink{
		log:    slog.New(slog.NewTextHandler(&logged, nil)),
		events: make(chan runnerEvent, len(lines)),
	}
	out := make(chan Event, len(lines)+1)
	done := l.drain("task_test", newEmitter(context.Background(), out), nil)
	for _, ev := range lines {
		l.events <- ev
	}
	close(l.events)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never finished")
	}
	close(out)

	var got []Event
	for e := range out {
		got = append(got, e)
	}
	return got, logged.String()
}

// TestDrainForwardsAMessageEvent: the node decodes, forwards and forgets. It does not check
// the attachment against the artifacts it has collected — at the moment the message arrives
// the file may still be uploading, and the relay is what resolves names.
func TestDrainForwardsAMessageEvent(t *testing.T) {
	got, _ := drainLines(t, runnerEvent{
		V: 1, Kind: runnerKindMessage, Type: "final",
		Text: "hello\nfrom inside", Attachments: []string{"shot.png"},
	})
	require.Len(t, got, 1)
	require.Equal(t, KindMessage, got[0].Kind)
	require.Equal(t, uint64(1), got[0].Seq)
	require.Equal(t, MessagePayload{
		Type: "final", Text: "hello\nfrom inside", Attachments: []string{"shot.png"},
	}, got[0].Payload)
}

// A message with no text has nothing to relay; it is dropped and said out loud.
func TestDrainDropsAMessageWithNoText(t *testing.T) {
	got, logged := drainLines(t, runnerEvent{V: 1, Kind: runnerKindMessage, Type: "final"})
	require.Empty(t, got)
	assert.Contains(t, logged, "runner message event with no text")
}

// An empty type is forwarded as it arrived: the set is open, and the reader decides what an
// unknown type means. The runner refuses to send one, so this is a forged or foreign line.
func TestDrainForwardsAMessageWithNoType(t *testing.T) {
	got, _ := drainLines(t, runnerEvent{V: 1, Kind: runnerKindMessage, Text: "typeless"})
	require.Len(t, got, 1)
	require.Equal(t, MessagePayload{Text: "typeless"}, got[0].Payload)
}

// A kind this node has never heard of still becomes a storable step event, which is what
// keeps a newer runner working under an older node.
func TestDrainTurnsAnUnknownKindIntoAStep(t *testing.T) {
	got, _ := drainLines(t,
		runnerEvent{V: 1, Kind: "plan", Status: "started"},
		runnerEvent{V: 1, Kind: runnerKindExited, ExitCode: 3},
	)
	require.Len(t, got, 1, "the runner's exit is only logged; ContainerWait is authoritative")
	require.Equal(t, KindStep, got[0].Kind)
	require.Equal(t, StepPayload{Name: "plan", Status: "started"}, got[0].Payload)
}
