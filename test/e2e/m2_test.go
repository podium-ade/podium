//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestPostgresSidecarExample runs the shipped example end to end: the CLI, the server, a
// real node, a real Postgres sidecar and a real psql. It is the whole point of step 08 —
// an adopter's "database plus app" environment running unchanged.
func TestPostgresSidecarExample(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	root, err := repoRoot()
	require.NoError(t, err)

	code, stdout, stderr := h.podium("run", "--spec", filepath.Join(root, "examples", "postgres-sidecar.yaml"))
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	t.Logf("podium run --spec examples/postgres-sidecar.yaml\n--- stderr ---\n%s--- stdout ---\n%s", stderr, stdout)

	// psql's own output is the task's stdout.
	require.Contains(t, stdout, "?column?")
	require.Contains(t, stdout, "(1 row)")

	// The sidecar's lifecycle and its output are visible, and its output is tagged so it
	// cannot be mistaken for the task's.
	require.Contains(t, stderr, "→ sidecar/db started")
	require.Contains(t, stderr, "→ sidecar/db ready")
	require.Contains(t, stderr, "[db] ")
	require.Contains(t, stderr, "database system is ready to accept connections")
	require.NotContains(t, stdout, "[db] ", "a sidecar's log never pollutes the task's stdout")

	taskID := taskIDFrom(t, stderr)

	var task struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exitCode"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.podiumOK("task", "get", taskID, "--json")), &task))
	require.Equal(t, "TASK_STATUS_SUCCEEDED", task.Status)
	require.Equal(t, 0, task.ExitCode)

	// `podium logs` renders the same prefixed sidecar output from the stored history.
	_, logsOut, logsErr := h.podium("logs", taskID)
	require.Contains(t, logsOut, "(1 row)")
	require.Contains(t, logsErr, "[db] ")

	requireNoPodiumResources(t)
}

// TestSidecarThatNeverBecomesReadyFailsTheTask covers the design's "sidecar never becomes
// ready" row: the task fails at provisioning and the operator is shown the sidecar's own
// error, not a generic one.
func TestSidecarThatNeverBecomesReadyFailsTheTask(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	spec := writeSpec(t, `
image: alpine:3
command: ["echo", "the task must never run"]
sidecars:
  db:
    image: alpine:3
    command: ["sh", "-c", "echo 'FATAL: data directory has wrong ownership'; sleep 300"]
    readiness:
      tcp_port: 9999
      timeout: 5s
`)

	code, stdout, stderr := h.podium("run", "--spec", spec)
	// The task never produced an exit code, so the CLI reports an infrastructure failure
	// rather than reading the unset exit_code as a success.
	require.Equal(t, 125, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.NotContains(t, stdout, "the task must never run")
	require.Contains(t, stderr, "sidecar db not ready")
	require.Contains(t, stderr, "[db] FATAL: data directory has wrong ownership")
	t.Logf("podium run with a sidecar that never listens\n--- stderr ---\n%s", stderr)

	taskID := taskIDFrom(t, stderr)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_FAILED", time.Minute)
	requireNoPodiumResources(t)
}

// TestOOMKilledTaskIsReportedAsOOM: a memory limit must produce a diagnosis, not a bare
// non-zero exit. The wire has no failure_reason field yet, so the column is read directly.
func TestOOMKilledTaskIsReportedAsOOM(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	spec := writeSpec(t, `
image: alpine:3
command: ["tail", "/dev/zero"]
resources:
  memory_mb: 64
`)

	code, _, stderr := h.podium("run", "--spec", spec)
	require.NotEqual(t, 0, code, "stderr:\n%s", stderr)

	taskID := taskIDFrom(t, stderr)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_FAILED", time.Minute)
	require.Equal(t, "oom", failureReason(t, h, taskID))

	requireNoPodiumResources(t)
}

// TestOrdinaryFailureIsNotReportedAsOOM guards the other direction: the OOM diagnosis must
// not leak onto a task that simply exited non-zero.
func TestOrdinaryFailureIsNotReportedAsOOM(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	code, _, stderr := h.podium("run", "--image", "alpine:3", "--", "sh", "-c", "exit 9")
	require.Equal(t, 9, code, "stderr:\n%s", stderr)

	taskID := taskIDFrom(t, stderr)
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_FAILED", time.Minute)
	require.Empty(t, failureReason(t, h, taskID))

	requireNoPodiumResources(t)
}

// writeSpec puts a task spec in the test's temp dir and returns its path.
func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimLeft(body, "\n")), 0o600))
	return path
}

// failureReason reads tasks.failure_reason, which is where the server records why a task
// died when the exit code alone does not say.
func failureReason(t *testing.T, h *harness, taskID string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var reason *string
	require.NoError(t, conn.QueryRow(ctx,
		`select failure_reason from tasks where id = $1`, taskID).Scan(&reason))
	if reason == nil {
		return ""
	}
	return *reason
}
