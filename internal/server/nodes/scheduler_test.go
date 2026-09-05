//go:build integration

package nodes_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/scheduler"
)

// fastTiming is what PODIUM_TEST_FAST_TIMERS=1 gives the server these tests run against.
// Everything below is expressed in terms of it rather than in literal seconds, so a change
// to the shipped policy cannot silently make a test assert the wrong thing.
var fastTiming = scheduler.DefaultTiming().Fast()

// fastHarness starts a control plane whose timers are shrunk tenfold, so a scenario that
// waits out the two-minute offline threshold takes twelve seconds. The environment variable
// is read by server.New, which is why it is set before the harness rather than after.
func fastHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv(scheduler.FastTimersEnv, "1")
	return newHarness(t)
}

// ---------------------------------------------------------------------------
// extra fake-node helpers
// ---------------------------------------------------------------------------

// awaitHelloAck reads the reconciliation answer the server sends before anything else.
func (n *fakeNode) awaitHelloAck(timeout time.Duration) *podiumv1.HelloAck {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ack := n.recv(time.Until(deadline)).GetHelloAck(); ack != nil {
			return ack
		}
	}
}

// awaitCancel reads until the server asks for a task to be stopped.
func (n *fakeNode) awaitCancel(taskID string, timeout time.Duration) *podiumv1.Cancel {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c := n.recv(time.Until(deadline)).GetCancel()
		if c != nil && c.GetTaskId() == taskID {
			return c
		}
	}
}

func (n *fakeNode) awaitDrain(timeout time.Duration) *podiumv1.Drain {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if d := n.recv(time.Until(deadline)).GetDrain(); d != nil {
			return d
		}
	}
}

// logChunk is one stdout chunk carrying the source offset the node would report: how many
// bytes the container had produced by the end of it.
func (n *fakeNode) logChunk(a *podiumv1.Assign, text string, sourceOffset int64) *podiumv1.TaskEvent {
	return n.stamp(a, &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream:       podiumv1.LogChunk_STREAM_STDOUT,
			Bytes:        []byte(text),
			SourceOffset: sourceOffset,
		}},
	})
}

// running emits the opening half of a run — provisioning and started — and nothing else, so
// the task sits in `running` for the test to do something to.
func (n *fakeNode) running(a *podiumv1.Assign) {
	n.t.Helper()
	n.send(
		n.stamp(a, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		n.stamp(a, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED}),
	)
}

// finish closes a run out with the exit code the container produced.
func (n *fakeNode) finish(a *podiumv1.Assign, exitCode int32) {
	n.t.Helper()
	n.send(
		n.stamp(a, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED,
			Payload: &podiumv1.TaskEvent_Exited{Exited: &podiumv1.Exited{ExitCode: exitCode}},
		}),
		n.stamp(a, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
			Payload: &podiumv1.TaskEvent_Finished{Finished: &podiumv1.Finished{ExitCode: exitCode}},
		}),
	)
}

// runAborted is the error event a node sends when something ended the run before the
// container could report an exit code: a pull that failed, an engine that would not answer.
// retryable is the node's judgement on whether anybody else could do better.
func (n *fakeNode) runAborted(a *podiumv1.Assign, message string, retryable bool) {
	n.t.Helper()
	n.send(
		n.stamp(a, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		n.stamp(a, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
			Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
				Message: message, Retryable: retryable, AbortsRun: true,
			}},
		}),
	)
}

// createSpec queues a task from a spec, which the simpler createTask helper cannot express.
func (h *harness) createSpec(sp *podiumv1.TaskSpec) *podiumv1.Task {
	h.t.Helper()
	res, err := h.tasks.CreateTask(context.Background(),
		connect.NewRequest(&podiumv1.CreateTaskRequest{Spec: sp}))
	require.NoError(h.t, err)
	return res.Msg.GetTask()
}

// ---------------------------------------------------------------------------
// matching
// ---------------------------------------------------------------------------

