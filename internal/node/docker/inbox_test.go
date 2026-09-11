package docker

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testInbox(t *testing.T) *inboxLink {
	t.Helper()
	dataDir, err := os.MkdirTemp("/tmp", "pdminbox")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	e := &Executor{dataDir: dataDir, log: discardLog()}
	inbox, err := e.listenInbox("task_1")
	require.NoError(t, err)
	t.Cleanup(inbox.close)
	return inbox
}

func TestInboxDeliversABufferedInjectWhenTheRuntimeDials(t *testing.T) {
	inbox := testInbox(t)

	require.NoError(t, inbox.write("use the other branch"))

	conn, err := net.Dial("unix", inbox.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)
	var got inboxEnvelope
	require.NoError(t, json.Unmarshal(line, &got))
	require.Equal(t, 1, got.V)
	require.Equal(t, "use the other branch", got.Text)
}

func TestInboxWritesToAConnectedRuntime(t *testing.T) {
	inbox := testInbox(t)

	conn, err := net.DialTimeout("unix", inbox.path, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Give accept() a moment to take the connection.
	require.Eventually(t, func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		return inbox.conn != nil
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, inbox.write("yes, ship it"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)
	var got inboxEnvelope
	require.NoError(t, json.Unmarshal(line, &got))
	require.Equal(t, "yes, ship it", got.Text)
}
