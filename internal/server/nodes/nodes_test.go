//go:build integration

// Package nodes_test drives the whole control plane through its own wire contract: a fake node
// that enrolls, opens the stream and pushes events, and an operator client that creates tasks
// and reads them back. It is an external test package because the harness starts the real
// internal/server, which imports the package under test.
package nodes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/server"
)

// devToken is the shared bearer token every client in this package presents. It is a test
// fixture, not a secret.
const devToken = "devtoken-integration"

// assignTimeout is generous on purpose: the naive scheduler ticks once a second and a task has
// to be claimed, assigned and pushed before a node sees it.
const assignTimeout = 20 * time.Second

// One Postgres 16 container for the whole package; every harness gets its own database in it.
var (
	adminURL string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := runPostgres(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		os.Exit(1)
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(ctr)
		os.Exit(1)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
	}
	os.Exit(code)
}

func runPostgres(ctx context.Context) (*postgres.PostgresContainer, error) {
	return postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("podium"),
		postgres.WithUsername("podium"),
		postgres.WithPassword("podium"),
		postgres.BasicWaitStrategies(),
	)
}

// newDatabase creates an empty database in the shared container and returns its URL.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_server_test_%d", dbSeq.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// harness is a running podium-server on a loopback port plus the clients to talk to it.
type harness struct {
	t     *testing.T
	url   string
	http  *http.Client
	tasks podiumv1connect.TaskServiceClient
	admin podiumv1connect.NodeAdminServiceClient
	nodes podiumv1connect.NodeServiceClient
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessOn(t, newDatabase(t))
}

func newHarnessOn(t *testing.T, databaseURL string) *harness {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	srv, err := server.New(ctx, server.Config{
		DatabaseURL: databaseURL,
		Transport:   server.TransportDev,
		DevListen:   "127.0.0.1:0",
		DevToken:    devToken,
	}, logger)
	require.NoError(t, err)
	require.NoError(t, srv.Start(ctx))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), server.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		srv.Close()
	})

	client := h2cClient(devToken)
	return &harness{
		t:     t,
		url:   srv.URL(),
		http:  client,
		tasks: podiumv1connect.NewTaskServiceClient(client, srv.URL()),
		admin: podiumv1connect.NewNodeAdminServiceClient(client, srv.URL()),
		nodes: podiumv1connect.NewNodeServiceClient(client, srv.URL()),
	}
}

// bearer presents the dev token on every request, which is how both operators and nodes
// authenticate under the dev transport.
type bearer struct {
	rt    http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.rt.RoundTrip(r)
}

// h2cClient speaks HTTP/2 over plaintext, which Connect requires for bidi streams. HTTP/1.1 is
// deliberately off so a misconfiguration shows up as a failure rather than a silent downgrade.
func h2cClient(token string) *http.Client {
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: bearer{token: token, rt: tr}}
}

func (h *harness) createTask(labels []string, command ...string) *podiumv1.Task {
	h.t.Helper()
	res, err := h.tasks.CreateTask(context.Background(), connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{Image: "alpine:3", Command: command, Labels: labels},
	}))
	require.NoError(h.t, err)
	return res.Msg.GetTask()
}

func (h *harness) getTask(taskID string) *podiumv1.Task {
	h.t.Helper()
	res, err := h.tasks.GetTask(context.Background(), connect.NewRequest(&podiumv1.GetTaskRequest{TaskId: taskID}))
	require.NoError(h.t, err)
	return res.Msg.GetTask()
}

func (h *harness) listNodes() []*podiumv1.Node {
	h.t.Helper()
	res, err := h.admin.ListNodes(context.Background(), connect.NewRequest(&podiumv1.ListNodesRequest{}))
	require.NoError(h.t, err)
	return res.Msg.GetNodes()
}

func (h *harness) node(nodeID string) *podiumv1.Node {
	h.t.Helper()
	for _, n := range h.listNodes() {
		if n.GetId() == nodeID {
			return n
		}
	}
	h.t.Fatalf("node %s is not in ListNodes", nodeID)
	return nil
}