// A labelled task always lands on the node that carries the label, however idle the others
// are — and the unlabelled node is never given any of it.
func TestLabelledTasksOnlyGoToLabelledNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	plain := enrollNode(t, h, "node-plain", nil)
	browser := enrollNode(t, h, "node-browser", []string{"browser"})
	plain.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 8, MemoryMb: 16384}, nil)
	defer plain.disconnect()
	browser.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 1, CpuCores: 2, MemoryMb: 2048}, nil)
	defer browser.disconnect()
	h.awaitNodeStatus(plain.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
	h.awaitNodeStatus(browser.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createTask([]string{"browser"}, "true")
	assign := browser.awaitAssign(assignTimeout)
	assert.Equal(t, task.GetId(), assign.GetTaskId())

	plain.expectNoAssign(2 * time.Second)
	assert.Equal(t, browser.id, h.getTask(task.GetId()).GetNodeId())
}

// Three tasks and a node with two slots: two run, the third waits, and it starts as soon as
// one finishes.
func TestANodeIsNeverGivenMoreThanItsSlots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 2, CpuCores: 8, MemoryMb: 16384}, nil)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
	node.heartbeat(2, 0)

	h.createTask(nil, "true")
	h.createTask(nil, "true")
	third := h.createTask(nil, "true")

	a1 := node.awaitAssign(assignTimeout)
	a2 := node.awaitAssign(assignTimeout)
	assigned := map[string]*podiumv1.Assign{a1.GetTaskId(): a1, a2.GetTaskId(): a2}
	require.Len(t, assigned, 2)
	assert.NotContains(t, assigned, third.GetId(), "the third task cannot fit")

	node.expectNoAssign(2 * time.Second)
	queued := h.getTask(third.GetId())
	assert.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, queued.GetStatus())
	assert.Equal(t, scheduler.ReasonFull, queued.GetQueuedReason())
	assert.NotNil(t, queued.GetLastScheduleAttemptAt())

	// Finish one and hand the slot back the way a real node does: through a heartbeat.
	done := a1
	node.running(done)
	node.finish(done, 0)
	h.awaitTaskStatus(done.GetTaskId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)
	node.heartbeat(1, 1)

	a3 := node.awaitAssign(assignTimeout)
	assert.Equal(t, third.GetId(), a3.GetTaskId())
}

// A task asking for more CPU than any node has stays queued, and says so.
func TestATaskBiggerThanEveryNodeStaysQueuedWithAReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-small", nil)
	node.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 4, MemoryMb: 4096}, nil)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Resources: &podiumv1.Resources{Cpu: 8},
	})

	waitFor(t, 15*time.Second, "the scheduler to record why it cannot place the task", func() bool {
		return h.getTask(task.GetId()).GetQueuedReason() != ""
	})
	queued := h.getTask(task.GetId())
	assert.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, queued.GetStatus())
	assert.Contains(t, queued.GetQueuedReason(), scheduler.ReasonNoRoom)
	assert.Contains(t, queued.GetQueuedReason(), "8 CPU")
	assert.NotNil(t, queued.GetLastScheduleAttemptAt())
	node.expectNoAssign(2 * time.Second)
}

// ---------------------------------------------------------------------------
// leases
// ---------------------------------------------------------------------------

// A node that takes an assignment and never says provisioning has not taken it. The task is
// requeued for another attempt, and failed once the budget is spent.
func TestAnUnacceptedAssignmentIsRequeuedThenFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-mute", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{
		Image:       "alpine:3",
		Command:     []string{"true"},
		MaxAttempts: 2,
	})

	// The node hears about it twice and answers neither time.
	first := node.awaitAssign(assignTimeout)
	assert.Equal(t, task.GetId(), first.GetTaskId())
	node.awaitCancel(task.GetId(), 10*time.Second)

	second := node.awaitAssign(assignTimeout)
	assert.Equal(t, task.GetId(), second.GetTaskId())
	assert.NotEqual(t, first.GetLeaseId(), second.GetLeaseId(), "a new attempt gets a new lease")

	failed := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 30*time.Second)
	assert.EqualValues(t, 2, failed.GetAttempts())
	assert.Equal(t, scheduler.ReasonNotAccepted, failed.GetFailureReason())
	assert.Nil(t, failed.ExitCode, "a task that never ran has no exit code")
}

