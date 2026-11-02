//go:build integration

package nodes_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server"
)

func serverID(id string) func(*server.Config) {
	return func(c *server.Config) { c.ServerIDOverride = id }
}

func (n *fakeNode) awaitInject(taskID string, timeout time.Duration) *podiumv1.Inject {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		in := n.recv(time.Until(deadline)).GetInject()
		if in != nil && in.GetTaskId() == taskID {
			return in
		}
	}
}

// TestTwoServersShareOneFleet: a node streams to one server, and the other — the one an
// operator's or a conductor's call happens to land on — still schedules onto it through the
// shared queue, cancels on it, injects into it and drains it, by relaying to the server that
// holds its stream. Nothing the second server does makes the node look gone.
func TestTwoServersShareOneFleet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newDatabase(t)
	front := newHarnessOn(t, db, serverID("front"))
	holder := newHarnessOn(t, db, serverID("holder"))

	node := enrollNode(t, holder, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	holder.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	// Submitted to the server that holds no node: the holder's scheduler places it.
	task := front.createSpec(&podiumv1.TaskSpec{Image: "alpine:3", Command: []string{"sleep", "300"}})
	assign := node.awaitAssign(assignTimeout)
	require.Equal(t, task.GetId(), assign.GetTaskId())
	node.running(assign)
	front.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	// An inject through the front server reaches the node through the holder.
	_, err := front.tasks.InjectTask(ctx, connect.NewRequest(&podiumv1.InjectTaskRequest{
		TaskId: task.GetId(), Text: "also check the logs",
	}))
	require.NoError(t, err)
	assert.Equal(t, "also check the logs", node.awaitInject(task.GetId(), 10*time.Second).GetText())

	// So does a cancel, and the node's own exit is what ends the task.
	_, err = front.tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: task.GetId(), Reason: "called off from the other server",
	}))
	require.NoError(t, err)
	node.awaitCancel(task.GetId(), 10*time.Second)
	node.finish(assign, 0)
	done := front.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_CANCELLED, 20*time.Second)
	assert.Equal(t, "called off from the other server", done.GetFailureReason())

	// And a drain.
	_, err = front.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	assert.False(t, node.awaitDrain(10*time.Second).GetUndo())

	// The front server never decided the node was gone.
	assert.NotEqual(t, podiumv1.NodeStatus_NODE_STATUS_UNREACHABLE, front.node(node.id).GetStatus())
}

// TestANodeThatMovesServersIsNotMarkedGone: a node that reconnects to another server is not
// marked unreachable when its old stream finally closes.
func TestANodeThatMovesServersIsNotMarkedGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newDatabase(t)
	first := newHarnessOn(t, db, serverID("first"))
	second := newHarnessOn(t, db, serverID("second"))

	node := enrollNode(t, first, "node-a", nil)
	node.open(ctx)
	first.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)
	old := node.stream

	// Reconnect to the second server, then let the first stream close.
	node.h = second
	node.open(ctx)
	waitFor(t, 10*time.Second, "the second server to hold the node", func() bool {
		return second.node(node.id).GetStatus() == podiumv1.NodeStatus_NODE_STATUS_ONLINE
	})
	_ = old.CloseRequest()
	_ = old.CloseResponse()
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, podiumv1.NodeStatus_NODE_STATUS_ONLINE, second.node(node.id).GetStatus())

	_, err := first.admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	assert.False(t, node.awaitDrain(10*time.Second).GetUndo(), "a command through the old server is relayed to the new one")
	node.disconnect()
}