// streamEvents reads StreamTaskEvents to completion. The stream ends by itself once the task is
// terminal and every event has been sent.
func (h *harness) streamEvents(taskID string, fromSeq uint64) []*podiumv1.TaskEvent {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := h.tasks.StreamTaskEvents(ctx, connect.NewRequest(&podiumv1.StreamTaskEventsRequest{
		TaskId: taskID, FromSeq: fromSeq,
	}))
	require.NoError(h.t, err)
	defer func() { _ = stream.Close() }()

	var out []*podiumv1.TaskEvent
	for stream.Receive() {
		out = append(out, proto.Clone(stream.Msg()).(*podiumv1.TaskEvent))
	}
	require.NoError(h.t, stream.Err())
	return out
}

func (h *harness) awaitTaskStatus(taskID string, want podiumv1.TaskStatus, timeout time.Duration) *podiumv1.Task {
	h.t.Helper()
	var task *podiumv1.Task
	waitFor(h.t, timeout, fmt.Sprintf("task %s to be %s", taskID, want), func() bool {
		task = h.getTask(taskID)
		return task.GetStatus() == want
	})
	return task
}

func (h *harness) awaitNodeStatus(nodeID string, want podiumv1.NodeStatus, timeout time.Duration) {
	h.t.Helper()
	waitFor(h.t, timeout, fmt.Sprintf("node %s to be %s", nodeID, want), func() bool {
		return h.node(nodeID).GetStatus() == want
	})
}

// waitFor polls on the test goroutine, so the condition may use require and cannot race.
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
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// fake node
// ---------------------------------------------------------------------------

// fakeNode is a node daemon reduced to the wire: it enrolls, holds a stream and emits the same
// event sequence the Docker executor produces. Seq is one monotonic space per task, exactly as
// the real node numbers events, which is what lets the server split a batch across
// task_events and task_log_chunks and still ack a single number.
type fakeNode struct {
	t      *testing.T
	h      *harness
	name   string
	id     string
	key    string
	labels []string

	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage]
	in     chan *podiumv1.ServerMessage
	seq    map[string]uint64
}

func enrollNode(t *testing.T, h *harness, name string, labels []string) *fakeNode {
	t.Helper()
	ctx := context.Background()

	tok, err := h.admin.CreateEnrollmentToken(ctx, connect.NewRequest(&podiumv1.CreateEnrollmentTokenRequest{
		Labels: labels,
		Ttl:    durationpb.New(time.Hour),
	}))
	require.NoError(t, err)
	require.NotEmpty(t, tok.Msg.GetToken())

	res, err := h.nodes.Enroll(ctx, connect.NewRequest(&podiumv1.EnrollRequest{
		Token:         tok.Msg.GetToken(),
		Hostname:      name,
		Arch:          "arm64",
		Os:            "linux",
		CpuCores:      8,
		MemoryMb:      16384,
		DockerVersion: "29.4.3",
	}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetNodeId())
	require.NotEmpty(t, res.Msg.GetNodeKey())

	return &fakeNode{
		t: t, h: h, name: name,
		id:     res.Msg.GetNodeId(),
		key:    res.Msg.GetNodeKey(),
		labels: labels,
		seq:    make(map[string]uint64),
	}
}

// open starts a stream and sends Hello. The reader goroutine exists because Receive blocks.
func (n *fakeNode) open(ctx context.Context) {
	n.t.Helper()
	n.stream = n.h.nodes.Stream(ctx)
	require.NoError(n.t, n.stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Hello{
		Hello: &podiumv1.Hello{
			NodeId:   n.id,
			NodeKey:  n.key,
			Labels:   n.labels,
			Capacity: &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 8, MemoryMb: 16384},
			Version:  "test",
		},
	}}))

	in := make(chan *podiumv1.ServerMessage, 256)
	n.in = in
	stream := n.stream
	go func() {
		defer close(in)
		for {
			msg, err := stream.Receive()
			if err != nil {
				return
			}
			in <- msg
		}
	}()
}

func (n *fakeNode) disconnect() {
	_ = n.stream.CloseRequest()
	_ = n.stream.CloseResponse()
}

