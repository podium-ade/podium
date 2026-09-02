package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
)

// followGrace is how long the CLI keeps reconnecting to StreamTaskEvents after the last
// event it managed to read. It is generous because the point of the reconnect is to
// survive a control plane restart without a gap in the output.
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
}

// follow returns nil when the stream ended by itself, which the server does only once the
// task is terminal and every event has been delivered.
func (f *follower) follow(ctx context.Context) error {
	lastProgress := time.Now()
	backoff := followBackoffMin

	for {
		progressed, err := f.once(ctx)
		if progressed {
			lastProgress = time.Now()
			backoff = followBackoffMin
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(lastProgress) > followGrace {
			return fmt.Errorf("lost the event stream of %s for more than %s: %w", f.taskID, followGrace, err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, followBackoffMax)
	}
}

// once holds one StreamTaskEvents call. A nil error means a clean end of stream.
func (f *follower) once(ctx context.Context) (bool, error) {
	stream, err := f.tasks.StreamTaskEvents(ctx, connect.NewRequest(&podiumv1.StreamTaskEventsRequest{
		TaskId:  f.taskID,
		FromSeq: f.lastSeq,
	}))
	if err != nil {
		return false, err
	}
	defer func() { _ = stream.Close() }()

	progressed := false
	for stream.Receive() {
		e := stream.Msg()
		if e.GetSeq() <= f.lastSeq {
			continue
		}
		f.lastSeq = e.GetSeq()
		progressed = true
		f.onEvent(e)
	}
	if err := stream.Err(); err != nil {
		return progressed, err
	}
	return progressed, nil
}

// writeLog sends a log chunk to the stream it came from, so `podium run … > out.txt`
// captures the task's stdout and nothing else.
func writeLog(stdout, stderr io.Writer, e *podiumv1.TaskEvent) {
	chunk := e.GetLog()
	if chunk == nil {
		return
	}
	w := stdout
	if chunk.GetStream() == podiumv1.LogChunk_STREAM_STDERR {
		w = stderr
	}
	_, _ = w.Write(chunk.GetBytes())
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
