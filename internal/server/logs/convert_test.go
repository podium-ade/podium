package logs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

func messageEvent(m *podiumv1.Message) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId:  "task_1",
		Seq:     7,
		Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE,
		Payload: &podiumv1.TaskEvent_Message{Message: m},
	}
}

func TestMessageKindNameRoundTrips(t *testing.T) {
	require.Equal(t, KindMessage, KindString(podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE))
	require.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE, kindValues[KindMessage])
}

// A message goes to task_events as JSON and comes back out of it as the same message. The
// text is stored verbatim, newlines and all: nothing on this path interprets it.
func TestMessagePayloadRoundTripsThroughStorage(t *testing.T) {
	in := &podiumv1.Message{
		Type:        "final",
		Text:        "the PR is ready\nsee the screenshots",
		Attachments: []string{"shot-1.png", "shot-2.png"},
	}
	raw, err := payloadJSON(messageEvent(in))
	require.NoError(t, err)

	var decoded struct {
		Type        string   `json:"type"`
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "final", decoded.Type)
	assert.Equal(t, in.GetText(), decoded.Text)
	assert.Equal(t, []string{"shot-1.png", "shot-2.png"}, decoded.Attachments)

	back, err := eventToProto("task_1", store.Event{Seq: 7, Kind: KindMessage, Payload: raw})
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE, back.GetKind())
	assert.Equal(t, in.GetType(), back.GetMessage().GetType())
	assert.Equal(t, in.GetText(), back.GetMessage().GetText())
	assert.Equal(t, in.GetAttachments(), back.GetMessage().GetAttachments())
}

// attachments is always present, even when the task named none, so a relay reading the
// stored JSON needs no nil check.
func TestMessagePayloadAlwaysCarriesAnAttachmentsArray(t *testing.T) {
	raw, err := payloadJSON(messageEvent(&podiumv1.Message{Type: "progress", Text: "working"}))
	require.NoError(t, err)

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded, "attachments")
	assert.JSONEq(t, "[]", string(decoded["attachments"]))
}

// TestMessageEventDoesNotMoveStatus: a message changes no task status and drives no
// server-side state at all. The proof is that applyStatus reaches its default branch and
// returns before it touches anything — a Service with no store survives it, where an event
// that does imply a transition dereferences the store it has not got.
func TestMessageEventDoesNotMoveStatus(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()

	assert.NotPanics(t, func() {
		s.applyStatus(ctx, "task_1", messageEvent(&podiumv1.Message{Type: "final", Text: "hi"}))
	}, "a message must imply no transition; nobody may helpfully add one")

	assert.Panics(t, func() {
		s.applyStatus(ctx, "task_1", &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
			Payload: &podiumv1.TaskEvent_Finished{Finished: &podiumv1.Finished{}},
		})
	}, "a kind that does imply a transition needs the store, which is what makes the assertion above mean something")
}
