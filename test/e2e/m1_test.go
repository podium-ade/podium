//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestM1Acceptance is the MVP-0 acceptance script, run against real everything: the CLI
// creates a task, the node runs it in a container, the logs arrive live and the exit code
// comes back through the CLI's own exit status.
func TestM1Acceptance(t *testing.T) {
	h := newHarness(t)
	node := startNode(t, h, "demo")

	code, stdout, stderr := h.podium("run", "--image", "alpine:3", "--",
		"sh", "-c", "echo hi; sleep 5; exit 3")

	require.Contains(t, stdout, "hi", "the task's stdout reaches the CLI's stdout")
	require.Equal(t, 3, code, "podium run exits with the task's exit code\nstderr:\n%s", stderr)
	require.Contains(t, stderr, "→ running")
	require.Contains(t, stderr, "→ finished exit 3")
	require.NotContains(t, stdout, "→", "progress lines never pollute stdout")

	taskID := taskIDFrom(t, stderr)

	nodes := h.podiumOK("nodes")
	require.Equal(t, 2, len(strings.Split(strings.TrimSpace(nodes), "\n")),
		"exactly one node, plus the header:\n%s", nodes)
	require.Contains(t, nodes, "online")
	require.Contains(t, nodes, "demo")
	require.Contains(t, nodes, node.dataDirNodeID(t))

	var task struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exitCode"`
		NodeID   string `json:"nodeId"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.podiumOK("task", "get", taskID, "--json")), &task))
	require.Equal(t, "TASK_STATUS_FAILED", task.Status)
	require.Equal(t, 3, task.ExitCode)
	require.Equal(t, node.dataDirNodeID(t), task.NodeID)

	require.Contains(t, h.podiumOK("tasks"), taskID)
	require.Contains(t, h.podiumOK("logs", taskID), "hi")

	requireNoPodiumResources(t)
}

// TestDetachReturnsImmediately covers the other half of the run contract.
func TestDetachReturnsImmediately(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	code, stdout, stderr := h.podium("run", "--image", "alpine:3", "--detach", "--", "true")
	require.Equal(t, 0, code, "stderr:\n%s", stderr)
	taskID := strings.TrimSpace(stdout)
	require.True(t, strings.HasPrefix(taskID, "task_"), "--detach prints the task ID and nothing else: %q", stdout)

	waitForTaskStatus(t, h, taskID, "TASK_STATUS_SUCCEEDED", 3*time.Minute)
	requireNoPodiumResources(t)
}

// TestServerRestartMidTaskLeavesNoGap is scenario 2: the control plane goes away for five
// seconds in the middle of a run. The node buffers and replays; the CLI reconnects from
// the last sequence number it printed. Neither the output nor the task is allowed to
// suffer for it.
func TestServerRestartMidTaskLeavesNoGap(t *testing.T) {
	const ticks = 20

	h := newHarness(t)
	startNode(t, h)

	stdout := &drain{}
	stderr := &drain{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "podium"),
		"--server", h.url(), "--token", devToken,
		"run", "--image", "alpine:3", "--",
		"sh", "-c", fmt.Sprintf("for i in $(seq 1 %d); do echo tick $i; sleep 1; done; exit 0", ticks))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start())

	// Wait until output is actually flowing, then take the server away for five seconds.
	waitFor(t, 2*time.Minute, "the task to start producing output", func() bool {
		return strings.Contains(stdout.String(), "tick 3")
	}, func() string { return "cli stderr:\n" + stderr.String() })

	h.stopServer()
	time.Sleep(5 * time.Second)
	h.startServer()

	require.NoError(t, cmd.Wait(), "the task must still finish\nstdout:\n%s\nstderr:\n%s",
		stdout.String(), stderr.String())

	lines := nonEmptyLines(stdout.String())
	require.Len(t, lines, ticks, "every tick is present exactly once:\n%s", stdout.String())
	for i, line := range lines {
		require.Equal(t, fmt.Sprintf("tick %d", i+1), line, "no gap and no repeat in the output")
	}

	taskID := taskIDFrom(t, stderr.String())
	requireEventsExactlyOnce(t, h, taskID)
	requireNoPodiumResources(t)
}

// TestNodeRestartAdoptsItsContainers covers the acceptance checklist item: SIGTERM the
// node mid-task and the container keeps running; on restart the node finds it through
// ListOwned, reports it in Hello.running_task_ids, resumes streaming its output and
// finishes it.
func TestNodeRestartAdoptsItsContainers(t *testing.T) {
	const ticks = 25

	h := newHarness(t)
	node := startNode(t, h)

	stdout := h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
		"sh", "-c", fmt.Sprintf("for i in $(seq 1 %d); do echo tick $i; sleep 1; done; exit 7", ticks))
	taskID := strings.TrimSpace(stdout)

	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)
	waitFor(t, time.Minute, "the task to produce some output", func() bool {
		return strings.Contains(h.podiumOK("logs", taskID), "tick 3")
	})

	node.stop()
	require.Equal(t, "running", containerState(t, taskID),
		"SIGTERM to podium-node must not touch the task container")

	node.start("")
	node.awaitOnline()
	waitFor(t, 30*time.Second, "the node to report the adoption", func() bool {
		return strings.Contains(node.logs(), "adopting container left behind")
	}, node.logs)
	require.Contains(t, node.logs(), taskID)

	task := waitForTaskStatus(t, h, taskID, "TASK_STATUS_FAILED", 3*time.Minute)
	require.Equal(t, 7, task.ExitCode, "the adopted run reports the container's real exit code")

	lines := nonEmptyLines(h.podiumOK("logs", taskID))
	require.Len(t, lines, ticks, "the adopted task's output is complete and unrepeated")
	for i, line := range lines {
		require.Equal(t, fmt.Sprintf("tick %d", i+1), line)
	}
	requireEventsExactlyOnce(t, h, taskID)
	requireNoPodiumResources(t)
}

// TestCancelStopsARunningTask covers `podium task cancel`. The command must return at once
// even though the container only dies after the node's grace period, so the task command
// traps SIGTERM to keep the test to a few seconds rather than the full 30.
func TestCancelStopsARunningTask(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	stdout := h.podiumOK("run", "--image", "alpine:3", "--detach", "--",
		"sh", "-c", `trap 'exit 143' TERM; for i in $(seq 1 120); do echo tick $i; sleep 1; done`)
	taskID := strings.TrimSpace(stdout)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)

	started := time.Now()
	out := h.podiumOK("task", "cancel", taskID, "--reason", "e2e")
	require.Less(t, time.Since(started), 20*time.Second, "cancel must not block on the container dying")
	require.Contains(t, out, taskID)

	// The node's SIGTERM grace period is 30s, so allow the full window plus slack.
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_CANCELLED", 90*time.Second)
	requireNoPodiumResources(t)
}

// ---------------------------------------------------------------------------
// assertions that read the database directly
// ---------------------------------------------------------------------------

// requireEventsExactlyOnce is the replay-buffer assertion: every sequence number the node
// produced landed once, the two tables never claim the same one, and no error marker was
// recorded. A replayed batch is a no-op server side, so a reconnect must not show up here.
func requireEventsExactlyOnce(t *testing.T, h *harness, taskID string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `
		select seq, kind from task_events where task_id = $1
		union all
		select seq, 'log' from task_log_chunks where task_id = $1
		order by seq`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	seen := make(map[int64]string)
	var errorKinds []string
	var last int64
	for rows.Next() {
		var seq int64
		var kind string
		require.NoError(t, rows.Scan(&seq, &kind))
		prev, dup := seen[seq]
		require.False(t, dup, "seq %d stored twice (%s then %s)", seq, prev, kind)
		seen[seq] = kind
		require.Greater(t, seq, last, "sequence numbers must be strictly ascending")
		last = seq
		if kind == "error" {
			errorKinds = append(errorKinds, strconv.FormatInt(seq, 10))
		}
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, seen, "the task recorded no events at all")
	require.Empty(t, errorKinds, "no error markers: nothing was dropped and no assignment was refused")
}

type taskJSON struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	NodeID   string `json:"nodeId"`
}

func waitForTaskStatus(t *testing.T, h *harness, taskID, want string, timeout time.Duration) taskJSON {
	t.Helper()
	var task taskJSON
	waitFor(t, timeout, fmt.Sprintf("task %s to be %s", taskID, want), func() bool {
		code, stdout, _, err := runCLI(t, h.url(), 30*time.Second, "task", "get", taskID, "--json")
		if err != nil || code != 0 {
			return false
		}
		task = taskJSON{}
		if json.Unmarshal([]byte(stdout), &task) != nil {
			return false
		}
		return task.Status == want
	})
	return task
}

// dataDirNodeID reads the identity the node persisted, which is what ListNodes and the
// task's node_id must agree with.
func (n *nodeProc) dataDirNodeID(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(n.dataDir, "identity.json"))
	require.NoError(t, err)
	var id struct {
		NodeID string `json:"node_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &id))
	require.NotEmpty(t, id.NodeID)
	return id.NodeID
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
