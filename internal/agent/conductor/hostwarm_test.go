package conductor

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func warmJail(t *testing.T) hostPaths {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pod")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	jail, err := hostJail(root, "turn_1")
	require.NoError(t, err)
	return jail
}

func readInboxLine(t *testing.T, r *bufio.Reader) hostInboxLine {
	t.Helper()
	raw, err := r.ReadBytes('\n')
	require.NoError(t, err)
	var line hostInboxLine
	require.NoError(t, json.Unmarshal(raw, &line))
	return line
}

// TestAHostInboxHoldsWhatArrivesBeforeTheRuntimeDials: the conversation's next message can
// arrive while the runtime is still reconnecting. It is held, in order, and the runtime reads
// exactly the node's envelope.
func TestAHostInboxHoldsWhatArrivesBeforeTheRuntimeDials(t *testing.T) {
	in, err := listenHostInbox(warmJail(t))
	require.NoError(t, err)
	t.Cleanup(in.close)

	require.NoError(t, in.inject("also check the logs"))
	require.NoError(t, in.endWait())

	conn, err := net.Dial("unix", in.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	assert.Equal(t, hostInboxLine{V: 1, Text: "also check the logs"}, readInboxLine(t, r))
	assert.Equal(t, hostInboxLine{V: 1, EndWait: true}, readInboxLine(t, r))

	require.NoError(t, in.inject("and the metrics"))
	assert.Equal(t, hostInboxLine{V: 1, Text: "and the metrics"}, readInboxLine(t, r), "once connected, a line goes straight out")

	in.close()
	require.Error(t, in.inject("too late"), "a closed inbox refuses, so the message becomes the next turn")
}

// TestTheLongestIdleWarmHostGivesItsSlotUpFirst: a conversation that is only waiting for its
// next message yields to a turn with work, oldest first, and is then no longer warm.
func TestTheLongestIdleWarmHostGivesItsSlotUpFirst(t *testing.T) {
	c := &Conductor{sessions: map[string]*sessionState{}, metrics: NewMetrics(nil), logger: slog.Default()}
	assert.False(t, c.evictIdleHost(), "nothing warm, nothing to evict")

	older, err := listenHostInbox(warmJail(t))
	require.NoError(t, err)
	t.Cleanup(older.close)
	newer, err := listenHostInbox(warmJail(t))
	require.NoError(t, err)
	t.Cleanup(newer.close)

	c.setHostWarm("sess_old", older)
	time.Sleep(5 * time.Millisecond)
	c.setHostWarm("sess_new", newer)

	conn, err := net.Dial("unix", older.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	require.True(t, c.evictIdleHost())
	assert.Equal(t, hostInboxLine{V: 1, EndWait: true}, readInboxLine(t, bufio.NewReader(conn)),
		"the older conversation is told to stop waiting")
	assert.Nil(t, c.sessions["sess_old"].warmHost)
	assert.NotNil(t, c.sessions["sess_new"].warmHost, "the newer one keeps its slot")

	// A message for the newer one claims it: working again, so no longer evictable.
	assert.Same(t, newer, c.takeHostWarm("sess_new"))
	assert.Nil(t, c.takeHostWarm("sess_new"))
	assert.False(t, c.evictIdleHost())
}

func TestClearingAWarmHostOnlyForgetsTheSameTurn(t *testing.T) {
	c := &Conductor{sessions: map[string]*sessionState{}}
	first, err := listenHostInbox(warmJail(t))
	require.NoError(t, err)
	t.Cleanup(first.close)
	second, err := listenHostInbox(warmJail(t))
	require.NoError(t, err)
	t.Cleanup(second.close)

	c.setHostWarm("sess", second)
	c.clearHostWarm("sess", first)
	assert.NotNil(t, c.sessions["sess"].warmHost, "an earlier turn exiting does not forget a later one")
	c.clearHostWarm("sess", second)
	assert.Nil(t, c.sessions["sess"].warmHost)
}
