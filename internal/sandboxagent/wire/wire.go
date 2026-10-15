// Package wire is the protocol between podium-node and the agent it runs inside a cloud
// sandbox: the run request, and the frames the agent streams back over a WebSocket.
package wire

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/pkg/spec"
)

// AgentPort is the port the agent listens on inside the sandbox.
const AgentPort = 7700

// Strategies the agent can run a task with.
const (
	// StrategyDocker runs the task image under the sandbox's own Docker engine.
	StrategyDocker = "docker"
	// StrategyProcess runs the task command directly in the sandbox.
	StrategyProcess = "process"
)

// RunRequest is docker.Request without its callbacks, which the agent turns into transfers.
type RunRequest struct {
	TaskID     string                      `json:"task_id"`
	LeaseID    string                      `json:"lease_id"`
	Strategy   string                      `json:"strategy"`
	Spec       spec.TaskSpec               `json:"spec"`
	Secrets    []docker.Secret             `json:"secrets,omitempty"`
	Registries []docker.RegistryCredential `json:"registries,omitempty"`
	Preview    *docker.Preview             `json:"preview,omitempty"`
	// Artifacts and Workspace say whether the node can take artifacts and keeps a
	// workspace session, so the agent knows to offer them.
	Artifacts bool `json:"artifacts,omitempty"`
	Workspace bool `json:"workspace,omitempty"`
}

// Transfer kinds: bytes that have to cross between the agent and the node mid-run.
const (
	// TransferArtifact: the node GETs the body and uploads it to the control plane.
	TransferArtifact = "artifact"
	// TransferWorkspaceRestore: the node PUTs the session tar, or answers not found.
	TransferWorkspaceRestore = "workspace-restore"
	// TransferWorkspaceSave: the node GETs the tar and saves it as the session snapshot.
	TransferWorkspaceSave = "workspace-save"
)

// Transfer asks the node to move bytes. The run waits until the node posts a Result.
type Transfer struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// TransferResult is the node's answer to a transfer.
type TransferResult struct {
	Error    string                `json:"error,omitempty"`
	NotFound bool                  `json:"not_found,omitempty"`
	Stored   docker.ArtifactStored `json:"stored"`
}

// Frame is one message on the events stream: exactly one of Event and Transfer is set.
type Frame struct {
	Event    *Event    `json:"event,omitempty"`
	Transfer *Transfer `json:"transfer,omitempty"`
}

// Event is docker.Event with its payload left as JSON until the kind says what it is.
type Event struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	TS      time.Time       `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Status is what the agent knows about its run.
type Status struct {
	Running  bool `json:"running"`
	Finished bool `json:"finished"`
	Held     bool `json:"held"`
	HeldExit *int `json:"held_exit,omitempty"`
}

// FromEvent encodes ev.
func FromEvent(ev docker.Event) (Event, error) {
	out := Event{Seq: ev.Seq, Kind: ev.Kind, TS: ev.TS}
	if ev.Payload != nil {
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			return Event{}, err
		}
		out.Payload = raw
	}
	return out, nil
}

// ToEvent decodes e back into the payload type its kind carries.
func (e Event) ToEvent() (docker.Event, error) {
	out := docker.Event{Seq: e.Seq, Kind: e.Kind, TS: e.TS}
	if len(e.Payload) == 0 || string(e.Payload) == "null" {
		return out, nil
	}
	var err error
	switch e.Kind {
	case docker.KindLog:
		out.Payload, err = decode[docker.LogPayload](e.Payload)
	case docker.KindPulling:
		out.Payload, err = decode[docker.PullingPayload](e.Payload)
	case docker.KindStep:
		out.Payload, err = decode[docker.StepPayload](e.Payload)
	case docker.KindMessage:
		out.Payload, err = decode[docker.MessagePayload](e.Payload)
	case docker.KindArtifact:
		out.Payload, err = decode[docker.ArtifactPayload](e.Payload)
	case docker.KindExited:
		out.Payload, err = decode[docker.ExitedPayload](e.Payload)
	case docker.KindFinished:
		out.Payload, err = decode[docker.FinishedPayload](e.Payload)
	case docker.KindError:
		out.Payload, err = decode[docker.ErrorPayload](e.Payload)
	case docker.KindPreview:
		out.Payload, err = decode[docker.PreviewPayload](e.Payload)
	default:
		return out, fmt.Errorf("event kind %q has no payload type", e.Kind)
	}
	return out, err
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}
