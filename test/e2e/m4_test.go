//go:build e2e

package e2e_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/scheduler"
)

// fastTiming is the shrunk policy PODIUM_TEST_FAST_TIMERS=1 gives the control plane. The
// waits below are expressed against it rather than in literal seconds, so a change to the
// shipped numbers cannot leave a test asserting something that is no longer true.
var fastTiming = scheduler.DefaultTiming().Fast()

// fastHarness starts a control plane whose timers are shrunk tenfold, so waiting out the
// two-minute offline threshold costs twelve seconds instead. server.New reads the variable,
// which is why it is set before the harness rather than after.
func fastHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv(scheduler.FastTimersEnv, "1")
	return newHarness(t)
}

// TestKilledNodeLosesItsTaskAndDropsTheOrphanWhenItReturns is the node-loss chaos scenario,
// end to end against a real Docker engine.
//
// SIGKILL the daemon and its container keeps running — that is the design, and it is what
// makes an upgrade cheap. But the control plane cannot tell that from the machine having
// burned down, so after the offline threshold it stops waiting: the task is `lost`, not
// `failed`, because nothing about the task went wrong.
//
// Then the interesting half. The daemon comes back still holding a container for a task the
// control plane has written off. Reconciliation tells it so, it tears the container down,
// and nothing is left running that nobody is watching.
func TestKilledNodeLosesItsTaskAndDropsTheOrphanWhenItReturns(t *testing.T) {
	h := fastHarness(t)
	node := startNode(t, h)

	stdout := h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
		"sh", "-c", "for i in $(seq 1 600); do echo tick $i; sleep 1; done")
	taskID := strings.TrimSpace(stdout)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)

	node.kill()
	require.Equal(t, "running", containerState(t, taskID),
		"SIGKILL to podium-node must not touch the task container")

	lost := waitForTaskStatus(t, h, taskID, "TASK_STATUS_LOST", 6*fastTiming.OfflineAfter)
	assert.Contains(t, lost.FailureReason, "offline")
	assert.Equal(t, "running", containerState(t, taskID),
		"the container outlives the daemon; only the daemon can remove it")

	waitFor(t, 30*time.Second, "the node to be reported offline", func() bool {
		return strings.Contains(h.podiumOK("nodes"), "offline")
	})

	// It comes back holding a container nobody wants any more.
	node.start("")
	node.awaitOnline()
	waitFor(t, 60*time.Second, "the node to be told to drop the orphan", func() bool {
		return strings.Contains(node.logs(), "tearing down an orphaned container")
	}, node.logs)

	requireNoPodiumResources(t)
	assert.Equal(t, "TASK_STATUS_LOST", waitForTaskStatus(t, h, taskID, "TASK_STATUS_LOST", 10*time.Second).Status,
		"tearing the container down does not resurrect the task")
}

// TestTimeoutIsEnforcedByTheServer covers the acceptance item: `timeout: 10s` on a task that
// sleeps far longer is stopped by the control plane, and the reason survives into the row.
//
// The timeout is not a node-side alarm. The server notices, records a durable intent and
// sends Cancel; the node's SIGTERM is what actually stops the container, so the exit code
// and the usage are still the container's own.
func TestTimeoutIsEnforcedByTheServer(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	started := time.Now()
	stdout := h.podiumOK("run", "--image", "alpine:3", "--detach", "--timeout", "10s", "--",
		"sh", "-c", "sleep 300")
	taskID := strings.TrimSpace(stdout)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)

	// 10s timeout + a 5s watchdog tick + the node's SIGTERM: the design's "within 15s and
	// the grace period".
	task := waitForTaskStatus(t, h, taskID, "TASK_STATUS_FAILED", 90*time.Second)
	assert.Equal(t, scheduler.ReasonTimeout, task.FailureReason)
	assert.Less(t, time.Since(started), 90*time.Second)
	t.Logf("a 10s timeout on `sleep 300` ended the task after %s with exit code %d",
		time.Since(started).Round(time.Second), task.ExitCode)

	requireNoPodiumResources(t)
}

