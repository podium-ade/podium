//go:build integration

package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/conductor"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

// fakePodium is a Podium control plane as much as the conductor can tell: CreateTask,
// StreamTaskEvents, GetTask, ListArtifacts and the artifact download route. It exists so
// the turn loop can be exercised without Docker — the e2e suite covers the real thing.
type fakePodium struct {
	mu         sync.Mutex
	tasks      map[string]*fakeTask
	next       int
	specs      []*podiumv1.TaskSpec
	priorities []int32
	arts       map[string][]*podiumv1.Artifact
	blobs      map[string][]byte
	events     func(taskID string) []*podiumv1.TaskEvent
	// hold, when non-nil, is waited on before a created task is marked terminal. It is how
	// a test makes one turn take long enough for a second message to arrive during it.
	hold <-chan struct{}
	// terminal is the status a task ends in.
	terminal podiumv1.TaskStatus
	exitCode *int32
	reason   string
	// replayAll makes StreamTaskEvents ignore from_seq and re-send everything, which is
	// what a second conductor following the same task sees. The relayed ledger is then the
	// only thing stopping the answer being posted twice.
	replayAll bool
	// cancels is every CancelTask the conductor asked for, in order. Delegated tasks are
	// cancelled by their conversation being deleted and by the turn that started them, so
	// the fake records rather than refuses.
	cancels []*podiumv1.CancelTaskRequest

	srv *httptest.Server
}

type fakeTask struct {
	task     *podiumv1.Task
	events   []*podiumv1.TaskEvent
	done     chan struct{}
	doneOnce sync.Once
}

func newFakePodium(t *testing.T) *fakePodium {
	f := &fakePodium{
		tasks:    map[string]*fakeTask{},
		arts:     map[string][]*podiumv1.Artifact{},
		blobs:    map[string][]byte{},
		terminal: podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED,
	}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewTaskServiceHandler(f))
	mux.Handle(podiumv1connect.NewArtifactServiceHandler(f))
	mux.HandleFunc("/artifacts/", f.download)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePodium) URL() string { return f.srv.URL }

// Specs is every task spec the conductor submitted, in order.
func (f *fakePodium) Specs() []*podiumv1.TaskSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*podiumv1.TaskSpec(nil), f.specs...)
}

// Priorities is the queue priority the conductor asked for on each task, in order.
func (f *fakePodium) Priorities() []int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int32(nil), f.priorities...)
}

// TaskIDs is every task the conductor created, in order.
func (f *fakePodium) TaskIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.tasks))
	for i := 1; i <= f.next; i++ {
		out = append(out, fmt.Sprintf("task_%02d", i))
	}
	return out
}

// AddArtifact registers one artifact and its bytes against a task.
func (f *fakePodium) AddArtifact(taskID, name, contentType, body string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "art_" + strconv.Itoa(len(f.blobs)+1)
	f.arts[taskID] = append(f.arts[taskID], &podiumv1.Artifact{
		Id: id, TaskId: taskID, Name: name, ContentType: contentType,
		SizeBytes: int64(len(body)), Kind: "file",
	})
	f.blobs[id] = []byte(body)
	return id
}

// HoldTasks makes every task created from now on wait before going terminal, and returns
// the function that releases them. It is how a test registers a task's artifacts before the
// turn settles, and how it keeps one turn running while a second message arrives.
func (f *fakePodium) HoldTasks() func() {
	hold := make(chan struct{})
	var once sync.Once
	f.mu.Lock()
	f.hold = hold
	f.mu.Unlock()
	return func() { once.Do(func() { close(hold) }) }
}

// Finish marks a task terminal, which is what ends its event stream.
func (f *fakePodium) Finish(taskID string) {
	f.mu.Lock()
	ft := f.tasks[taskID]
	f.mu.Unlock()
	if ft == nil {
		return
	}
	ft.doneOnce.Do(func() { close(ft.done) })
}

// CreateTask records the spec and builds the scripted task.
func (f *fakePodium) CreateTask(
	_ context.Context, req *connect.Request[podiumv1.CreateTaskRequest],
) (*connect.Response[podiumv1.CreateTaskResponse], error) {
	f.mu.Lock()
	f.next++
	id := fmt.Sprintf("task_%02d", f.next)
	f.specs = append(f.specs, req.Msg.GetSpec())
	f.priorities = append(f.priorities, req.Msg.GetPriority())
	var events []*podiumv1.TaskEvent
	if f.events != nil {
		events = f.events(id)
	}
	ft := &fakeTask{
		task: &podiumv1.Task{
			Id: id, Spec: req.Msg.GetSpec(), Status: podiumv1.TaskStatus_TASK_STATUS_RUNNING,
			CreatedAt: timestamppb.Now(),
		},
		events: events,
		done:   make(chan struct{}),
	}
	f.tasks[id] = ft
	hold := f.hold
	f.mu.Unlock()

	// A task with no hold is over as soon as its events have been delivered; one with a
	// hold waits for the test.
	go func() {
		if hold != nil {
			<-hold
		}
		ft.doneOnce.Do(func() { close(ft.done) })
	}()

	return connect.NewResponse(&podiumv1.CreateTaskResponse{Task: ft.task}), nil
}

