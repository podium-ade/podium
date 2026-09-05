package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
)

// streamStep is what one StreamTaskEvents call does before it ends: deliver up to deliver of
// the events the caller has not seen, hold the stream open saying nothing for quiet, and then
// end — cleanly, which the real server does only once the task is terminal, or the way a
// control plane being restarted under it does.
type streamStep struct {
	deliver int
	quiet   time.Duration
	clean   bool
}

// scriptedTasks is podium-server as far as a follower can tell: it honours from_seq against
// the task's whole event list, and each StreamTaskEvents call delivers the next batch of
// steps, holds the stream open saying nothing for as long as the step says, and then breaks
// — or, on the last step, ends cleanly the way the server does once the task is terminal.
type scriptedTasks struct {
	podiumv1connect.UnimplementedTaskServiceHandler

	events []*podiumv1.TaskEvent
	steps  []streamStep

	mu       sync.Mutex
	fromSeqs []uint64
}

func logLine(seq uint64, text string) *podiumv1.TaskEvent {
	e := chunk(podiumv1.LogChunk_STREAM_STDOUT, "", text)
	e.Seq = seq
	return e
}

func (s *scriptedTasks) StreamTaskEvents(
	ctx context.Context,
	req *connect.Request[podiumv1.StreamTaskEventsRequest],
	stream *connect.ServerStream[podiumv1.TaskEvent],
) error {
	from := req.Msg.GetFromSeq()
	s.mu.Lock()
	attempt := len(s.fromSeqs)
	s.fromSeqs = append(s.fromSeqs, from)
	s.mu.Unlock()
	if attempt >= len(s.steps) {
		return connect.NewError(connect.CodeInternal, errors.New("unscripted stream attempt"))
	}
	step := s.steps[attempt]

	sent := 0
	for _, e := range s.events {
		if e.GetSeq() <= from || sent >= step.deliver {
			continue
		}
		if err := stream.Send(e); err != nil {
			return err
		}
		sent++
	}
	select {
	case <-time.After(step.quiet):
	case <-ctx.Done():
		return ctx.Err()
	}
	if step.clean {
		return nil
	}
	return connect.NewError(connect.CodeUnavailable, errors.New("the control plane went away"))
}

// TestFollowOutlastsAQuietStream.
//
// `podium run` on a task that says nothing for a while — provisioning, pulling an image,
// compiling — used to spend its whole reconnect budget on that silence, because the grace
// was measured from the last event rather than from the disconnection. The first blip after
// it then ended the command with "lost the event stream" and no reconnect at all, on a task
// that was about to get going.
func TestFollowOutlastsAQuietStream(t *testing.T) {
	const grace = 200 * time.Millisecond
	tasks := &scriptedTasks{
		events: []*podiumv1.TaskEvent{logLine(1, "starting\n"), logLine(2, "working\n"), logLine(3, "done\n")},
		steps: []streamStep{
			// One line, and then the stream goes.
			{deliver: 1},
			// A long quiet stretch, and then a restart under it.
			{quiet: 4 * grace},
			// The reconnect the command depended on.
			{deliver: 2, clean: true},
		},
	}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewTaskServiceHandler(tasks))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var got []uint64
	f := &follower{
		tasks:   podiumv1connect.NewTaskServiceClient(srv.Client(), srv.URL),
		taskID:  "task_01",
		grace:   grace,
		onEvent: func(e *podiumv1.TaskEvent) { got = append(got, e.GetSeq()) },
	}

	require.NoError(t, f.follow(context.Background()), "a quiet stream is a healthy stream")
	assert.Equal(t, []uint64{1, 2, 3}, got, "every event, once, in order")
	assert.Equal(t, []uint64{0, 1, 1}, tasks.fromSeqs, "each reconnect resumes after the last seq rendered")
}

// A control plane that is really gone is still given up on, and only after the whole grace
// of trying.
func TestFollowGivesUpOnContinuousDisconnection(t *testing.T) {
	const grace = 300 * time.Millisecond
	srv := httptest.NewServer(http.NotFoundHandler())
	client := podiumv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
	srv.Close()

	f := &follower{
		tasks:   client,
		taskID:  "task_01",
		grace:   grace,
		onEvent: func(*podiumv1.TaskEvent) { t.Error("nothing was delivered") },
	}

	started := time.Now()
	err := f.follow(context.Background())

	require.ErrorContains(t, err, "lost the event stream of task_01")
	assert.GreaterOrEqual(t, time.Since(started), grace, "it reconnects for the whole grace first")
}
