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

func TestLogEventCoalescesPerSource(t *testing.T) {
	e := logEvent(docker.StreamSidecar, "cache", []byte("PONG\n"), 5)
	assert.Equal(t, podiumv1.LogChunk_STREAM_SIDECAR, e.GetLog().GetStream())
	assert.Equal(t, "cache", e.GetLog().GetSidecarName())
}