func (f *fakePodium) GetTask(
	_ context.Context, req *connect.Request[podiumv1.GetTaskRequest],
) (*connect.Response[podiumv1.GetTaskResponse], error) {
	f.mu.Lock()
	ft := f.tasks[req.Msg.GetTaskId()]
	terminal, exit, reason := f.terminal, f.exitCode, f.reason
	f.mu.Unlock()
	if ft == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such task"))
	}
	out := &podiumv1.Task{
		Id: ft.task.GetId(), Spec: ft.task.GetSpec(), Status: ft.task.GetStatus(),
		CreatedAt: ft.task.GetCreatedAt(),
	}
	select {
	case <-ft.done:
		out.Status = terminal
		out.ExitCode = exit
		out.FailureReason = reason
		out.FinishedAt = timestamppb.Now()
	default:
	}
	return connect.NewResponse(&podiumv1.GetTaskResponse{Task: out}), nil
}

// StreamTaskEvents replays the scripted events and then holds the stream open until the
// task is terminal, which is what the real server does.
func (f *fakePodium) StreamTaskEvents(
	ctx context.Context,
	req *connect.Request[podiumv1.StreamTaskEventsRequest],
	stream *connect.ServerStream[podiumv1.TaskEvent],
) error {
	f.mu.Lock()
	ft := f.tasks[req.Msg.GetTaskId()]
	replayAll := f.replayAll
	f.mu.Unlock()
	if ft == nil {
		return connect.NewError(connect.CodeNotFound, errors.New("no such task"))
	}
	for _, e := range ft.events {
		if !replayAll && e.GetSeq() <= req.Msg.GetFromSeq() {
			continue
		}
		if err := stream.Send(e); err != nil {
			return err
		}
	}
	select {
	case <-ft.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakePodium) ListTasks(
	context.Context, *connect.Request[podiumv1.ListTasksRequest],
) (*connect.Response[podiumv1.ListTasksResponse], error) {
	return connect.NewResponse(&podiumv1.ListTasksResponse{}), nil
}

// CancelTask records the request and ends the task as cancelled, which is what a node does
// when it gets the SIGTERM: the follower then reads a terminal task back and classifies it.
func (f *fakePodium) CancelTask(
	_ context.Context, req *connect.Request[podiumv1.CancelTaskRequest],
) (*connect.Response[podiumv1.CancelTaskResponse], error) {
	f.mu.Lock()
	f.cancels = append(f.cancels, req.Msg)
	ft := f.tasks[req.Msg.GetTaskId()]
	f.mu.Unlock()
	if ft == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such task"))
	}
	f.mu.Lock()
	ft.task.Status = podiumv1.TaskStatus_TASK_STATUS_CANCELLED
	f.mu.Unlock()
	ft.doneOnce.Do(func() { close(ft.done) })
	return connect.NewResponse(&podiumv1.CancelTaskResponse{}), nil
}

// Cancels is every CancelTask the conductor asked for.
func (f *fakePodium) Cancels() []*podiumv1.CancelTaskRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*podiumv1.CancelTaskRequest(nil), f.cancels...)
}

func (f *fakePodium) ListArtifacts(
	_ context.Context, req *connect.Request[podiumv1.ListArtifactsRequest],
) (*connect.Response[podiumv1.ListArtifactsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&podiumv1.ListArtifactsResponse{
		Artifacts: f.arts[req.Msg.GetTaskId()],
	}), nil
}

func (f *fakePodium) GetArtifactURL(
	context.Context, *connect.Request[podiumv1.GetArtifactURLRequest],
) (*connect.Response[podiumv1.GetArtifactURLResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not in this fake"))
}

func (f *fakePodium) download(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	f.mu.Lock()
	body, ok := f.blobs[id]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no such artifact", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------------------
// event helpers
// ---------------------------------------------------------------------------

func messageEvent(taskID string, seq uint64, kind, text string, attachments ...string) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId: taskID,
		Seq:    seq,
		Ts:     timestamppb.Now(),
		Kind:   podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE,
		Payload: &podiumv1.TaskEvent_Message{Message: &podiumv1.Message{
			Type: kind, Text: text, Attachments: attachments,
		}},
	}
}

// accountingEvent is the message the runtime emits after its final, carrying turn.json's
// document through the runner socket instead of the object store.
func accountingEvent(taskID string, seq uint64, numTurns int, cost float64) *podiumv1.TaskEvent {
	return messageEvent(taskID, seq, conductor.MsgAccounting, fmt.Sprintf(
		`{"session_id":"sess_x","turn_id":"turn_x","sdk_session_id":"sdk_x",`+
			`"num_turns":%d,"total_cost_usd":%v,"exit_code":0,`+
			`"started_at":"2026-09-05T10:00:00.000Z","finished_at":"2026-09-05T10:00:09.000Z"}`,
		numTurns, cost))
}

func logEvent(taskID string, seq uint64, text string) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		TaskId: taskID,
		Seq:    seq,
		Ts:     timestamppb.Now(),
		Kind:   podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream: podiumv1.LogChunk_STREAM_STDOUT, Bytes: []byte(text),
		}},
	}
}

// waitFor polls until cond holds, which is how a test waits on a turn loop that runs on its
// own goroutines.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var _ podiumv1connect.TaskServiceHandler = (*fakePodium)(nil)
var _ podiumv1connect.ArtifactServiceHandler = (*fakePodium)(nil)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	require.NoError(t, err)
}
