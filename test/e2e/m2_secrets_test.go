//go:build e2e

package e2e_test

import (
	"context"
	"crypto/md5" //nolint:gosec // used to fingerprint a test fixture, never as a security control
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secretValue is deliberately long, distinctive and not a real credential. It has to be at
// least MinRedactableSecret bytes or the node would not search the log stream for it.
const secretValue = "correct-horse-battery-staple-42"

// deployKey is a multi-line fake: `podium secret set` strips one trailing newline from
// stdin, so what is stored is exactly this string.
const deployKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-real-key-just-a-test-fixture"

// md5hex is how a task proves it received the exact bytes without printing them: the
// digest is not a form of the secret, so the redactor leaves it alone.
func md5hex(value string) string {
	sum := md5.Sum([]byte(value)) //nolint:gosec // a test fixture digest, not a security control
	return hex.EncodeToString(sum[:])
}

// setSecret pipes a value into `podium secret set` on stdin, which is how the docs tell an
// operator to do it.
func setSecret(t *testing.T, h *harness, name, value string) {
	t.Helper()
	code, stdout, stderr, err := runCLIStdin(t, h.url(), value, "secret", "set", name)
	require.NoError(t, err)
	require.Equal(t, 0, code, "podium secret set %s failed\nstdout:\n%s\nstderr:\n%s", name, stdout, stderr)
	require.NotContains(t, stdout, value, "the value must never be echoed back")
	require.NotContains(t, stderr, value)
}

// storedLogs concatenates every task_log_chunks row for a task, straight from Postgres.
// This is the ground truth for redaction: what the server actually persisted.
func storedLogs(t *testing.T, h *harness, taskID string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx,
		"select bytes from task_log_chunks where task_id = $1 order by seq", taskID)
	require.NoError(t, err)
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var chunk []byte
		require.NoError(t, rows.Scan(&chunk))
		b.Write(chunk)
	}
	require.NoError(t, rows.Err())
	return b.String()
}

func taskFailureReason(t *testing.T, h *harness, taskID string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var reason *string
	require.NoError(t, conn.QueryRow(ctx,
		"select failure_reason from tasks where id = $1", taskID).Scan(&reason))
	if reason == nil {
		return ""
	}
	return *reason
}

// TestSecretRoundTripAndRedaction is the whole of step 09 in one run.
//
// The container's own stdout contains the secret — the task echoes $GREETING and exits 0
// only if the value is byte-for-byte what was set — while everything Podium carries and
// stores says [redacted:GREETING] instead.
//
// Note what that means for `podium run`: redaction happens on the node, before a chunk is
// ever buffered or sent, so the operator's terminal shows the marker too. There is exactly
// one log path and the value does not travel it. The exit status and the length line are
// the proof the value reached the container, not the echoed bytes.
func TestSecretRoundTripAndRedaction(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	setSecret(t, h, "GREETING", secretValue)

	// The task echoes the value, then prints its length and its MD5. The digest is what
	// proves the bytes arrived intact; putting the value itself in the command would put
	// it in the task spec, which is stored in Postgres in the clear.
	const script = `echo "$GREETING"; echo "len=${#GREETING}"; ` +
		`printf %s "$GREETING" | md5sum | cut -d" " -f1`
	code, stdout, stderr := h.podium("run",
		"--secret", "GREETING:env:GREETING",
		"--image", "alpine:3", "--", "sh", "-c", script)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	t.Logf("podium run --secret GREETING:env:GREETING\n--- stdout ---\n%s--- stderr ---\n%s", stdout, stderr)

	// The value really did reach the container, whole and unmodified.
	assert.Contains(t, stdout, "len=31")
	assert.Contains(t, stdout, md5hex(secretValue), "the container did not receive the exact value")

	// And nothing that left the node carries it.
	assert.NotContains(t, stdout, secretValue, "the value reached the operator's terminal")
	assert.Contains(t, stdout, "[redacted:GREETING]")

	taskID := taskIDFrom(t, stderr)
	stored := storedLogs(t, h, taskID)
	t.Logf("stored task_log_chunks for %s: %q", taskID, stored)
	assert.NotContains(t, stored, secretValue, "the secret value was persisted in task_log_chunks")
	assert.Contains(t, stored, "[redacted:GREETING]")

	// `podium logs` replays the stored rows, so it shows the marker too.
	_, logsOut, _ := h.podium("logs", taskID)
	assert.Contains(t, logsOut, "[redacted:GREETING]")
	assert.NotContains(t, logsOut, secretValue)

	// The task's spec is stored and served; it carries the name, never the value.
	specJSON := h.podiumOK("task", "get", taskID, "--json")
	assert.NotContains(t, specJSON, secretValue)
	assert.Contains(t, specJSON, "GREETING", "the ref itself is visible: a name is not a secret")

	requireNoPodiumResources(t)
}

