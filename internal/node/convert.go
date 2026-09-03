package node

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

var eventKinds = map[string]podiumv1.TaskEventKind{
	docker.KindProvisioning: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING,
	docker.KindPulling:      podiumv1.TaskEventKind_TASK_EVENT_KIND_PULLING,
	docker.KindStarted:      podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED,
	docker.KindLog:          podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
	docker.KindStep:         podiumv1.TaskEventKind_TASK_EVENT_KIND_STEP,
	docker.KindExited:       podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED,
	docker.KindFinished:     podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
	docker.KindError:        podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
}

var logStreams = map[string]podiumv1.LogChunk_Stream{
	docker.StreamStdout: podiumv1.LogChunk_STREAM_STDOUT,
	docker.StreamStderr: podiumv1.LogChunk_STREAM_STDERR,
}

// toWire turns an executor event into its wire form, minus the task, lease and seq
// fields, which the buffer stamps when it takes ownership. Log chunks never come through
// here: the task loop coalesces them itself.
//
// provisioning, pulling and started carry no payload; the executor reports nil for the
// first and third, and the wire has no message for pull progress at all.
func toWire(ev docker.Event) *podiumv1.TaskEvent {
	out := &podiumv1.TaskEvent{
		Kind: eventKinds[ev.Kind],
		Ts:   timestamppb.New(ev.TS),
	}
	switch p := ev.Payload.(type) {
	case docker.LogPayload:
		out.Payload = &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream: logStreams[p.Stream],
			Bytes:  p.Bytes,
		}}
	case docker.StepPayload:
		out.Payload = &podiumv1.TaskEvent_Step{Step: &podiumv1.Step{
			Name:     p.Name,
			Status:   p.Status,
			ExitCode: int32(p.ExitCode),
		}}
	case docker.ExitedPayload:
		out.Payload = &podiumv1.TaskEvent_Exited{Exited: &podiumv1.Exited{
			ExitCode:  int32(p.ExitCode),
			OomKilled: p.OOMKilled,
		}}
	case docker.FinishedPayload:
		out.Payload = &podiumv1.TaskEvent_Finished{Finished: &podiumv1.Finished{
			ExitCode: int32(p.ExitCode),
			Usage: &podiumv1.Usage{
				CpuSeconds:   p.Usage.CPUSeconds,
				PeakMemoryMb: p.Usage.PeakMemoryMB,
				WallMs:       p.Usage.WallMS,
			},
		}}
	case docker.ErrorPayload:
		out.Payload = &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message:   p.Message,
			Retryable: p.Retryable,
		}}
	}
	return out
}

// errorEvent is a node-originated error marker: a failure the executor never saw, such as
// the replay buffer overflowing or an assignment arriving at a full node.
func errorEvent(message string, retryable bool) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
		Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message:   message,
			Retryable: retryable,
		}},
	}
}

// logEvent is one coalesced run of container output on a single stream.
func logEvent(stream string, data []byte) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream: logStreams[stream],
			Bytes:  data,
		}},
	}
}