// A node that goes away takes its tasks with it. A task that says it may be re-run comes
// back as a new attempt; one that does not is lost, with an event that says why.
func TestAnOfflineNodeRequeuesOrLosesItsTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-doomed", nil)
	node.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 2, CpuCores: 8, MemoryMb: 16384}, nil)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	retryable := h.createSpec(&podiumv1.TaskSpec{
		Image: "alpine:3", Command: []string{"sleep", "300"},
		MaxAttempts: 2, RetryOnNodeLoss: true,
	})
	once := h.createSpec(&podiumv1.TaskSpec{
		Image: "alpine:3", Command: []string{"sleep", "300"},
		MaxAttempts: 2,
	})

	byTask := map[string]*podiumv1.Assign{}
	for range 2 {
		a := node.awaitAssign(assignTimeout)
		byTask[a.GetTaskId()] = a
		node.running(a)
	}
	require.Len(t, byTask, 2)
	h.awaitTaskStatus(retryable.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)
	h.awaitTaskStatus(once.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	// The node vanishes and never heartbeats again.
	node.disconnect()

	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_OFFLINE, 4*fastTiming.OfflineAfter)

	back := h.awaitTaskStatus(retryable.GetId(), podiumv1.TaskStatus_TASK_STATUS_QUEUED, 30*time.Second)
	assert.Empty(t, back.GetNodeId(), "a requeued task is not on any node")
	assert.EqualValues(t, 1, back.GetAttempts(), "the attempt it lost is not refunded, and no new one is spent yet")

	lost := h.awaitTaskStatus(once.GetId(), podiumv1.TaskStatus_TASK_STATUS_LOST, 30*time.Second)
	assert.Contains(t, lost.GetFailureReason(), "offline")
	assert.Contains(t, lost.GetFailureReason(), "node-doomed")

	// The task's own log explains it, which is the difference between a task an operator
	// can act on and one that just stopped.
	var explained bool
	for _, e := range h.streamEvents(once.GetId(), 0) {
		if e.GetKind() == podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR &&
			e.GetError().GetMessage() != "" {
			explained = true
		}
	}
	assert.True(t, explained, "a lost task carries an error event saying what happened")
}

// A node that comes back holding a container the control plane has since written off is told
// to tear it down, rather than being left running an orphan forever.
func TestANodeIsToldToDropAContainerTheControlPlaneHasLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-orphan", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	node.disconnect()
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_LOST, 4*fastTiming.OfflineAfter)

	// It reconnects still holding the container.
	node.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 8, MemoryMb: 16384},
		[]string{task.GetId()})
	defer node.disconnect()

	ack := node.awaitHelloAck(10 * time.Second)
	require.Len(t, ack.GetTasks(), 1)
	assert.Equal(t, task.GetId(), ack.GetTasks()[0].GetTaskId())
	assert.False(t, ack.GetTasks()[0].GetAdopt(), "the control plane does not want this container")

	cancelMsg := node.awaitCancel(task.GetId(), 10*time.Second)
	assert.Contains(t, cancelMsg.GetReason(), "no longer holds")
}

// The reconciliation answer is the fix for the duplicated log line: the control plane hands
// back exactly what it has, so an adopting node resumes from the right byte rather than from
// a local bookmark an unacknowledged commit has made stale.
func TestHelloAckHandsBackTheCommittedSequenceAndByteOffsets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)

	// Three lines of output. The source offsets are what the container produced; the bytes
	// stored are deliberately shorter for the middle one, the way a redacted chunk is.
	node.send(
		node.logChunk(assign, "tick 1\n", 7),
		node.logChunk(assign, "[redacted:PW]\n", 40),
		node.logChunk(assign, "tick 3\n", 47),
	)
	last := node.seq[task.GetId()]
	node.awaitAck(task.GetId(), last, 20*time.Second)

	node.disconnect()

	// It comes back holding the same container.
	node.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 8, MemoryMb: 16384},
		[]string{task.GetId()})
	defer node.disconnect()

	ack := node.awaitHelloAck(10 * time.Second)
	require.Len(t, ack.GetTasks(), 1)
	cp := ack.GetTasks()[0]
	assert.True(t, cp.GetAdopt(), "the control plane still holds this task on this node")
	assert.Equal(t, last, cp.GetHighSeq(), "an adopting node numbers its next event above this")
	assert.EqualValues(t, 47, cp.GetStdoutOffset(),
		"the offset is what the container produced, not the length of what was stored")
	assert.EqualValues(t, 0, cp.GetStderrOffset())
}

// ---------------------------------------------------------------------------
// timeout and cancellation
// ---------------------------------------------------------------------------

// A task that outruns its timeout is stopped by the server and reported as failed for that
// reason, not as a generic non-zero exit.
func TestTimeoutStopsATaskAndNamesTheReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{
		Image:   "alpine:3",
		Command: []string{"sleep", "300"},
		Timeout: durationpb.New(2 * time.Second),
	})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	stop := node.awaitCancel(task.GetId(), 30*time.Second)
	assert.Equal(t, scheduler.ReasonTimeout, stop.GetReason())

	// The container dies of the SIGKILL the node's grace period ends with.
	node.finish(assign, 137)
	failed := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 30*time.Second)
	assert.Equal(t, scheduler.ReasonTimeout, failed.GetFailureReason())
	assert.EqualValues(t, 137, failed.GetExitCode(), "the container's own exit code survives")
}

