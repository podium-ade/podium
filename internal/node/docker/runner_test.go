package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainLines feeds the runner's decoded lines through drain and returns the executor events
// it produced, plus whatever it logged.
func drainLines(t *testing.T, lines ...runnerEvent) ([]Event, string) {
	t.Helper()
	var logged bytes.Buffer
	l := &runnerLink{
		log:    slog.New(slog.NewTextHandler(&logged, nil)),
		events: make(chan runnerEvent, len(lines)),
	}
	out := make(chan Event, len(lines)+1)
	done := l.drain("task_test", newEmitter(context.Background(), out), nil)
	for _, ev := range lines {
		l.events <- ev
	}
	close(l.events)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never finished")
	}
	close(out)

	var got []Event
	for e := range out {
		got = append(got, e)
	}
	return got, logged.String()
}

// TestDrainForwardsAMessageEvent: the node decodes, forwards and forgets. It does not check
// the attachment against the artifacts it has collected — at the moment the message arrives
// the file may still be uploading, and the relay is what resolves names.
func TestDrainForwardsAMessageEvent(t *testing.T) {
	got, _ := drainLines(t, runnerEvent{
		V: 1, Kind: runnerKindMessage, Type: "final",
		Text: "hello\nfrom inside", Attachments: []string{"shot.png"},
	})
	require.Len(t, got, 1)
	require.Equal(t, KindMessage, got[0].Kind)
	require.Equal(t, uint64(1), got[0].Seq)
	require.Equal(t, MessagePayload{
		Type: "final", Text: "hello\nfrom inside", Attachments: []string{"shot.png"},
	}, got[0].Payload)
}

// A message with no text has nothing to relay; it is dropped and said out loud.
func TestDrainDropsAMessageWithNoText(t *testing.T) {
	got, logged := drainLines(t, runnerEvent{V: 1, Kind: runnerKindMessage, Type: "final"})
	require.Empty(t, got)
	assert.Contains(t, logged, "runner message event with no text")
}

// An empty type is forwarded as it arrived: the set is open, and the reader decides what an
// unknown type means. The runner refuses to send one, so this is a forged or foreign line.
func TestDrainForwardsAMessageWithNoType(t *testing.T) {
	got, _ := drainLines(t, runnerEvent{V: 1, Kind: runnerKindMessage, Text: "typeless"})
	require.Len(t, got, 1)
	require.Equal(t, MessagePayload{Text: "typeless"}, got[0].Payload)
}

// A kind this node has never heard of still becomes a storable step event, which is what
// keeps a newer runner working under an older node.
func TestDrainTurnsAnUnknownKindIntoAStep(t *testing.T) {
	got, _ := drainLines(t,
		runnerEvent{V: 1, Kind: "plan", Status: "started"},
		runnerEvent{V: 1, Kind: runnerKindExited, ExitCode: 3},
	)
	require.Len(t, got, 1, "the runner's exit is only logged; ContainerWait is authoritative")
	require.Equal(t, KindStep, got[0].Kind)
	require.Equal(t, StepPayload{Name: "plan", Status: "started"}, got[0].Payload)
}

// newTestLink opens a real event socket in a temporary data dir, with no Docker engine
// anywhere near it. The dir is not t.TempDir(): on Darwin that path alone is longer than
// the AF_UNIX sun_path budget.
func newTestLink(t *testing.T) *runnerLink {
	t.Helper()
	dataDir, err := os.MkdirTemp("/tmp", "pdmrun")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	e := &Executor{dataDir: dataDir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	link, err := e.listenRunner("task_test")
	require.NoError(t, err)
	t.Cleanup(link.close)
	return link
}

// TestStopAcceptingKeepsTheSocketFile is the regression guard for silently lost artifacts.
// The socket file is the bind-mount SOURCE of /podium/events.sock, and artifacts are
// collected with CopyFromContainer *after* the run stops accepting. net.Listen("unix")
// unlinks its socket on Close by default, which deleted that source while the container was
// still there — and the daemon then could not re-materialise the mount point, so the copy
// failed and every file under /workspace/.podium/artifacts vanished.
func TestStopAcceptingKeepsTheSocketFile(t *testing.T) {
	link := newTestLink(t)
	require.FileExists(t, link.path)

	link.stopAccepting()
	_, err := os.Lstat(link.path)
	require.NoError(t, err, "stopAccepting deleted the bind-mount source artifact collection needs")

	// Still refusing new connections, which is the whole point of stopAccepting.
	_, err = net.Dial("unix", link.path)
	require.Error(t, err)
}

// close is what actually retires the socket, and Teardown removes it again for an executor
// that keeps its sockets outside the task directory.
func TestCloseRemovesTheSocketFile(t *testing.T) {
	link := newTestLink(t)
	link.close()
	require.NoFileExists(t, link.path)
}

// TestIsMissingPathOnlyIgnoresAnAbsentPath: the collector may swallow a directory that is
// not there, and nothing else. The second case is the daemon's real complaint from the run
// that lost two artifacts — an engine-side failure, not an absent directory.
func TestIsMissingPathOnlyIgnoresAnAbsentPath(t *testing.T) {
	require.True(t, isMissingPath(fmt.Errorf("copy out: %w", cerrdefs.ErrNotFound)))
	require.False(t, isMissingPath(errors.New(
		"Error response from daemon: mkdirat podium/events.sock: file exists")))
	require.False(t, isMissingPath(context.DeadlineExceeded))
}