func (n *fakeNode) heartbeat(freeSlots, running int32) {
	n.t.Helper()
	require.NoError(n.t, n.stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Heartbeat{
		Heartbeat: &podiumv1.Heartbeat{
			Load:          &podiumv1.NodeLoad{RunningTasks: running, CpuPct: 4, MemPct: 12},
			FreeSlots:     freeSlots,
			DiskFreeBytes: 1 << 40,
			Ts:            timestamppb.New(time.Now().UTC()),
		},
	}}))
}

// recv takes the next server message, failing the test if none arrives in time.
func (n *fakeNode) recv(timeout time.Duration) *podiumv1.ServerMessage {
	n.t.Helper()
	select {
	case msg, ok := <-n.in:
		if !ok {
			n.t.Fatalf("node %s: stream closed while waiting for a server message", n.name)
		}
		return msg
	case <-time.After(timeout):
		n.t.Fatalf("node %s: no server message within %s", n.name, timeout)
	}
	return nil
}

func (n *fakeNode) awaitAssign(timeout time.Duration) *podiumv1.Assign {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if a := n.recv(time.Until(deadline)).GetAssign(); a != nil {
			return a
		}
	}
}

// expectNoAssign drains the stream for d and fails if an assignment shows up.
func (n *fakeNode) expectNoAssign(d time.Duration) {
	n.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case msg, ok := <-n.in:
			if !ok {
				return
			}
			if a := msg.GetAssign(); a != nil {
				n.t.Fatalf("node %s: unexpected assign of task %s", n.name, a.GetTaskId())
			}
		case <-deadline:
			return
		}
	}
}

// awaitAck reads until the task's acked seq reaches want, asserting acks never go backwards.
func (n *fakeNode) awaitAck(taskID string, want uint64, timeout time.Duration) {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	var last uint64
	for {
		ack := n.recv(time.Until(deadline)).GetAck()
		if ack == nil || ack.GetTaskId() != taskID {
			continue
		}
		require.GreaterOrEqual(n.t, ack.GetSeq(), last, "acks must not go backwards")
		last = ack.GetSeq()
		if last >= want {
			require.Equal(n.t, want, last, "the ack is the highest seq of the batch, never beyond it")
			return
		}
	}
}

// stamp fills in the fields every event of a task shares and takes the next seq.
func (n *fakeNode) stamp(a *podiumv1.Assign, e *podiumv1.TaskEvent) *podiumv1.TaskEvent {
	n.seq[a.GetTaskId()]++
	e.TaskId = a.GetTaskId()
	e.LeaseId = a.GetLeaseId()
	e.Seq = n.seq[a.GetTaskId()]
	e.Ts = timestamppb.New(time.Now().UTC())
	return e
}

// lifecycle is the event sequence internal/node/docker emits for one container run:
// provisioning, started, a log chunk per line, exited, finished.
func (n *fakeNode) lifecycle(a *podiumv1.Assign, exitCode int32, lines ...string) []*podiumv1.TaskEvent {
	out := []*podiumv1.TaskEvent{
		n.stamp(a, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		n.stamp(a, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED}),
	}
	for _, line := range lines {
		out = append(out, n.stamp(a, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
			Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
				Stream: podiumv1.LogChunk_STREAM_STDOUT,
				Bytes:  []byte(line),
			}},
		}))
	}
	return append(out,
		n.stamp(a, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED,
			Payload: &podiumv1.TaskEvent_Exited{Exited: &podiumv1.Exited{ExitCode: exitCode}},
		}),
		n.stamp(a, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
			Payload: &podiumv1.TaskEvent_Finished{Finished: &podiumv1.Finished{
				ExitCode: exitCode,
				Usage:    &podiumv1.Usage{CpuSeconds: 0.5, PeakMemoryMb: 12, WallMs: 1234},
			}},
		}),
	)
}

