package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
)

// followGrace is how long the CLI keeps reconnecting to StreamTaskEvents once it can no
// longer hold the stream. It is measured from the disconnection, not from the last event:
// a task that says nothing for minutes — provisioning, pulling an image, compiling — has a
// perfectly healthy stream, and counting its silence against the budget left `podium run`
// with no reconnects at all for the control plane restart the budget exists for.
const followGrace = 90 * time.Second

// followBackoff bounds the reconnect delay while the server is away.
const (
	followBackoffMin = 200 * time.Millisecond
	followBackoffMax = 2 * time.Second
)

// follower reads StreamTaskEvents to the end of the task, reconnecting from the last seq
// it rendered whenever the stream breaks. from_seq is exclusive, so resuming from the
// last seq is exactly-once: no gap and no repeat.
type follower struct {
	tasks   podiumv1connect.TaskServiceClient
	taskID  string
	lastSeq uint64
	// onEvent is called for every event, in strictly ascending seq order.
	onEvent func(*podiumv1.TaskEvent)
	// grace overrides followGrace. It is zero everywhere but in the tests that have to
	// outlast it.
	grace time.Duration
}

// follow returns nil when the stream ended by itself, which the server does only once the
// task is terminal and every event has been delivered.
func (f *follower) follow(ctx context.Context) error {
	grace := f.grace
	if grace == 0 {
		grace = followGrace
	}
	backoff := followBackoffMin
	// lostAt is when the stream was last lost. It is zero until the first failure and starts
	// again from every stream that was held, so what it measures is one unbroken run of
	// failures and never the silence of a task that is simply busy.
	var lostAt time.Time

	for {
		held, err := f.once(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case held:
			// The control plane answered this call, whether or not the task said anything
			// down it. Both budgets start again.
			lostAt = time.Now()
			backoff = followBackoffMin
		case lostAt.IsZero():
			lostAt = time.Now()
		}
		if time.Since(lostAt) > grace {
			return fmt.Errorf("lost the event stream of %s for more than %s: %w", f.taskID, grace, err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, followBackoffMax)
	}
}

// once holds one StreamTaskEvents call. A nil error means a clean end of stream. The bool
// says whether the call ever held the stream, which a call that read no event still did:
// the caller is judging the control plane, not the task's talkativeness.
func (f *follower) once(ctx context.Context) (bool, error) {
	stream, err := f.tasks.StreamTaskEvents(ctx, connect.NewRequest(&podiumv1.StreamTaskEventsRequest{
		TaskId:  f.taskID,
		FromSeq: f.lastSeq,
	}))
	if err != nil {
		return false, err
	}
	defer func() { _ = stream.Close() }()

	for stream.Receive() {
		e := stream.Msg()
		if e.GetSeq() <= f.lastSeq {
			continue
		}
		f.lastSeq = e.GetSeq()
		f.onEvent(e)
	}
	// connect-go's call is not a round trip — it returns a stream object before anything has
	// reached the server — so "no error from the call" proves nothing. The response header is
	// the proof: connect fills it in only from a valid streaming response, so it is empty for
	// a refused connection and for a proxy answering while the control plane is down, and
	// non-empty for a stream the server answered and then dropped. By here it is settled and
	// costs nothing to read: Receive has already returned false.
	return len(stream.ResponseHeader()) > 0, stream.Err()
}

// logPrinter routes a task's log chunks to the right stream. The task's own output goes
// to the corresponding stream of this process, so `podium run … > out.txt` captures the
// task's stdout and nothing else; a sidecar's output is not the task's, so it goes to
// stderr prefixed with the sidecar's name.
//
// Chunks are bytes, not lines, and one may end mid-line, so a prefixed source keeps its
// unterminated tail until the next chunk completes it.
type logPrinter struct {
	stdout, stderr io.Writer
	partial        map[string][]byte
}

func newLogPrinter(stdout, stderr io.Writer) *logPrinter {
	return &logPrinter{stdout: stdout, stderr: stderr, partial: make(map[string][]byte)}
}

func (p *logPrinter) write(e *podiumv1.TaskEvent) {
	chunk := e.GetLog()
	if chunk == nil {
		return
	}
	switch chunk.GetStream() {
	case podiumv1.LogChunk_STREAM_SIDECAR:
		p.writePrefixed(chunk.GetSidecarName(), chunk.GetBytes())
	case podiumv1.LogChunk_STREAM_STDERR:
		_, _ = p.stderr.Write(chunk.GetBytes())
	default:
		_, _ = p.stdout.Write(chunk.GetBytes())
	}
}

// writePrefixed emits every complete line of a sidecar's output as "[name] line".
func (p *logPrinter) writePrefixed(name string, b []byte) {
	buf := append(p.partial[name], b...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		fmt.Fprintf(p.stderr, "[%s] %s\n", name, buf[:i])
		buf = buf[i+1:]
	}
	p.partial[name] = append([]byte(nil), buf...)
}

// flush prints whatever never got its newline, so the last line of a sidecar that dies
// mid-write is not swallowed.
func (p *logPrinter) flush() {
	for _, name := range slices.Sorted(maps.Keys(p.partial)) {
		if len(p.partial[name]) > 0 {
			fmt.Fprintf(p.stderr, "[%s] %s\n", name, p.partial[name])
		}
	}
	clear(p.partial)
}

// getTaskWithRetry reads a task back, tolerating a control plane that is still coming
// up. It is used after the stream ends, when the exit code is the only thing left to
// learn.
func getTaskWithRetry(ctx context.Context, c podiumv1connect.TaskServiceClient, taskID string, budget time.Duration) (*podiumv1.Task, error) {
	deadline := time.Now().Add(budget)
	backoff := followBackoffMin
	var lastErr error
	for {
		res, err := c.GetTask(ctx, connect.NewRequest(&podiumv1.GetTaskRequest{TaskId: taskID}))
		if err == nil {
			return res.Msg.GetTask(), nil
		}
		lastErr = err
		if connect.CodeOf(err) == connect.CodeNotFound || time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, errors.Join(lastErr, ctx.Err())
		}
		backoff = min(backoff*2, followBackoffMax)
	}
}
