package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// ReplayBufferBytes bounds one task's unacked events. Past it the oldest log chunks are
// dropped — never exited, finished or error, which are what the server needs to move the
// task out of running.
const ReplayBufferBytes = 8 << 20

// eventOverhead is the fixed cost charged per buffered event on top of its log bytes, so
// a flood of tiny chunks is bounded too.
const eventOverhead = 128

// stateFile records where a task's sequence space had got to, inside the per-task
// directory the executor already owns and tears down.
const stateFile = "node-state.json"

// statePersistInterval throttles the state file to one write per task per interval; it is
// only read after a daemon restart, so it may lag by that much.
const statePersistInterval = 250 * time.Millisecond

// taskState is what survives a daemon restart so an adopted task can carry on numbering
// where the previous incarnation stopped instead of colliding with stored rows.
type taskState struct {
	TaskID  string `json:"task_id"`
	LeaseID string `json:"lease_id"`
	HighSeq uint64 `json:"high_seq"`
	// AckedStdout and AckedStderr are how far into each of the container's streams the
	// server has committed, as this daemon last saw it. They are a *fallback*: the
	// control plane hands the true offsets back in its HelloAck, because a node killed
	// between the server's commit and its Ack has no record of an ack that did happen.
	// These are what an adoption uses only when no checkpoint arrives at all.
	AckedStdout int64 `json:"acked_stdout"`
	AckedStderr int64 `json:"acked_stderr"`
}

// buffer is one task's sequence space and its replay buffer: every event the node has
// produced for the task and not yet seen acked, in ascending seq order.
//
// The task goroutine appends; the stream loop sends, marks sent and acks. Everything is
// guarded by mu, and changed is a broadcast channel so waiters can also select on a
// context.
type buffer struct {
	taskID  string
	leaseID string
	dir     string

	mu      sync.Mutex
	changed chan struct{}
	seq     uint64 // highest seq assigned
	acked   uint64 // highest seq the server has committed
	sent    uint64 // highest seq handed to the current stream
	events  []*podiumv1.TaskEvent
	bytes   int
	// progress is the last time the buffer moved: an event sent, or an ack landing.
	// A buffer that has not moved for a while is retransmitted.
	progress time.Time
	// ackedStdout and ackedStderr are how far into each of the container's own streams
	// the server has committed, as the last Ack showed. They are the offsets the
	// container produced, not the bytes that were stored: redaction rewrites what is
	// stored, so len(bytes) summed is a different number.
	ackedStdout int64
	ackedStderr int64
	// overflowed latches on the first dropped chunk; reported makes the marker
	// event happen exactly once per task however long the overflow lasts.
	overflowed  bool
	reported    bool
	lastPersist time.Time
}

// newBuffer starts a sequence space just above startSeq. startSeq is 0 for a fresh
// assignment and the recovered high-water mark for an adopted one.
func newBuffer(taskID, leaseID, dir string, startSeq uint64) *buffer {
	return &buffer{
		taskID:  taskID,
		leaseID: leaseID,
		dir:     dir,
		changed: make(chan struct{}),
		seq:     startSeq,
		acked:   startSeq,
		sent:    startSeq,
	}
}

// append stamps the event with the task, the lease and the next seq, and buffers it. The
// second result is the number of log chunks dropped to stay inside the byte cap; the
// caller turns a non-zero count into an error marker.
func (b *buffer) append(e *podiumv1.TaskEvent) (*podiumv1.TaskEvent, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seq++
	e.TaskId = b.taskID
	e.LeaseId = b.leaseID
	e.Seq = b.seq
	if e.GetTs() == nil {
		e.Ts = timestamppb.New(time.Now().UTC())
	}
	b.events = append(b.events, e)
	b.bytes += sizeOf(e)

	dropped := b.trimLocked()
	b.persistLocked(false)
	b.notifyLocked()
	return e, dropped
}

// trimLocked drops the oldest log chunks until the buffer fits. Anything that is not a log
// chunk stays: exited, finished and error are the events the server cannot do without.
func (b *buffer) trimLocked() int {
	dropped := 0
	for b.bytes > ReplayBufferBytes {
		idx := -1
		for i, e := range b.events {
			if e.GetKind() == podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		b.bytes -= sizeOf(b.events[idx])
		b.events = append(b.events[:idx], b.events[idx+1:]...)
		dropped++
	}
	if dropped > 0 {
		b.overflowed = true
	}
	return dropped
}

// claimOverflow reports whether an overflow marker still has to be emitted for this
// task. It answers true at most once, however many chunks are dropped.
func (b *buffer) claimOverflow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.overflowed && !b.reported {
		b.reported = true
		return true
	}
	return false
}

