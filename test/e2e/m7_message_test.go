//go:build e2e

package e2e_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunnerMessageReachesTheCLI is the step's acceptance path driven the way an agent will
// drive it: a real node, a real container, `podium-runner message` inside it, and the real
// CLI reading what came back.
//
// The final message names an attachment that does not exist. That is deliberate: an
// attachment is an artifact *name* the reader resolves, and proving the node does not
// validate it is proving that a message emitted while an upload is still in flight is not
// dropped on the floor.
func TestRunnerMessageReachesTheCLI(t *testing.T) {
	h := newHarness(t)
	startNode(t, h)

	code, stdout, stderr := h.podium("run", "--image", "alpine:3", "--",
		"sh", "-c", "/podium/runner message --type progress 'working' && "+
			"/podium/runner message --type final --attach out.txt 'hello from inside' && "+
			"echo done")
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.Equal(t, "done\n", stdout, "stdout is the task's own output, byte for byte")

	// Every message, and only messages, on stderr — in the order the task said them.
	progress := strings.Index(stderr, "→ message (progress): working")
	final := strings.Index(stderr, "→ message (final): hello from inside")
	attach := strings.Index(stderr, "(attachments: out.txt)")
	require.Positive(t, progress, "no progress message in:\n%s", stderr)
	require.Greater(t, final, progress, "the messages must arrive in order:\n%s", stderr)
	require.Greater(t, attach, final, "the attachment list follows its message:\n%s", stderr)

	taskID := taskIDFrom(t, stderr)
	rows := taskMessages(t, h.databaseURL, taskID)
	require.Len(t, rows, 2, "one task_events row per message")
	assert.Equal(t, storedMessage{Type: "progress", Text: "working", Attachments: "[]"}, rows[0])
	assert.Equal(t, storedMessage{
		Type: "final", Text: "hello from inside", Attachments: `["out.txt"]`,
	}, rows[1])

	requireNoPodiumResources(t)
}

type storedMessage struct {
	Type        string
	Text        string
	Attachments string
}

// taskMessages reads a task's message rows straight out of Postgres, in seq order: the
// payload the control plane stored is what step 17's relay will read.
func taskMessages(t *testing.T, databaseURL, taskID string) []storedMessage {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `select payload->>'type', payload->>'text',
		(payload->'attachments')::text
		from task_events where task_id = $1 and kind = 'message' order by seq`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	var out []storedMessage
	for rows.Next() {
		var m storedMessage
		require.NoError(t, rows.Scan(&m.Type, &m.Text, &m.Attachments))
		out = append(out, m)
	}
	require.NoError(t, rows.Err())
	return out
}
