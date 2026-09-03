// Package docker runs Podium tasks as containers on a local Docker engine and
// streams ordered lifecycle events for them.
package docker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Event kinds emitted by [Executor.Run]. They are the canonical lowercase
// TaskEvent kind names from the wire contract; the node daemon maps them onto
// podiumv1.TaskEvent.
const (
	KindProvisioning = "provisioning"
	KindPulling      = "pulling"
	KindStarted      = "started"
	KindLog          = "log"
	KindStep         = "step"
	KindExited       = "exited"
	KindFinished     = "finished"
	KindError        = "error"
)

// Log stream names carried by [LogPayload].
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Event is one ordered observation about a running task. Seq starts at 1 for
// each task and increases by exactly one per event.
type Event struct {
	Seq     uint64
	Kind    string
	TS      time.Time
	Payload any
}

// LogPayload carries one demultiplexed chunk of container output. Bytes is
// owned by the receiver and is never reused.
type LogPayload struct {
	Stream string
	Bytes  []byte
}

// PullingPayload reports image pull progress. Current and Total are bytes and
// are zero for status lines that carry no progress detail.
type PullingPayload struct {
	Image   string
	Status  string
	LayerID string
	Current int64
	Total   int64
}

// StepPayload carries a structured event the runner reported over its event socket. In
// this slice the runner emits none of its own: the kind exists so that a runner newer than
// its node still delivers something the server can store instead of nothing.
type StepPayload struct {
	Name     string
	Status   string
	ExitCode int
}

// ExitedPayload reports the container's exit as observed from the Docker API.
type ExitedPayload struct {
	ExitCode  int
	OOMKilled bool
}

// FinishedPayload closes out a task run with its exit code and resource usage.
type FinishedPayload struct {
	ExitCode int
	Usage    Usage
}

// ErrorPayload reports a failure that aborts the run. Retryable is true for
// pull and engine errors and false for spec errors.
type ErrorPayload struct {
	Message   string
	Retryable bool
}

// emitter assigns sequence numbers and delivers events. The mutex is held
// across both so that the sequence the receiver observes is also the order the
// events were produced in.
type emitter struct {
	ctx context.Context
	ch  chan<- Event

	mu  sync.Mutex
	seq atomic.Uint64
}

func newEmitter(ctx context.Context, ch chan<- Event) *emitter {
	return newEmitterAt(ctx, ch, 0)
}

// newEmitterAt starts the sequence space just above base, which is how an
// adopted task continues the numbering a previous daemon incarnation left off
// at instead of colliding with the rows the server already has.
func newEmitterAt(ctx context.Context, ch chan<- Event, base uint64) *emitter {
	e := &emitter{ctx: ctx, ch: ch}
	e.seq.Store(base)
	return e
}

func (e *emitter) emit(kind string, payload any) {
	e.mu.Lock()
	defer e.mu.Unlock()

	ev := Event{Seq: e.seq.Add(1), Kind: kind, TS: time.Now().UTC(), Payload: payload}
	if e.ch == nil {
		return
	}
	select {
	case e.ch <- ev:
	case <-e.ctx.Done():
	}
}
