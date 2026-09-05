package node

import (
	"bytes"
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// coalesceInterval and coalesceBytes are the design's event batching budget: a run of
// container output becomes one log event after 100ms or 64KB, whichever comes first.
// Together they keep a chatty task from costing one row per write while still putting a
// line on an operator's terminal within a tenth of a second.
const (
	coalesceInterval = 100 * time.Millisecond
	coalesceBytes    = 64 << 10
)

// teardownTimeout bounds the post-run cleanup, which runs on its own context so a dying
// daemon still gets the chance to remove what it created.
const teardownTimeout = 60 * time.Second

// startTask spawns the goroutine that runs one assignment to completion.
func (n *Node) startTask(a *podiumv1.Assign) {
	taskID := a.GetTaskId()
	buf := newBuffer(taskID, a.GetLeaseId(), n.exec.TaskDir(taskID), 0)
	if !n.addTask(taskID, buf, taskImages(a.GetSpec())...) {
		n.logger.Warn("ignoring duplicate assignment", "task_id", taskID)
		return
	}
	n.logger.Info("task assigned", "task_id", taskID, "lease_id", a.GetLeaseId(),
		"image", a.GetSpec().GetImage(), "secrets", len(a.GetResolvedSecrets()))

	// The redactor and the executor each take their own copy of the values, so the
	// Assign's own plaintext can be scrubbed here and never outlive this call.
	red := newRedactor(a.GetResolvedSecrets())
	injected := requestSecrets(a.GetResolvedSecrets())
	for _, rs := range a.GetResolvedSecrets() {
		zeroBytes(rs.GetValue())
	}

	taskSpec := spec.FromProto(a.GetSpec())
	go n.execute(buf, red, func(events chan<- docker.Event) error {
		_, err := n.exec.Run(n.runCtx, docker.Request{
			TaskID:    taskID,
			LeaseID:   a.GetLeaseId(),
			Spec:      *taskSpec,
			Secrets:   injected,
			Artifacts: artifactUploader{n},
		}, events)
		return err
	})
}

// taskImages is every image reference an assignment needs, so the image cache prune can
// never take one out from under it.
func taskImages(sp *podiumv1.TaskSpec) []string {
	out := []string{sp.GetImage()}
	for _, sc := range sp.GetSidecars() {
		out = append(out, sc.GetImage())
	}
	return out
}

// requestSecrets copies the resolved secrets onto the executor's own type, with their own
// copy of every value.
func requestSecrets(resolved []*podiumv1.ResolvedSecret) []docker.Secret {
	if len(resolved) == 0 {
		return nil
	}
	out := make([]docker.Secret, 0, len(resolved))
	for _, s := range resolved {
		out = append(out, docker.Secret{
			Name:   s.GetName(),
			Target: s.GetTarget(),
			Key:    s.GetKey(),
			Value:  bytes.Clone(s.GetValue()),
		})
	}
	return out
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// adoptTask takes over a container the previous incarnation of the daemon started.
//
// An adopted task gets no redactor: the values were in the previous incarnation's memory
// and are gone with it. Anything the container prints from here on reaches the server
// unredacted. The container's own copies — its environment and its mounted files — are
// untouched, so the task keeps working; it is only the defence-in-depth log filter that
// does not survive a node restart. See the step 09 hand-off notes.
func (n *Node) adoptTask(buf *buffer) {
	stdout, stderr := buf.ackedBytes()
	n.execute(buf, nil, func(events chan<- docker.Event) error {
		_, err := n.exec.Adopt(n.runCtx, docker.AdoptRequest{
			TaskID:     buf.taskID,
			LeaseID:    buf.leaseID,
			FromSeq:    buf.high(),
			SkipStdout: stdout,
			SkipStderr: stderr,
		}, events)
		return err
	})
}

// pendingLog is the run of output being coalesced into the next log event. A run belongs
// to one source: a stream of the task container, or one sidecar.
type pendingLog struct {
	stream  string
	sidecar string
	data    []byte
	// endOffset is how many bytes of this source the container had produced by the end
	// of everything in data. It travels with the emitted chunk so the control plane can
	// tell a future adoption where to resume — the stored bytes cannot say, because
	// redaction rewrites them.
	endOffset int64
}

// execute drives one container run: it consumes the executor's event channel, coalesces
// log chunks, buffers every event for replay, and once the server has acked the last one
// tears the task's resources down and frees the slot.
func (n *Node) execute(buf *buffer, red *redactor, run func(chan<- docker.Event) error) {
	events := make(chan docker.Event, 256)
	done := make(chan error, 1)
	go func() { done <- run(events) }()

	ticker := time.NewTicker(coalesceInterval)
	defer ticker.Stop()

	var pend pendingLog
	// flush emits what has been coalesced so far, redacted. The redactor may hold a few
	// trailing bytes back to catch a secret straddling this boundary; closeRun releases
	// them. The source is kept so closeRun knows whose carry to drain.
	flush := func() {
		if len(pend.data) == 0 {
			return
		}
		out := red.redact(pend.stream, pend.sidecar, pend.data)
		pend.data = nil
		if len(out) > 0 {
			// Whatever the redactor is still holding has not been accounted for yet:
			// those source bytes belong to the next chunk, not this one.
			held := int64(red.held(pend.stream, pend.sidecar))
			n.push(buf, logEvent(pend.stream, pend.sidecar, out, pend.endOffset-held))
		}
	}
	// closeRun ends a source's run of output: flush, then release whatever the redactor
	// was holding, so no bytes are stranded and nothing is emitted after `finished`.
	closeRun := func() {
		flush()
		if tail := red.flush(pend.stream, pend.sidecar); len(tail) > 0 {
			n.push(buf, logEvent(pend.stream, pend.sidecar, tail, pend.endOffset))
		}
		pend = pendingLog{}
	}

	var runErr error
	for open := true; open; {
		select {
		case ev := <-events:
			n.consume(buf, ev, &pend, flush, closeRun)
		case <-ticker.C:
			flush()
		case runErr = <-done:
			open = false
		}
	}

	// Run has returned, so no further event can be produced: the emitter takes a mutex
	// around assigning a seq and sending, and it is finished. Whatever is still in the
	// channel is the tail of the run.
	for drained := false; !drained; {
		select {
		case ev := <-events:
			n.consume(buf, ev, &pend, flush, closeRun)
		default:
			drained = true
		}
	}
	closeRun()
	buf.flushState()

	high := buf.high()
	n.logger.Info("task run finished", "task_id", buf.taskID, "events", high,
		"error", runErrorLine(runErr))

	// Hold the slot until the server has committed every event. An ingest that failed is
	// never acked, so this is also what keeps the replay buffer alive across a reconnect.
	if err := buf.waitAcked(n.runCtx, high); err != nil {
		n.logger.Warn("gave up waiting for the server to ack a finished task",
			"task_id", buf.taskID, "seq", high, "error", err)
		n.removeTask(buf.taskID)
		n.signal()
		return
	}

	if runErr == nil {
		// The executor cleans up after itself only on the failure path.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(n.runCtx), teardownTimeout)
		if err := n.exec.Teardown(ctx, buf.taskID, false); err != nil {
			n.logger.Error("tearing down task resources failed", "task_id", buf.taskID, "error", err)
		}
		cancel()
	}
	n.removeTask(buf.taskID)
	n.signal()
}

// consume turns one executor event into buffered wire events. Log chunks accumulate into
// the pending run; anything else flushes that run first so the ordering the executor
// produced survives on the wire.
func (n *Node) consume(buf *buffer, ev docker.Event, pend *pendingLog, flush, closeRun func()) {
	if ev.Kind == docker.KindLog {
		p, ok := ev.Payload.(docker.LogPayload)
		if !ok {
			return
		}
		if pend.stream != "" && (pend.stream != p.Stream || pend.sidecar != p.Sidecar) {
			closeRun()
		}
		pend.stream, pend.sidecar = p.Stream, p.Sidecar
		pend.data = append(pend.data, p.Bytes...)
		pend.endOffset = p.Offset
		if len(pend.data) >= coalesceBytes {
			flush()
		}
		return
	}
	closeRun()
	n.push(buf, toWire(ev))
}

// runErrorLine is a finished run's error as ONE log line. A sidecar that never became ready
// carries up to a hundred lines of the sidecar's own log inside its error, and a hundred
// embedded newlines in a structured log record is not a log record. The whole text still
// reaches the control plane as the task's error event, which is where an operator reads it.
func runErrorLine(err error) any {
	if err == nil {
		return nil
	}
	if head, _, more := strings.Cut(err.Error(), "\n"); more {
		return head + " (full text in the task's error event)"
	}
	return err.Error()
}

// push buffers one event and reports a replay-buffer overflow exactly once per task. The
// marker is not retryable, which is what tells the server the task's record is incomplete,
// and it does not abort the run: the container is still going and still owes an exit code.
func (n *Node) push(buf *buffer, e *podiumv1.TaskEvent) {
	_, dropped := buf.append(e)
	if dropped > 0 && buf.claimOverflow() {
		n.logger.Warn("replay buffer overflowed; oldest log chunks dropped",
			"task_id", buf.taskID, "cap_bytes", ReplayBufferBytes)
		buf.append(errorEvent("log buffer overflow", false, false))
	}
	n.signal()
}

// rejectAssign tells the server a task it just handed over is not going to run here. It is
// retryable — another node, or this one later, can take it — and it aborts the run, because
// there is no run: the control plane must put the task somewhere else rather than wait for
// output that will never come.
func (n *Node) rejectAssign(
	ctx context.Context,
	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage],
	a *podiumv1.Assign,
	reason string,
) error {
	n.logger.WarnContext(ctx, "refusing assignment", "task_id", a.GetTaskId(), "reason", reason)
	e := errorEvent(reason, true, true)
	e.TaskId = a.GetTaskId()
	e.LeaseId = a.GetLeaseId()
	e.Seq = 1
	return stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_TaskEvent{TaskEvent: e}})
}
