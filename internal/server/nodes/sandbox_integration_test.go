//go:build integration

package nodes_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
)

var boatLabels = []string{"backend=sandbox", "sandbox.provider=boat"}

// awaitReap reads until the node is asked to delete a sandbox.
func (n *fakeNode) awaitReap(timeout time.Duration) *podiumv1.ReapSandbox {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if r := n.recv(time.Until(deadline)).GetReapSandbox(); r != nil {
			return r
		}
	}
}

func (n *fakeNode) reaped(r *podiumv1.ReapSandbox, errText string) {
	n.t.Helper()
	require.NoError(n.t, n.stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_SandboxReaped{
		SandboxReaped: &podiumv1.SandboxReaped{Provider: r.GetProvider(), SandboxId: r.GetSandboxId(), Error: errText},
	}}))
}

func sandboxReaps(t *testing.T, h *harness, provider string) []store.SandboxReap {
	t.Helper()
	ctx := context.Background()
	st, err := store.New(ctx, h.databaseURL)
	require.NoError(t, err)
	defer st.Close()
	reaps, err := st.ListSandboxReaps(ctx, provider)
	require.NoError(t, err)
	return reaps
}

// A node that comes back without the task it was running leaves its sandbox behind. The
// control plane keeps asking the provider's nodes to delete it until one says it is gone.
func TestALostTasksSandboxIsDeletedByANodeOfItsProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t)
	a := enrollNode(t, h, "boat-a", boatLabels)
	b := enrollNode(t, h, "boat-b", boatLabels)

	a.open(ctx)
	task := h.createTask([]string{"backend=sandbox"}, "sleep", "60")
	as := a.awaitAssign(10 * time.Second)
	a.send(
		a.stamp(as, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		a.stamp(as, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_SANDBOX,
			Payload: &podiumv1.TaskEvent_Sandbox{Sandbox: &podiumv1.SandboxHandle{Provider: "boat", SandboxId: "sbx-1"}},
		}),
	)
	a.awaitAck(task.GetId(), 2, 10*time.Second)
	got := h.getTask(task.GetId())
	require.Equal(t, "boat", got.GetSandboxProvider())
	require.Equal(t, "sbx-1", got.GetSandboxId())

	a.disconnect()
	a.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4}, nil)
	h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_LOST, 10*time.Second)
	r := a.awaitReap(10 * time.Second)
	require.Equal(t, "sbx-1", r.GetSandboxId())
	require.Equal(t, task.GetId(), r.GetTaskId())
	a.reaped(r, "boat is down")
	time.Sleep(300 * time.Millisecond)
	require.Len(t, sandboxReaps(t, h, "boat"), 1, "a failed delete stays queued")

	b.open(ctx)
	defer b.disconnect()
	r = b.awaitReap(10 * time.Second)
	require.Equal(t, "sbx-1", r.GetSandboxId(), "a node of the provider is asked as its stream opens")
	b.reaped(r, "")
	waitFor(t, 10*time.Second, "the reap to clear", func() bool { return len(sandboxReaps(t, h, "boat")) == 0 })
	a.disconnect()
}

// A provider's cap holds across all of its nodes, whatever slots each one has free, and the
// account limit a node reports comes back to the admin.
func TestASandboxProvidersCapHoldsAcrossItsNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t)
	providers := podiumv1connect.NewSandboxProviderServiceClient(h.http, h.url)
	_, err := providers.SetSandboxProvider(ctx, connect.NewRequest(&podiumv1.SetSandboxProviderRequest{
		Name: "boat", Secret: []byte("boat_key"), MaxConcurrent: 1,
	}))
	require.NoError(t, err)

	a := enrollNode(t, h, "boat-a", boatLabels)
	b := enrollNode(t, h, "boat-b", boatLabels)
	a.open(ctx)
	defer a.disconnect()
	b.open(ctx)
	defer b.disconnect()
	require.NoError(t, a.stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Heartbeat{Heartbeat: &podiumv1.Heartbeat{
		FreeSlots: 4, SandboxAccountLimit: 5, Ts: timestamppb.Now(),
	}}}))

	first := h.createTask([]string{"backend=sandbox"}, "true")
	second := h.createTask([]string{"backend=sandbox"}, "true")
	waitFor(t, 10*time.Second, "one task placed and one held back", func() bool {
		x, y := h.getTask(first.GetId()), h.getTask(second.GetId())
		queued := x
		if x.GetStatus() != podiumv1.TaskStatus_TASK_STATUS_QUEUED {
			queued = y
		}
		return (x.GetStatus() == podiumv1.TaskStatus_TASK_STATUS_QUEUED) != (y.GetStatus() == podiumv1.TaskStatus_TASK_STATUS_QUEUED) &&
			strings.Contains(queued.GetQueuedReason(), "sandbox provider boat")
	})

	list, err := providers.ListSandboxProviders(ctx, connect.NewRequest(&podiumv1.ListSandboxProvidersRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetProviders(), 1)
	p := list.Msg.GetProviders()[0]
	require.Equal(t, int32(1), p.GetMaxConcurrent())
	require.Equal(t, int32(1), p.GetInUse())
	require.Equal(t, int32(5), p.GetAccountLimit())
}
