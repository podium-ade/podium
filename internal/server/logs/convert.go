package logs

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
)

// The canonical task_events.kind strings, matching podium.v1.TaskEventKind one for one.
const (
	KindProvisioning = "provisioning"
	KindPulling      = "pulling"
	KindStarted      = "started"
	KindLog          = "log"
	KindStep         = "step"
	KindArtifact     = "artifact"
	KindExited       = "exited"
	KindFinished     = "finished"
	KindError        = "error"
	KindMessage      = "message"
)

var kindNames = map[podiumv1.TaskEventKind]string{
	podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING: KindProvisioning,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_PULLING:      KindPulling,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED:      KindStarted,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG:          KindLog,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_STEP:         KindStep,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_ARTIFACT:     KindArtifact,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED:       KindExited,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED:     KindFinished,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR:        KindError,
	podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE:      KindMessage,
}

var kindValues = func() map[string]podiumv1.TaskEventKind {
	m := make(map[string]podiumv1.TaskEventKind, len(kindNames))
	for k, v := range kindNames {
		m[v] = k
	}
	return m
}()

var streamNames = map[podiumv1.LogChunk_Stream]string{
	podiumv1.LogChunk_STREAM_STDOUT:  "stdout",
	podiumv1.LogChunk_STREAM_STDERR:  "stderr",
	podiumv1.LogChunk_STREAM_SIDECAR: "sidecar",
}

var streamValues = func() map[string]podiumv1.LogChunk_Stream {
	m := make(map[string]podiumv1.LogChunk_Stream, len(streamNames))
	for k, v := range streamNames {
		m[v] = k
	}
	return m
}()

// KindString is the stored spelling of a wire event kind.
func KindString(k podiumv1.TaskEventKind) string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return "unspecified"
}

// payloadJSON renders an event's oneof payload as protojson. Kinds that carry no payload —
// provisioning, pulling and started — store an empty object.
//
// A message is the one payload rendered with EmitDefaultValues, so `attachments` is always
// an empty array rather than absent: every relay reads that field and none of them should
// need a nil check for a task that attached nothing.
func payloadJSON(e *podiumv1.TaskEvent) (json.RawMessage, error) {
	if msg := e.GetMessage(); msg != nil {
		b, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("marshal %s payload: %w", KindString(e.GetKind()), err)
		}
		return b, nil
	}
	var m proto.Message
	switch {
	case e.GetStep() != nil:
		m = e.GetStep()
	case e.GetExited() != nil:
		m = e.GetExited()
	case e.GetFinished() != nil:
		m = e.GetFinished()
	case e.GetError() != nil:
		m = e.GetError()
	case e.GetArtifact() != nil:
		m = e.GetArtifact()
	default:
		return json.RawMessage("{}"), nil
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", KindString(e.GetKind()), err)
	}
	return b, nil
}

// eventTime is the event's own timestamp, falling back to now for a node that sent none.
func eventTime(e *podiumv1.TaskEvent) time.Time {
	if ts := e.GetTs(); ts != nil && ts.IsValid() {
		return ts.AsTime().UTC()
	}
	return time.Now().UTC()
}

// eventToProto rebuilds a wire TaskEvent from a stored task_events row. The lease is not a
// column, so a replayed event carries an empty lease_id.
func eventToProto(taskID string, row store.Event) (*podiumv1.TaskEvent, error) {
	kind, ok := kindValues[row.Kind]
	if !ok {
		kind = podiumv1.TaskEventKind_TASK_EVENT_KIND_UNSPECIFIED
	}
	out := &podiumv1.TaskEvent{
		TaskId: taskID,
		Seq:    row.Seq,
		Ts:     timestamppb.New(row.TS),
		Kind:   kind,
	}
	if len(row.Payload) == 0 {
		return out, nil
	}
	unmarshal := func(m proto.Message) error {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(row.Payload, m); err != nil {
			return fmt.Errorf("decode %s payload of task %s seq %d: %w", row.Kind, taskID, row.Seq, err)
		}
		return nil
	}
	switch row.Kind {
	case KindStep:
		p := &podiumv1.Step{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Step{Step: p}
	case KindExited:
		p := &podiumv1.Exited{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Exited{Exited: p}
	case KindFinished:
		p := &podiumv1.Finished{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Finished{Finished: p}
	case KindError:
		p := &podiumv1.Error{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Error{Error: p}
	case KindArtifact:
		p := &podiumv1.ArtifactRef{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Artifact{Artifact: p}
	case KindMessage:
		p := &podiumv1.Message{}
		if err := unmarshal(p); err != nil {
			return nil, err
		}
		out.Payload = &podiumv1.TaskEvent_Message{Message: p}
	}
	return out, nil
}

// chunkToProto rebuilds a wire TaskEvent from a stored task_log_chunks row.
func chunkToProto(taskID string, row store.LogChunk) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId: taskID,
		Seq:    row.Seq,
		Ts:     timestamppb.New(row.TS),
		Kind:   podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream:       streamValues[row.Stream],
			SidecarName:  row.Sidecar,
			Bytes:        row.Bytes,
			SourceOffset: row.SourceOffset,
		}},
	}
}