func (n *fakeNode) send(events ...*podiumv1.TaskEvent) {
	n.t.Helper()
	for _, e := range events {
		require.NoError(n.t, n.stream.Send(&podiumv1.NodeMessage{
			Msg: &podiumv1.NodeMessage_TaskEvent{TaskEvent: e},
		}))
	}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestFakeNodeRunsATaskEndToEnd is the acceptance scenario: enroll, stream, Hello, receive an
// Assign, push the whole event lifecycle, get acked, and see the task end succeeded with every
// event replayable in seq order.
func TestFakeNodeRunsATaskEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", []string{"linux/arm64"})
	require.Equal(t, podiumv1.NodeStatus_NODE_STATUS_OFFLINE, h.node(node.id).GetStatus(),
		"a node is offline until it opens a stream")

	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createTask(nil, "sh", "-c", "echo hi")
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, task.GetStatus())
	require.Equal(t, "dev", task.GetRequestedBy())

	assign := node.awaitAssign(assignTimeout)
	require.Equal(t, task.GetId(), assign.GetTaskId())
	require.NotEmpty(t, assign.GetLeaseId())
	require.Equal(t, "alpine:3", assign.GetSpec().GetImage())
	require.Equal(t, []string{"sh", "-c", "echo hi"}, assign.GetSpec().GetCommand())
	require.Equal(t, "/workspace", assign.GetSpec().GetWorkingDir(), "CreateTask applies pkg/spec defaults")
	require.True(t, assign.GetDeadline().AsTime().After(time.Now()), "the provisioning deadline is in the future")

	scheduled := h.getTask(task.GetId())
	require.Equal(t, node.id, scheduled.GetNodeId())

	events := node.lifecycle(assign, 0, "tick 1\n", "tick 2\n")
	require.Len(t, events, 6)
	node.send(events...)
	node.awaitAck(task.GetId(), 6, 15*time.Second)

	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 15*time.Second)
	require.NotNil(t, done.ExitCode)
	require.EqualValues(t, 0, done.GetExitCode())
	require.Equal(t, node.id, done.GetNodeId())
	require.NotNil(t, done.GetStartedAt())
	require.NotNil(t, done.GetFinishedAt())
	require.EqualValues(t, 1234, done.GetUsage().GetWallMs())

	replay := h.streamEvents(task.GetId(), 0)
	require.Len(t, replay, len(events), "every event is replayed exactly once")
	for i, got := range replay {
		require.EqualValues(t, i+1, got.GetSeq(), "events replay in seq order")
		require.Equal(t, events[i].GetKind(), got.GetKind())
		require.Equal(t, task.GetId(), got.GetTaskId())
	}
	require.Equal(t, "tick 1\n", string(replay[2].GetLog().GetBytes()))
	require.Equal(t, "tick 2\n", string(replay[3].GetLog().GetBytes()))
	require.EqualValues(t, 0, replay[4].GetExited().GetExitCode())
	require.EqualValues(t, 1234, replay[5].GetFinished().GetUsage().GetWallMs())

	tail := h.streamEvents(task.GetId(), 3)
	require.Len(t, tail, 3, "from_seq is exclusive")
	require.EqualValues(t, 4, tail[0].GetSeq())
}

// TestNonZeroExitFailsTheTask is the other half of the finished mapping.
func TestNonZeroExitFailsTheTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createTask(nil, "sh", "-c", "exit 3")
	assign := node.awaitAssign(assignTimeout)

	events := node.lifecycle(assign, 3, "boom\n")
	node.send(events...)
	node.awaitAck(task.GetId(), uint64(len(events)), 15*time.Second)

	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 15*time.Second)
	require.NotNil(t, done.ExitCode)
	require.EqualValues(t, 3, done.GetExitCode())
	require.Len(t, h.streamEvents(task.GetId(), 0), len(events))
}

// TestReplayedBatchDoesNotDuplicateRows covers the node's replay buffer: an ack that never
// reached the node makes it send the same seqs again, and that must change nothing.
func TestReplayedBatchDoesNotDuplicateRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createTask(nil, "true")
	assign := node.awaitAssign(assignTimeout)

	events := node.lifecycle(assign, 0, "first\n")
	node.send(events...)
	node.awaitAck(task.GetId(), uint64(len(events)), 15*time.Second)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 15*time.Second)

	before := h.streamEvents(task.GetId(), 0)
	require.Len(t, before, len(events))

	// Replay the identical batch, but with the log chunk's bytes changed: the stored row must
	// win, proving the write was a real no-op and not an upsert.
	replayed := make([]*podiumv1.TaskEvent, 0, len(events))
	for _, e := range events {
		clone := proto.Clone(e).(*podiumv1.TaskEvent)
		if clone.GetKind() == podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG {
			clone.Payload = &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
				Stream: podiumv1.LogChunk_STREAM_STDOUT,
				Bytes:  []byte("TAMPERED\n"),
			}}
		}
		replayed = append(replayed, clone)
	}
	node.send(replayed...)
	node.awaitAck(task.GetId(), uint64(len(events)), 15*time.Second)

	after := h.streamEvents(task.GetId(), 0)
	require.Len(t, after, len(before), "a replayed batch must not add rows")
	seen := make(map[uint64]struct{}, len(after))
	for i, got := range after {
		_, dup := seen[got.GetSeq()]
		require.False(t, dup, "seq %d appears twice", got.GetSeq())
		seen[got.GetSeq()] = struct{}{}
		require.Equal(t, before[i].GetKind(), got.GetKind())
	}
	require.Equal(t, "first\n", string(after[2].GetLog().GetBytes()),
		"the first write of a seq is the one that is kept")
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, h.getTask(task.GetId()).GetStatus())
}