// Cancellation is a row, not a map in one process's memory: a task cancelled while running
// lands cancelled even though the node reported a clean exit 0.
func TestACancelledTaskLandsCancelledWhateverTheNodeReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	_, err := h.tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: task.GetId(), Reason: "operator changed their mind",
	}))
	require.NoError(t, err)
	node.awaitCancel(task.GetId(), 10*time.Second)

	node.finish(assign, 0)
	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_CANCELLED, 20*time.Second)
	assert.Equal(t, "operator changed their mind", done.GetFailureReason())
}

// A cancelled task whose node never comes back is written off after the grace period rather
// than staying running forever.
func TestACancelledTaskIsForcedTerminalWhenItsNodeIsGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	_, err := h.tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: task.GetId(), Reason: "e2e",
	}))
	require.NoError(t, err)
	node.disconnect()

	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_CANCELLED,
		4*fastTiming.OfflineAfter)
	assert.Equal(t, "e2e", done.GetFailureReason())
}

// ---------------------------------------------------------------------------
// drain
// ---------------------------------------------------------------------------

// A drained node takes no new work and says why; undraining puts it straight back.
func TestDrainStopsNewWorkAndUndrainResumesIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	_, err := h.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)

	drain := node.awaitDrain(10 * time.Second)
	assert.False(t, drain.GetUndo())
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_DRAINING, 10*time.Second)
	assert.True(t, h.node(node.id).GetDraining())

	task := h.createTask(nil, "true")
	waitFor(t, 15*time.Second, "the scheduler to record why it cannot place the task", func() bool {
		return h.getTask(task.GetId()).GetQueuedReason() != ""
	})
	assert.Equal(t, scheduler.ReasonAllDraining, h.getTask(task.GetId()).GetQueuedReason())
	node.expectNoAssign(2 * time.Second)

	_, err = h.admin.UndrainNode(ctx, connect.NewRequest(&podiumv1.UndrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	resume := node.awaitDrain(10 * time.Second)
	assert.True(t, resume.GetUndo())

	assign := node.awaitAssign(assignTimeout)
	assert.Equal(t, task.GetId(), assign.GetTaskId())
	assert.False(t, h.node(node.id).GetDraining())
}

// A drain survives the node reconnecting: it is a stored instruction, not session state.
func TestDrainSurvivesAReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	_, err := h.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	node.awaitDrain(10 * time.Second)
	node.disconnect()

	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_DRAINING, 10*time.Second)
	assert.False(t, node.awaitDrain(10*time.Second).GetUndo(),
		"a reconnecting node is told again that it is draining")

	h.createTask(nil, "true")
	node.expectNoAssign(3 * time.Second)
}