// TestDrainedNodeFinishesItsWorkAndExits is the upgrade path: drain, let the running task
// finish, exit 0, and take nothing new in the meantime.
func TestDrainedNodeFinishesItsWorkAndExits(t *testing.T) {
	h := newHarness(t)
	node := startNodeWithEnv(t, h, []string{"PODIUM_NODE_EXIT_ON_DRAIN=1"})
	nodeID := node.dataDirNodeID(t)

	running := strings.TrimSpace(h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
		"sh", "-c", "sleep 20; echo done"))
	waitForTaskStatus(t, h, running, "TASK_STATUS_RUNNING", 2*time.Minute)

	out := h.podiumOK("node", "drain", nodeID)
	assert.Contains(t, out, "draining")
	waitFor(t, 30*time.Second, "the node to report draining", func() bool {
		return strings.Contains(h.podiumOK("nodes"), "draining")
	})

	// Nothing new lands on it while it is draining.
	waiting := strings.TrimSpace(h.podiumOK("run", "--image", "alpine:3", "--detach", "--", "true"))
	waitFor(t, 30*time.Second, "the scheduler to say why the second task is waiting", func() bool {
		return waitForTaskStatus(t, h, waiting, "TASK_STATUS_QUEUED", 10*time.Second).QueuedReason != ""
	})
	queued := waitForTaskStatus(t, h, waiting, "TASK_STATUS_QUEUED", 10*time.Second)
	assert.Equal(t, scheduler.ReasonAllDraining, queued.QueuedReason)
	assert.Empty(t, queued.NodeID)

	// The task it was already running finishes normally, and then the daemon exits 0.
	done := waitForTaskStatus(t, h, running, "TASK_STATUS_SUCCEEDED", 2*time.Minute)
	assert.Equal(t, 0, done.ExitCode)
	assert.Equal(t, 0, node.awaitExit(60*time.Second),
		"--exit-on-drain means the supervisor gets a clean exit to restart from\nnode log:\n"+node.logs())

	// The queued task is still queued: there is nothing left to run it.
	assert.Equal(t, "TASK_STATUS_QUEUED", waitForTaskStatus(t, h, waiting, "TASK_STATUS_QUEUED", 10*time.Second).Status)
	requireNoPodiumResources(t)
}

// TestAssignmentToAFullNodeWaitsRatherThanFailing covers the capacity half of the scheduler
// against the real daemon: a node with one slot runs one task at a time, and the rest wait
// their turn instead of being refused.
func TestAssignmentToAFullNodeWaitsRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	startNodeWithEnv(t, h, []string{"PODIUM_NODE_MAX_TASKS=1"})

	var ids []string
	for i := range 3 {
		out := h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
			"sh", "-c", fmt.Sprintf("echo task %d; sleep 2", i))
		ids = append(ids, strings.TrimSpace(out))
	}

	for _, id := range ids {
		task := waitForTaskStatus(t, h, id, "TASK_STATUS_SUCCEEDED", 3*time.Minute)
		assert.Equal(t, 0, task.ExitCode)
		assert.EqualValues(t, 1, task.Attempts, "a task that waited its turn is not a retry")
	}
	requireNoPodiumResources(t)
}

// TestALongControlPlaneOutageOnlyCostsUnreachable is the closest this machine can get to the
// checklist's "block the node's network for 60s": the control plane goes away for 35 seconds,
// which is past the 30s unreachable threshold and well short of the 120s offline one.
//
// Nothing about the task may change. The container keeps running, the node buffers its
// output and replays it on reconnect, and the log the operator finally reads has no gap and
// no repeat. The only thing that moves is the node's own status, and it moves back.
//
// A real iptables block would exercise the same code path from the other end; it needs root
// on Linux, which this build host is not.
func TestALongControlPlaneOutageOnlyCostsUnreachable(t *testing.T) {
	const ticks = 90

	h := newHarness(t)
	node := startNode(t, h)

	taskID := strings.TrimSpace(h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
		"sh", "-c", fmt.Sprintf("for i in $(seq 1 %d); do echo tick $i; sleep 1; done; exit 0", ticks)))
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)
	waitFor(t, time.Minute, "the task to produce some output", func() bool {
		return strings.Contains(h.podiumOK("logs", taskID), "tick 3")
	})

	h.stopServer()
	time.Sleep(35 * time.Second)
	h.startServer()

	// The node reconnects on its own backoff and the task carries on where it was.
	node.awaitOnline()
	task := waitForTaskStatus(t, h, taskID, "TASK_STATUS_SUCCEEDED", 4*time.Minute)
	assert.Equal(t, 0, task.ExitCode)
	assert.Empty(t, task.FailureReason, "an outage the node survived is not a failure")

	lines := nonEmptyLines(h.podiumOK("logs", taskID))
	require.Len(t, lines, ticks, "every tick is present exactly once:\n%s", strings.Join(lines, "\n"))
	for i, line := range lines {
		require.Equal(t, fmt.Sprintf("tick %d", i+1), line, "no gap and no repeat across the outage")
	}
	requireEventsExactlyOnce(t, h, taskID)
	requireNoPodiumResources(t)
}