// The bare `--secret NAME` shorthand means "put it in an environment variable of the same
// name", which is the form the docs lead with.
func TestSecretShorthandAndFileTarget(t *testing.T) {
	h := newHarness(t)
	node := startNode(t, h)

	setSecret(t, h, "GREETING", secretValue)
	setSecret(t, h, "DEPLOY_KEY", deployKey+"\n")

	// Every assertion is on something derived from the value rather than on the value, so
	// the run proves both halves at once: the secret arrived, and it did not leave.
	const script = `printf %s "$GREETING" | md5sum | cut -d" " -f1; ` +
		`md5sum < /podium/secrets/deploy_key | cut -d" " -f1; ` +
		`ls -l /podium/secrets/deploy_key; ` +
		`echo "leak=$GREETING"`

	code, stdout, stderr := h.podium("run",
		"--secret", "GREETING",
		"--secret", "DEPLOY_KEY:file:/podium/secrets/deploy_key",
		"--image", "alpine:3", "--", "sh", "-c", script)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	t.Logf("--- stdout ---\n%s", stdout)

	assert.Contains(t, stdout, md5hex(secretValue), "the bare --secret NAME shorthand did not set $GREETING")
	assert.Contains(t, stdout, md5hex(deployKey), "the file target does not hold the value that was set")
	assert.Contains(t, stdout, "-r--------", "a file target lands 0400 inside the container")
	assert.Contains(t, stdout, "leak=[redacted:GREETING]", "a task echoing its own secret is redacted")

	taskID := taskIDFrom(t, stderr)
	stored := storedLogs(t, h, taskID)
	assert.NotContains(t, stored, secretValue)
	assert.NotContains(t, stored, "not-a-real-key", "the file secret is redacted from the stored logs too")
	assert.NotContains(t, stored, deployKey)
	assert.Contains(t, stored, "[redacted:GREETING]")

	// Nothing survives teardown: the node's per-task directory, staged secret files and
	// all, is gone.
	taskDir := filepath.Join(node.dataDir, "tasks", taskID)
	waitFor(t, 60*time.Second, "the node's task directory to be removed", func() bool {
		_, err := os.Stat(taskDir)
		return os.IsNotExist(err)
	}, func() string { return "node log:\n" + node.logs() })

	requireNoPodiumResources(t)
}

// The acceptance item: a task naming a secret that does not exist fails before a node ever
// sees it, and says which secret.
func TestMissingSecretFailsTheTaskBeforeItIsScheduled(t *testing.T) {
	h := newHarness(t)
	node := startNode(t, h)

	code, stdout, stderr := h.podium("run",
		"--secret", "NOT_SET_ANYWHERE",
		"--image", "alpine:3", "--", "sh", "-c", "echo this should never run")
	require.Equal(t, 125, code, "a task that never produced an exit code exits 125")
	assert.NotContains(t, stdout, "this should never run")
	t.Logf("--- stderr ---\n%s", stderr)

	taskID := taskIDFrom(t, stderr)
	var task struct {
		Status        string `json:"status"`
		NodeID        string `json:"nodeId"`
		FailureReason string `json:"failureReason"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.podiumOK("task", "get", taskID, "--json")), &task))
	assert.Equal(t, "TASK_STATUS_FAILED", task.Status)
	assert.Empty(t, task.NodeID, "the task must never have been assigned to a node")
	assert.Contains(t, task.FailureReason, "NOT_SET_ANYWHERE")
	assert.Contains(t, stderr, "NOT_SET_ANYWHERE", "`podium run` says which secret is missing")

	reason := taskFailureReason(t, h, taskID)
	t.Logf("failure_reason: %s", reason)
	assert.Contains(t, reason, "NOT_SET_ANYWHERE", "failure_reason names the secret")
	assert.Contains(t, reason, "missing secret")

	// The node never heard about it, so it never created anything.
	assert.NotContains(t, node.logs(), taskID, "the node was told about a task it should never have seen")
	requireNoPodiumResources(t)
}

// `podium secret ls` and `rm` are the rest of the operator surface. There is no `get`.
func TestSecretListAndRemove(t *testing.T) {
	h := newHarness(t)

	setSecret(t, h, "ALPHA", "a value for alpha secret")
	setSecret(t, h, "BRAVO", "a value for bravo secret")
	setSecret(t, h, "ALPHA", "a replacement value for alpha")

	out := h.podiumOK("secret", "ls")
	t.Logf("podium secret ls:\n%s", out)
	assert.Contains(t, out, "ALPHA")
	assert.Contains(t, out, "BRAVO")
	assert.NotContains(t, out, "a value for", "listing must never show a value")

	// ALPHA was set twice, so it is at version 2 and BRAVO at 1.
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "ALPHA" {
			assert.Equal(t, "2", fields[1], "setting a secret again bumps its version")
		}
		if len(fields) >= 2 && fields[0] == "BRAVO" {
			assert.Equal(t, "1", fields[1])
		}
	}

	h.podiumOK("secret", "rm", "BRAVO")
	out = h.podiumOK("secret", "ls")
	assert.Contains(t, out, "ALPHA")
	assert.NotContains(t, out, "BRAVO")

	code, _, _ := h.podium("secret", "rm", "BRAVO")
	assert.NotEqual(t, 0, code, "removing a secret that is gone is an error")

	// There is no `podium secret get`, and there never will be.
	code, _, stderr := h.podium("secret", "get", "ALPHA")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "unknown command")
}
