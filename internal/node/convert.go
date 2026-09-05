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
	docker.KindArtifact:     podiumv1.TaskEventKind_TASK_EVENT_KIND_ARTIFACT,
	docker.KindExited:       podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED,
	docker.KindFinished:     podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
	docker.KindError:        podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
	docker.KindMessage:      podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE,
}

var logStreams = map[string]podiumv1.LogChunk_Stream{
	docker.StreamStdout:  podiumv1.LogChunk_STREAM_STDOUT,
	docker.StreamStderr:  podiumv1.LogChunk_STREAM_STDERR,
	docker.StreamSidecar: podiumv1.LogChunk_STREAM_SIDECAR,
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
			Stream:       logStreams[p.Stream],
			SidecarName:  p.Sidecar,
			Bytes:        p.Bytes,
			SourceOffset: p.Offset,
		}}
	case docker.StepPayload:
		out.Payload = &podiumv1.TaskEvent_Step{Step: &podiumv1.Step{
			Name:     p.Name,
			Status:   p.Status,
			ExitCode: int32(p.ExitCode),
		}}
	case docker.ArtifactPayload:
		out.Payload = &podiumv1.TaskEvent_Artifact{Artifact: &podiumv1.ArtifactRef{
			ArtifactId:  p.ArtifactID,
			Name:        p.Name,
			ObjectKey:   p.ObjectKey,
			SizeBytes:   p.SizeBytes,
			ContentType: p.ContentType,
		}}
	case docker.MessagePayload:
		out.Payload = &podiumv1.TaskEvent_Message{Message: &podiumv1.Message{
			Type:        p.Type,
			Text:        p.Text,
			Attachments: p.Attachments,
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
			AbortsRun: p.AbortsRun,
		}}
	}
	return out
}

// errorEvent is a node-originated error marker: a failure the executor never saw, such as
// the replay buffer overflowing or an assignment arriving at a full node. abortsRun says
// whether the node has stopped working on the task because of it.
func errorEvent(message string, retryable, abortsRun bool) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
		Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message:   message,
			Retryable: retryable,
			AbortsRun: abortsRun,
		}},
	}
}

// logEvent is one coalesced run of output from a single source: one of the task
// container's two streams, or one sidecar's.
func logEvent(stream, sidecar string, data []byte, sourceOffset int64) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream:       logStreams[stream],
			SidecarName:  sidecar,
			Bytes:        data,
			SourceOffset: sourceOffset,
		}},
	}
}