// TestLabelRoutingAndUnschedulableTasks covers the naive scheduler's only real decision.
func TestLabelRoutingAndUnschedulableTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	plain := enrollNode(t, h, "node-plain", []string{"linux/arm64"})
	gpu := enrollNode(t, h, "node-gpu", []string{"linux/arm64", "gpu"})
	plain.open(ctx)
	defer plain.disconnect()
	gpu.open(ctx)
	defer gpu.disconnect()
	h.awaitNodeStatus(plain.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
	h.awaitNodeStatus(gpu.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createTask([]string{"gpu"}, "nvidia-smi")
	assign := gpu.awaitAssign(assignTimeout)
	require.Equal(t, task.GetId(), assign.GetTaskId())
	require.Equal(t, gpu.id, h.getTask(task.GetId()).GetNodeId())
	plain.expectNoAssign(2 * time.Second)

	stuck := h.createTask([]string{"fpga"}, "true")
	plain.expectNoAssign(4 * time.Second)
	gpu.expectNoAssign(time.Second)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, h.getTask(stuck.GetId()).GetStatus(),
		"a task no node can satisfy stays queued")
}

// TestDisconnectMarksUnreachableAndReconnectMarksOnline covers the session lifecycle the node
// daemon depends on for its reconnect loop.
func TestDisconnectMarksUnreachableAndReconnectMarksOnline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-flappy", nil)

	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	node.heartbeat(3, 1)
	waitFor(t, 10*time.Second, "the heartbeat's free slots to reach ListNodes", func() bool {
		return h.node(node.id).GetFreeSlots() == 3
	})

	node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_UNREACHABLE, 10*time.Second)

	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
}

// TestFinishedTasksReleaseTheirSlots is the regression test for a running_tasks count that only
// ever grew: a slot was booked on every Assign and nothing ever gave one back, so a node that
// had run five tasks reported "running 5" against a capacity of 4.
func TestFinishedTasksReleaseTheirSlots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	const runs = 5
	for i := range runs {
		// The honest ground truth before each run: nothing is running, every slot is free.
		node.heartbeat(4, 0)

		task := h.createTask(nil, "sh", "-c", "true")
		assign := node.awaitAssign(assignTimeout)
		require.Equal(t, task.GetId(), assign.GetTaskId())
		waitFor(t, 10*time.Second, fmt.Sprintf("run %d to count as running", i), func() bool {
			return h.node(node.id).GetRunningTasks() == 1
		})
		requireSlotsConsistent(t, h.node(node.id))

		events := node.lifecycle(assign, 0, "tick\n")
		node.send(events...)
		node.awaitAck(task.GetId(), uint64(len(events)), 15*time.Second)
		h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 15*time.Second)

		waitFor(t, 10*time.Second, fmt.Sprintf("run %d to release its slot", i), func() bool {
			return h.node(node.id).GetRunningTasks() == 0
		})
		requireSlotsConsistent(t, h.node(node.id))
	}

	node.heartbeat(4, 0)
	waitFor(t, 10*time.Second, "the idle node to report every slot free", func() bool {
		return h.node(node.id).GetFreeSlots() == 4
	})
	live := h.node(node.id)
	require.EqualValues(t, 0, live.GetRunningTasks(), "%d finished tasks leave nothing running", runs)
	requireSlotsConsistent(t, live)
}

