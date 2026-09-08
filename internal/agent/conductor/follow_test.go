package conductor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
)

// streamStep is what one StreamTaskEvents call does before it ends: deliver up to deliver
// of the events the caller has not seen, hold the stream open saying nothing for quiet, and
// then either end cleanly — which the real server does only once the task is terminal — or
// break the way a control plane being restarted under it does.
type streamStep struct {
	deliver int
	quiet   time.Duration
	clean   bool
}

// scriptedTasks is podium-server as far as a follower can tell. It honours from_seq against
// the task's whole event list, so a reconnect is offered exactly what the real server would
// offer it, and it runs one streamStep per call.
type scriptedTasks struct {
	podiumv1connect.UnimplementedTaskServiceHandler

	events []*podiumv1.TaskEvent
	steps  []streamStep

	mu        sync.Mutex
	fromSeqs  []uint64
	cancels   []*podiumv1.CancelTaskRequest
	cancelErr error
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

func (s *scriptedTasks) CancelTask(
	_ context.Context, req *connect.Request[podiumv1.CancelTaskRequest],
) (*connect.Response[podiumv1.CancelTaskResponse], error) {
	s.mu.Lock()
	s.cancels = append(s.cancels, req.Msg)
	err := s.cancelErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&podiumv1.CancelTaskResponse{Task: &podiumv1.Task{
		Id: req.Msg.GetTaskId(), Status: podiumv1.TaskStatus_TASK_STATUS_CANCELLED,
	}}), nil
}

func (s *scriptedTasks) seen() ([]uint64, []*podiumv1.CancelTaskRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.fromSeqs...), append([]*podiumv1.CancelTaskRequest(nil), s.cancels...)
}

// serveTasks puts a TaskService behind an httptest server and returns a client of it.
func serveTasks(t *testing.T, h podiumv1connect.TaskServiceHandler) podiumv1connect.TaskServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewTaskServiceHandler(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return podiumv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
}

func provisioningEvent(seq uint64) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId: "task_01", Seq: seq, Ts: timestamppb.Now(),
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING,
	}
}

func sayEvent(seq uint64, text string) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId: "task_01", Seq: seq, Ts: timestamppb.Now(),
		Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE,
		Payload: &podiumv1.TaskEvent_Message{Message: &podiumv1.Message{Type: OutFinal, Text: text}},
	}
}

// TestFollowOutlastsAQuietStream is the turn this file exists for. A task that had said one
// thing — provisioning — went quiet for three minutes while its node pulled a 1.66 GB image,
// and the grace, which used to be measured from the last event rather than from the
// disconnection, was long gone by the time the control plane restarted. The turn was written
// off with zero reconnects, fourteen seconds before the agent actually started, and then ran
// for seven more minutes with nobody listening.
func TestFollowOutlastsAQuietStream(t *testing.T) {
	const grace = 200 * time.Millisecond
	tasks := &scriptedTasks{
		events: []*podiumv1.TaskEvent{
			provisioningEvent(1), sayEvent(2, "on it"), sayEvent(3, "opened the PR"),
		},
		steps: []streamStep{
			// The provisioning event, and then the stream goes.
			{deliver: 1},
			// The node pulls the image. Nothing is said, the stream is fine, and then the
			// control plane restarts under it.
			{quiet: 4 * grace},
			// The reconnect the turn depended on.
			{deliver: 2, clean: true},
		},
	}
	var got []uint64
	f := &follower{
		tasks:   serveTasks(t, tasks),
		taskID:  "task_01",
		grace:   grace,
		onEvent: func(_ context.Context, e *podiumv1.TaskEvent) { got = append(got, e.GetSeq()) },
	}

	require.NoError(t, f.follow(context.Background()), "a quiet stream is a healthy stream")

	fromSeqs, _ := tasks.seen()
	assert.Equal(t, []uint64{1, 2, 3}, got, "every event, once, in order")
	assert.Equal(t, []uint64{0, 1, 1}, fromSeqs, "each reconnect resumes after the last seq handled")
	assert.Equal(t, uint64(3), f.lastSeq)
}