// Deleting a node is refused while it is online and undrained, and allowed once it is not.
func TestDeleteNodeRefusesAnOnlineNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	_, err := h.admin.DeleteNode(ctx, connect.NewRequest(&podiumv1.DeleteNodeRequest{NodeId: node.id}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = h.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	node.awaitDrain(10 * time.Second)

	_, err = h.admin.DeleteNode(ctx, connect.NewRequest(&podiumv1.DeleteNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	for _, n := range h.listNodes() {
		assert.NotEqual(t, node.id, n.GetId())
	}
	node.disconnect()
}

// A node that still has work on it is never deleted, even drained: the tasks would have
// nowhere to point.
func TestDeleteNodeRefusesANodeWithRunningTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	_, err := h.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	node.awaitDrain(10 * time.Second)

	_, err = h.admin.DeleteNode(ctx, connect.NewRequest(&podiumv1.DeleteNodeRequest{NodeId: node.id}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still running")
}

// A task naming a secret that does not exist fails even with nothing connected at all: the
// names are checked at admission, and admission does not need a node.
func TestAnUnsatisfiableTaskFailsOnAnEmptyCluster(t *testing.T) {
	h := newHarness(t)

	created := h.createSpec(&podiumv1.TaskSpec{
		Image:   "alpine:3",
		Command: []string{"true"},
		Secrets: []*podiumv1.SecretRef{{Name: "NOWHERE", Target: "env", Key: "NOWHERE"}},
	})
	assert.Equal(t, podiumv1.TaskStatus_TASK_STATUS_FAILED, created.GetStatus(),
		"the response already says it failed; there is no node to wait for")
	assert.Contains(t, created.GetFailureReason(), "NOWHERE")
	assert.EqualValues(t, 0, created.GetAttempts())
	assert.Empty(t, created.GetNodeId())
	assert.NotNil(t, created.GetFinishedAt())
	assert.Empty(t, h.listNodes(), "no node was ever involved")
}

// A queued task's reason says the cluster is empty rather than nothing at all.
func TestAQueuedTaskOnAnEmptyClusterSaysSo(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(nil, "true")

	waitFor(t, 15*time.Second, "the scheduler to record why it cannot place the task", func() bool {
		return h.getTask(task.GetId()).GetQueuedReason() != ""
	})
	assert.Equal(t, scheduler.ReasonNoNodes, h.getTask(task.GetId()).GetQueuedReason())
}

// The queue wake-up: a task submitted to an idle control plane is assigned in well under the
// scheduler's own tick, because the database says a row became queued.
func TestASubmittedTaskIsAssignedWithoutWaitingForATick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
	// One task first, so nothing about this measurement includes the cost of the very
	// first scheduler pass over an empty queue.
	h.createTask(nil, "true")
	node.awaitAssign(assignTimeout)

	started := time.Now()
	h.createTask(nil, "true")
	node.awaitAssign(assignTimeout)
	assert.Less(t, time.Since(started), 3*time.Second,
		"a notify-driven scheduler must not make a submission wait out a poll")
}

// ---------------------------------------------------------------------------
// a task is never parked
//
// The invariant these three cover: an error that ended a run either leads to another
// attempt or to a terminal status. It never leaves the task in a status no node is working
// on — which is what a retryable error used to do, because the ingest returned early on the
// assumption that something else would requeue it and nothing ever did.
// ---------------------------------------------------------------------------

// The default budget is one attempt, so the first retryable error is also the last. The task
// fails fast, carrying the node's own words, instead of sitting in provisioning forever.
func TestARetryableErrorWithNoAttemptsLeftFailsTheTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-pull", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "podium-agent-runtime:dev", Command: []string{"true"}})
	assign := node.awaitAssign(assignTimeout)

	const boom = "pull image podium-agent-runtime:dev: Error response from daemon: registry is down"
	node.runAborted(assign, boom, true)

	failed := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 20*time.Second)
	assert.Equal(t, boom, failed.GetFailureReason(), "an operator has to be told what the node said")
	assert.EqualValues(t, 1, failed.GetAttempts())
	assert.Nil(t, failed.ExitCode, "a task whose container never started has no exit code")
}

// With a budget to spend, the same error buys another attempt — and when that one ends the
// same way the task still stops, rather than being handed round forever.
func TestARetryableErrorSpendsTheAttemptBudgetAndThenStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-flaky", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{
		Image: "alpine:3", Command: []string{"true"}, MaxAttempts: 2,
	})

	const boom = "pull image alpine:3: Error response from daemon: registry is down"
	first := node.awaitAssign(assignTimeout)
	node.runAborted(first, boom, true)

	// The requeue is not asserted by waiting for `queued`: the scheduler is woken by the
	// same commit and places the task again within milliseconds, so that status is real but
	// not reliably observable. A second assignment under a second lease is the proof.
	second := node.awaitAssign(assignTimeout)
	assert.Equal(t, task.GetId(), second.GetTaskId())
	assert.NotEqual(t, first.GetLeaseId(), second.GetLeaseId(), "a new attempt gets a new lease")
	node.runAborted(second, boom, true)

	failed := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 20*time.Second)
	assert.Equal(t, boom, failed.GetFailureReason())
	assert.EqualValues(t, 2, failed.GetAttempts(), "the budget is spent, not exceeded")
}

// An error the run survived is the other half of the rule, and the half a fix for the first
// half can easily break: the task keeps its node and still reports its own exit code.
func TestAnErrorTheRunSurvivedLeavesTheTaskWithItsNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-noisy", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"true"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	node.send(node.stamp(assign, &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
		Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message:   `artifact "huge.bin" was not stored: over the limit`,
			Retryable: true,
			AbortsRun: false,
		}},
	}))
	node.finish(assign, 0)

	done := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)
	assert.EqualValues(t, 1, done.GetAttempts(), "nothing about the run was retried")
}

// The backstop, stated as the invariant it enforces: a task in an active status with no
// lease is nobody's. No node is working on it, no lease can expire under it and no node
// health check will ever name it, so the sweep has to end it rather than look at it forever.
func TestAnActiveTaskWithNoLeaseIsNeverLeftParked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := fastHarness(t)
	node := enrollNode(t, h, "node-leaseless", nil)
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	task := h.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	node.running(assign)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	// However it happened, the row now points at no node and holds no lease while claiming
	// to be running.
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx,
		"update tasks set node_id = null, lease_id = null, lease_expires_at = null where id = $1",
		task.GetId())
	require.NoError(t, err)

	failed := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, 30*time.Second)
	assert.Equal(t, scheduler.ReasonNoLease, failed.GetFailureReason())
}
