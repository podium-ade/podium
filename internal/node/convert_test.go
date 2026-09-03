package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

func TestToWireTagsSidecarLogs(t *testing.T) {
	got := toWire(docker.Event{
		Kind:    docker.KindLog,
		Payload: docker.LogPayload{Stream: docker.StreamSidecar, Sidecar: "db", Bytes: []byte("up\n")},
	})
	require.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG, got.GetKind())
	assert.Equal(t, podiumv1.LogChunk_STREAM_SIDECAR, got.GetLog().GetStream())
	assert.Equal(t, "db", got.GetLog().GetSidecarName())
	assert.Equal(t, []byte("up\n"), got.GetLog().GetBytes())
}

func TestToWireCarriesSidecarSteps(t *testing.T) {
	got := toWire(docker.Event{
		Kind:    docker.KindStep,
		Payload: docker.StepPayload{Name: "sidecar/db", Status: "ready"},
	})
	require.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_STEP, got.GetKind())
	assert.Equal(t, "sidecar/db", got.GetStep().GetName())
	assert.Equal(t, "ready", got.GetStep().GetStatus())
}

func TestToWireLeavesTaskLogsUntagged(t *testing.T) {
	got := toWire(docker.Event{
		Kind:    docker.KindLog,
		Payload: docker.LogPayload{Stream: docker.StreamStderr, Bytes: []byte("boom\n")},
	})
	assert.Equal(t, podiumv1.LogChunk_STREAM_STDERR, got.GetLog().GetStream())
	assert.Empty(t, got.GetLog().GetSidecarName())
}

// A message crosses to the wire verbatim: the node is a relay for it, not a reader.
func TestToWireCarriesMessages(t *testing.T) {
	got := toWire(docker.Event{
		Kind: docker.KindMessage,
		Payload: docker.MessagePayload{
			Type:        "final",
			Text:        "the PR is ready\nsee the screenshots",
			Attachments: []string{"shot-1.png", "shot-2.png"},
		},
	})
	require.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE, got.GetKind())
	assert.Equal(t, "final", got.GetMessage().GetType())
	assert.Equal(t, "the PR is ready\nsee the screenshots", got.GetMessage().GetText())
	assert.Equal(t, []string{"shot-1.png", "shot-2.png"}, got.GetMessage().GetAttachments())
}

// An event kind this node has no mapping for becomes UNSPECIFIED rather than being dropped.
// That is existing behaviour and this test only pins it: the server stores the row either
// way, and a kind nothing recognises is still part of the task's history.
func TestToWireLeavesAnUnmappedKindUnspecified(t *testing.T) {
	got := toWire(docker.Event{Kind: "something-newer"})
	assert.Equal(t, podiumv1.TaskEventKind_TASK_EVENT_KIND_UNSPECIFIED, got.GetKind())
	assert.Nil(t, got.GetPayload())
}

func TestLogEventCoalescesPerSource(t *testing.T) {
	e := logEvent(docker.StreamSidecar, "cache", []byte("PONG\n"), 5)
	assert.Equal(t, podiumv1.LogChunk_STREAM_SIDECAR, e.GetLog().GetStream())
	assert.Equal(t, "cache", e.GetLog().GetSidecarName())
}