// A control plane that is really gone is still given up on, and the follower keeps trying
// for the whole grace first. Neither of these ever answers a stream, so neither can be
// mistaken for a task that is merely quiet: the empty response header is what separates
// them.
func TestFollowGivesUpOnContinuousDisconnection(t *testing.T) {
	const grace = 300 * time.Millisecond
	tests := []struct {
		name  string
		build func(*testing.T) podiumv1connect.TaskServiceClient
	}{
		{
			name: "nothing is listening",
			build: func(*testing.T) podiumv1connect.TaskServiceClient {
				srv := httptest.NewServer(http.NotFoundHandler())
				c := podiumv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
				srv.Close()
				return c
			},
		},
		{
			name: "something in front of the control plane is answering for it",
			build: func(t *testing.T) podiumv1connect.TaskServiceClient {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "no healthy upstream", http.StatusServiceUnavailable)
				}))
				t.Cleanup(srv.Close)
				return podiumv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &follower{
				tasks:   tc.build(t),
				taskID:  "task_01",
				grace:   grace,
				onEvent: func(context.Context, *podiumv1.TaskEvent) { t.Error("nothing was delivered") },
			}

			started := time.Now()
			err := f.follow(context.Background())

			require.ErrorContains(t, err, "lost the event stream of task_01")
			assert.GreaterOrEqual(t, time.Since(started), grace, "it reconnects for the whole grace first")
		})
	}
}

// A stream that is held resets the budget however long the run of failures before it was,
// because a control plane that answered is a control plane that is there.
func TestFollowStartsTheGraceAgainAfterAStreamItHeld(t *testing.T) {
	const grace = 300 * time.Millisecond
	tasks := &scriptedTasks{
		events: []*podiumv1.TaskEvent{sayEvent(1, "done")},
		steps: []streamStep{
			// Long enough that a budget which never restarted would be spent.
			{quiet: grace}, {quiet: grace}, {quiet: grace},
			{deliver: 1, clean: true},
		},
	}
	f := &follower{
		tasks:   serveTasks(t, tasks),
		taskID:  "task_01",
		grace:   grace,
		onEvent: func(context.Context, *podiumv1.TaskEvent) {},
	}

	require.NoError(t, f.follow(context.Background()))
}

// TestGivingUpOnATaskCancelsIt: a turn the conductor has stopped following must not leave
// its task running. It held a node slot and a privileged dind daemon and spent $3.15 after
// the turn it belonged to had already been recorded as failed.
func TestGivingUpOnATaskCancelsIt(t *testing.T) {
	tests := []struct {
		name    string
		fail    error
		wantLog string
	}{
		{name: "the cancel lands"},
		{
			name: "a task that already ended is not worth reporting",
			fail: connect.NewError(connect.CodeFailedPrecondition, errors.New("it is already succeeded")),
		},
		{
			name:    "a cancel that fails is logged",
			fail:    connect.NewError(connect.CodeUnavailable, errors.New("the node is gone")),
			wantLog: "cancelling the task the conductor stopped following failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &scriptedTasks{cancelErr: tc.fail}
			var logged bytes.Buffer
			r := &turnRun{
				sink: &sink{c: &Conductor{
					podium: &podium.Client{Tasks: serveTasks(t, tasks)},
					logger: slog.New(slog.NewTextHandler(&logged, nil)),
				}},
				turn: store.Turn{ID: "turn_01", TaskID: "task_01"},
			}

			r.cancelAbandoned(context.Background(), errors.New("lost the event stream of task_01"))

			_, cancels := tasks.seen()
			require.Len(t, cancels, 1)
			assert.Equal(t, "task_01", cancels[0].GetTaskId())
			assert.Contains(t, cancels[0].GetReason(), "lost the event stream of task_01",
				"the reason says who gave up and why")
			if tc.wantLog == "" {
				assert.Empty(t, logged.String())
				return
			}
			assert.Contains(t, logged.String(), tc.wantLog)
		})
	}
}