// pending is everything the current stream has not been handed yet.
func (b *buffer) pending() []*podiumv1.TaskEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*podiumv1.TaskEvent, 0, len(b.events))
	for _, e := range b.events {
		if e.GetSeq() > b.sent {
			out = append(out, e)
		}
	}
	return out
}

// markSent records how far the current stream got. A send that failed halfway simply
// leaves sent where it was and the next connection replays from there.
func (b *buffer) markSent(seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq > b.sent {
		b.sent = seq
		b.progress = time.Now()
	}
}

// stalled reports whether the buffer still holds events the server has not acked and
// nothing has moved for d. An Ingest that failed is never acked, so the node has to send
// those events again rather than wait for a disconnect that may never come.
func (b *buffer) stalled(d time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events) > 0 && b.sent > b.acked && !b.progress.IsZero() && time.Since(b.progress) > d
}

// rewind puts every unacked event back in line for a fresh stream. It is the whole of the
// replay contract: after a reconnect the node re-sends everything the server never acked.
func (b *buffer) rewind() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = b.acked
}

// ack drops everything at or below seq. Acks are monotonic, so an older one is ignored.
func (b *buffer) ack(seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq <= b.acked {
		return
	}
	b.acked = seq
	b.progress = time.Now()
	if b.sent < seq {
		b.sent = seq
	}
	kept := b.events[:0]
	b.bytes = 0
	for _, e := range b.events {
		if e.GetSeq() > seq {
			kept = append(kept, e)
			b.bytes += sizeOf(e)
			continue
		}
		switch e.GetLog().GetStream() {
		case podiumv1.LogChunk_STREAM_STDOUT:
			b.ackedStdout = max(b.ackedStdout, e.GetLog().GetSourceOffset())
		case podiumv1.LogChunk_STREAM_STDERR:
			b.ackedStderr = max(b.ackedStderr, e.GetLog().GetSourceOffset())
		}
	}
	b.events = kept
	b.persistLocked(true)
	b.notifyLocked()
}

// ackedBytes is how many log bytes of each stream the server has committed, which is
// where an adopting daemon resumes reading the container's output from.
func (b *buffer) ackedBytes() (stdout, stderr int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ackedStdout, b.ackedStderr
}

// resume moves an untouched buffer onto the control plane's own view of the task before
// anything is read from the container: the sequence space continues above what the server
// stored, and the byte offsets are what the server actually holds.
//
// It is only ever called on a buffer whose adoption has not started, so there is nothing
// buffered to renumber and nothing in flight to confuse.
func (b *buffer) resume(highSeq uint64, stdout, stderr int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if highSeq > b.seq {
		b.seq, b.acked, b.sent = highSeq, highSeq, highSeq
	}
	b.ackedStdout = max(b.ackedStdout, stdout)
	b.ackedStderr = max(b.ackedStderr, stderr)
	b.persistLocked(true)
}

// high is the highest seq assigned so far.
func (b *buffer) high() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// waitAcked blocks until the server has committed everything up to seq.
func (b *buffer) waitAcked(ctx context.Context, seq uint64) error {
	for {
		b.mu.Lock()
		acked := b.acked
		ch := b.changed
		b.mu.Unlock()
		if acked >= seq {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// notifyLocked wakes every waiter. The channel is closed and replaced, which is a
// broadcast that also composes with select on a context.
func (b *buffer) notifyLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// persistLocked records the high-water mark so a restarted daemon can adopt the task
// without reusing sequence numbers the server has already stored.
func (b *buffer) persistLocked(force bool) {
	if b.dir == "" {
		return
	}
	now := time.Now()
	if !force && now.Sub(b.lastPersist) < statePersistInterval {
		return
	}
	b.lastPersist = now

	raw, err := json.Marshal(taskState{
		TaskID:      b.taskID,
		LeaseID:     b.leaseID,
		HighSeq:     b.seq,
		AckedStdout: b.ackedStdout,
		AckedStderr: b.ackedStderr,
	})
	if err != nil {
		return
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(b.dir, stateFile), raw, 0o600)
}

// flushState forces the high-water mark to disk; the task goroutine calls it once the run
// is over so an adoption after a crash starts from the right number.
func (b *buffer) flushState() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.persistLocked(true)
}

// loadTaskState reads the state a previous incarnation left for a task. A missing or
// unreadable file yields the zero value, which is a safe (if colliding) starting point
// only for a task that never emitted anything.
func loadTaskState(dir string) (taskState, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile)) //nolint:gosec // dir is inside the data dir
	if err != nil {
		return taskState{}, fmt.Errorf("read task state in %s: %w", dir, err)
	}
	var st taskState
	if err := json.Unmarshal(raw, &st); err != nil {
		return taskState{}, fmt.Errorf("parse task state in %s: %w", dir, err)
	}
	return st, nil
}

func sizeOf(e *podiumv1.TaskEvent) int {
	return eventOverhead + len(e.GetLog().GetBytes())
}