// requireSlotsConsistent asserts what ListNodes reports about a node is internally consistent,
// which is what the UI renders as "running N / max".
func requireSlotsConsistent(t *testing.T, n *podiumv1.Node) {
	t.Helper()
	maxTasks := n.GetCapacity().GetMaxTasks()
	require.LessOrEqual(t, n.GetRunningTasks(), maxTasks,
		"a node cannot run more tasks than its capacity")
	require.LessOrEqual(t, n.GetRunningTasks()+n.GetFreeSlots(), maxTasks,
		"running and free slots together cannot exceed capacity")
}

// TestCancelQueuedTaskIsImmediate covers the one cancel path that does not need a node.
func TestCancelQueuedTaskIsImmediate(t *testing.T) {
	h := newHarness(t)
	task := h.createTask([]string{"fpga"}, "true") // nothing can run it, so it stays queued

	res, err := h.tasks.CancelTask(context.Background(), connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: task.GetId(), Reason: "operator changed their mind",
	}))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_CANCELLED, res.Msg.GetTask().GetStatus())
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_CANCELLED, h.getTask(task.GetId()).GetStatus())

	_, err = h.tasks.CancelTask(context.Background(), connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: task.GetId(),
	}))
	require.Error(t, err, "a terminal task cannot be cancelled twice")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestConnectJSONWithoutTheCLI is the curl case from the acceptance checklist: plain HTTP/1.1,
// a JSON body and a bearer token are enough to create a task.
func TestConnectJSONWithoutTheCLI(t *testing.T) {
	h := newHarness(t)
	endpoint := h.url + podiumv1connect.TaskServiceCreateTaskProcedure
	body := `{"spec":{"image":"alpine:3","command":["echo","hi"]}}`

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+devToken)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode, "body: %s", raw)
	require.Equal(t, "HTTP/1.1", res.Proto, "unary Connect works without HTTP/2")

	var decoded struct {
		Task struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			RequestedBy string `json:"requestedBy"`
		} `json:"task"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded.Task.ID, "task_")
	require.Equal(t, "TASK_STATUS_QUEUED", decoded.Task.Status)
	require.Equal(t, "dev", decoded.Task.RequestedBy)

	t.Run("without a token", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = res.Body.Close() }()
		require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	})

	t.Run("healthz and metrics are open", func(t *testing.T) {
		for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
			res, err := http.Get(h.url + path)
			require.NoError(t, err)
			_ = res.Body.Close()
			require.Equal(t, http.StatusOK, res.StatusCode, path)
		}
	})
}

// TestReadyzTracksPostgres uses a Postgres of its own so it can take it away mid-test.
func TestReadyzTracksPostgres(t *testing.T) {
	ctx := context.Background()
	ctr, err := runPostgres(ctx)
	require.NoError(t, err)
	terminated := false
	defer func() {
		if !terminated {
			_ = testcontainers.TerminateContainer(ctr)
		}
	}()
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	h := newHarnessOn(t, dsn)
	require.Equal(t, http.StatusOK, statusOf(t, h.url+"/readyz"))
	require.Equal(t, http.StatusOK, statusOf(t, h.url+"/healthz"))

	require.NoError(t, testcontainers.TerminateContainer(ctr))
	terminated = true

	waitFor(t, 30*time.Second, "/readyz to report postgres down", func() bool {
		return statusOf(t, h.url+"/readyz") == http.StatusServiceUnavailable
	})
	require.Equal(t, http.StatusOK, statusOf(t, h.url+"/healthz"),
		"/healthz is process liveness and does not depend on postgres")
}

func statusOf(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	require.NoError(t, err)
	_ = res.Body.Close()
	return res.StatusCode
}

// TestServerRefusesNonLoopbackDevListen proves the refusal happens in the real start path, not
// only in the dev package's own unit test.
func TestServerRefusesNonLoopbackDevListen(t *testing.T) {
	_, err := server.New(context.Background(), server.Config{
		DatabaseURL: newDatabase(t),
		Transport:   server.TransportDev,
		DevListen:   "0.0.0.0:8080",
		DevToken:    devToken,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "0.0.0.0:8080")
	require.Contains(t, err.Error(), "loopback")
}
