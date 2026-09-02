//go:build integration

package docker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

const testImage = "alpine:3"

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	e, err := New(context.Background(), Options{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	return e
}

// collector drains the event channel so Run never blocks on a slow reader.
type collector struct {
	ch   chan Event
	done chan struct{}

	mu     sync.Mutex
	events []Event
}

func newCollector() *collector {
	c := &collector{ch: make(chan Event, 64), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for ev := range c.ch {
			c.mu.Lock()
			c.events = append(c.events, ev)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) finish() []Event {
	close(c.ch)
	<-c.done
	return c.events
}

func assertSeq(t *testing.T, events []Event) {
	t.Helper()
	require.NotEmpty(t, events)
	for i, ev := range events {
		require.Equal(t, uint64(i+1), ev.Seq, "event %d (%s) broke the sequence", i, ev.Kind)
		require.False(t, ev.TS.IsZero(), "event %d (%s) has no timestamp", i, ev.Kind)
	}
}

func kindsOf(events []Event) []string {
	kinds := make([]string, len(events))
	for i, ev := range events {
		kinds[i] = ev.Kind
	}
	return kinds
}

func indexOfKind(events []Event, kind string) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return -1
}

func teardownAfter(t *testing.T, e *Executor, taskID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, e.Teardown(ctx, taskID, false))
	})
}

func requireNoLeaks(t *testing.T, e *Executor, taskID string) {
	t.Helper()
	ctx := context.Background()

	owned, err := e.ListOwned(ctx)
	require.NoError(t, err)
	for _, c := range owned {
		require.NotEqual(t, taskID, c.TaskID, "container %s leaked", c.ID)
	}

	_, err = e.cli.NetworkInspect(ctx, networkName(taskID), network.InspectOptions{})
	require.True(t, cerrdefs.IsNotFound(err), "network %s leaked (err=%v)", networkName(taskID), err)

	_, err = e.cli.VolumeInspect(ctx, volumeName(taskID))
	require.True(t, cerrdefs.IsNotFound(err), "volume %s leaked (err=%v)", volumeName(taskID), err)
}

func TestRunEmitsOrderedEventsAndExitCode(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo hi; echo err >&2; exit 3"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 3, res.ExitCode)
	require.False(t, res.OOMKilled)
	require.Positive(t, res.Usage.WallMS)

	events := c.finish()
	assertSeq(t, events)
	kinds := kindsOf(events)
	t.Logf("event kinds: %v", kinds)

	require.Equal(t, KindProvisioning, kinds[0])
	require.Equal(t, KindFinished, kinds[len(kinds)-1])
	require.Equal(t, KindExited, kinds[len(kinds)-2])
	require.NotContains(t, kinds, KindError)

	// alpine:3 is present locally, so this run does not pull.
	started := indexOfKind(events, KindStarted)
	require.Positive(t, started, "started must follow provisioning")
	for i, ev := range events {
		if ev.Kind == KindPulling {
			require.Less(t, i, started, "pulling must precede started")
		}
		if ev.Kind == KindLog {
			require.Greater(t, i, started, "log must follow started")
			require.Less(t, i, len(events)-2, "log must precede exited")
		}
	}

	var stdout, stderr strings.Builder
	firstStdout, firstStderr := -1, -1
	for i, ev := range events {
		if ev.Kind != KindLog {
			continue
		}
		p, ok := ev.Payload.(LogPayload)
		require.True(t, ok, "log payload must be LogPayload")
		switch p.Stream {
		case StreamStdout:
			stdout.Write(p.Bytes)
			if firstStdout < 0 {
				firstStdout = i
			}
		case StreamStderr:
			stderr.Write(p.Bytes)
			if firstStderr < 0 {
				firstStderr = i
			}
		default:
			t.Fatalf("unexpected log stream %q", p.Stream)
		}
	}
	require.Equal(t, "hi\n", stdout.String())
	require.Equal(t, "err\n", stderr.String())
	require.Positive(t, firstStdout)
	require.Positive(t, firstStderr)

	exited, ok := events[len(events)-2].Payload.(ExitedPayload)
	require.True(t, ok)
	require.Equal(t, ExitedPayload{ExitCode: 3, OOMKilled: false}, exited)

	finished, ok := events[len(events)-1].Payload.(FinishedPayload)
	require.True(t, ok)
	require.Equal(t, 3, finished.ExitCode)
	require.Positive(t, finished.Usage.WallMS)
}

// TestLogStreamsKeepTheirRelativeOrder proves stdout and stderr are interleaved
// in real time rather than demultiplexed into two separate runs. The acceptance
// command writes both lines in the same millisecond, which the daemon's two log
// collector goroutines are free to record in either order, so this variant puts
// a second between them.
func TestLogStreamsKeepTheirRelativeOrder(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo hi; sleep 1; echo err >&2; sleep 1; echo bye"},
		},
	}, c.ch)
	require.NoError(t, err)

	events := c.finish()
	assertSeq(t, events)

	var seen []string
	for _, ev := range events {
		if ev.Kind != KindLog {
			continue
		}
		p := ev.Payload.(LogPayload)
		seen = append(seen, p.Stream+":"+strings.TrimSpace(string(p.Bytes)))
	}
	require.Equal(t, []string{"stdout:hi", "stderr:err", "stdout:bye"}, seen)
}

func TestRunSeqHasNoGapsUnderHeavyOutput(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "i=0; while [ $i -lt 400 ]; do echo out $i; echo err $i >&2; i=$((i+1)); done"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)

	var out, errOut int
	for _, ev := range events {
		if ev.Kind != KindLog {
			continue
		}
		p := ev.Payload.(LogPayload)
		switch p.Stream {
		case StreamStdout:
			out += strings.Count(string(p.Bytes), "\n")
		case StreamStderr:
			errOut += strings.Count(string(p.Bytes), "\n")
		}
	}
	require.Equal(t, 400, out)
	require.Equal(t, 400, errOut)
	t.Logf("%d events, %d stdout lines, %d stderr lines", len(events), out, errOut)
}

// TestCancelSignalsTheContainer covers the cancel path for a command that
// installs a TERM handler: SIGTERM lands and the container exits 143 promptly.
func TestCancelSignalsTheContainer(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	go func() {
		time.Sleep(2 * time.Second)
		e.Cancel(taskID)
		e.Cancel(taskID) // idempotent
	}()

	start := time.Now()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", `trap 'exit 143' TERM; sleep 60 & wait`},
		},
	}, c.ch)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 143, res.ExitCode, "SIGTERM death is 128+15")
	require.Less(t, elapsed, 30*time.Second, "a handled SIGTERM must not wait out the grace period")
	t.Logf("handled SIGTERM: finished in %s with exit code %d", elapsed, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.Equal(t, ExitedPayload{ExitCode: 143}, events[len(events)-2].Payload)

	ctx := context.Background()
	require.NoError(t, e.Teardown(ctx, taskID, false))
	requireNoLeaks(t, e, taskID)
}

// TestCancelEscalatesToSIGKILL covers the acceptance case, `sleep 60`. With no
// runner in MVP-0 the task command is PID 1, and the kernel discards a
// default-disposition SIGTERM sent to a PID namespace's init, so the container
// only dies when the 30s grace expires and SIGKILL arrives — exit code 137, not
// 143. Step 04's runner is what turns this back into a graceful 143.
func TestCancelEscalatesToSIGKILL(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	go func() {
		time.Sleep(time.Second)
		e.Cancel(taskID)
	}()

	start := time.Now()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sleep", "60"},
		},
	}, c.ch)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 137, res.ExitCode, "SIGKILL death is 128+9")
	require.Less(t, elapsed, 35*time.Second, "the container must be gone within the grace period + slack")
	require.Greater(t, elapsed, cancelGrace, "SIGKILL must only follow the full grace period")
	t.Logf("unhandled SIGTERM: finished in %s with exit code %d", elapsed, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)

	ctx := context.Background()
	require.NoError(t, e.Teardown(ctx, taskID, false))
	requireNoLeaks(t, e, taskID)
}

func TestCancelForUnknownTaskIsANoop(t *testing.T) {
	e := newTestExecutor(t)
	e.Cancel(ids.NewTask())
}

func TestNonexistentImageFailsRetryablyAndLeaksNothing(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   "alpine:0.0.0-podium-does-not-exist",
			Command: []string{"true"},
		},
	}, c.ch)
	require.Error(t, err)
	t.Logf("run error: %v", err)

	events := c.finish()
	assertSeq(t, events)
	require.Equal(t, KindProvisioning, events[0].Kind)

	last := events[len(events)-1]
	require.Equal(t, KindError, last.Kind)
	payload, ok := last.Payload.(ErrorPayload)
	require.True(t, ok)
	require.True(t, payload.Retryable, "a pull failure is retryable")
	require.NotEmpty(t, payload.Message)

	requireNoLeaks(t, e, taskID)
}

func TestEmptyImageFailsWithoutRetry(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{TaskID: taskID, LeaseID: ids.NewLease()}, c.ch)
	require.Error(t, err)

	events := c.finish()
	assertSeq(t, events)
	payload := events[len(events)-1].Payload.(ErrorPayload)
	require.False(t, payload.Retryable, "a spec error is not retryable")

	requireNoLeaks(t, e, taskID)
}

func TestPullEmitsThrottledPullingEvents(t *testing.T) {
	const ref = "alpine:3.21"

	e := newTestExecutor(t)
	ctx := context.Background()
	// Make the pull real, and put the engine back the way we found it.
	_, _ = e.cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: true, PruneChildren: true})
	t.Cleanup(func() {
		_, _ = e.cli.ImageRemove(context.Background(), ref, image.RemoveOptions{Force: true, PruneChildren: true})
	})

	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: ref, Command: []string{"echo", "pulled"}},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)

	pulling := 0
	started := indexOfKind(events, KindStarted)
	for i, ev := range events {
		if ev.Kind != KindPulling {
			continue
		}
		pulling++
		require.Less(t, i, started, "pulling must precede started")
		require.Greater(t, i, 0, "pulling must follow provisioning")
		p, ok := ev.Payload.(PullingPayload)
		require.True(t, ok)
		require.Equal(t, ref, p.Image)
	}
	require.Positive(t, pulling, "a cold pull must emit at least one pulling event")
	t.Logf("%d pulling events for %s", pulling, ref)
}

func TestListOwnedAndTeardown(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()
	leaseID := ids.NewLease()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: leaseID,
		Spec:    spec.TaskSpec{Image: testImage, Command: []string{"true"}},
	}, c.ch)
	require.NoError(t, err)
	assertSeq(t, c.finish())

	owned, err := e.ListOwned(ctx)
	require.NoError(t, err)
	var found *OwnedContainer
	for i := range owned {
		if owned[i].TaskID == taskID {
			found = &owned[i]
		}
	}
	require.NotNil(t, found, "the exited container is still listed until teardown")
	require.Equal(t, leaseID, found.LeaseID)
	require.Equal(t, RoleTask, found.Role)
	require.Equal(t, containerName(taskID), found.Name)
	require.False(t, found.CreatedAt.IsZero())

	// keepWorkspace keeps the volume and nothing else.
	require.NoError(t, e.Teardown(ctx, taskID, true))

	owned, err = e.ListOwned(ctx)
	require.NoError(t, err)
	for _, oc := range owned {
		require.NotEqual(t, taskID, oc.TaskID)
	}
	_, err = e.cli.NetworkInspect(ctx, networkName(taskID), network.InspectOptions{})
	require.True(t, cerrdefs.IsNotFound(err))
	vol, err := e.cli.VolumeInspect(ctx, volumeName(taskID))
	require.NoError(t, err, "keepWorkspace must keep the workspace volume")
	require.Equal(t, volumeName(taskID), vol.Name)

	// A second teardown removes the volume and is otherwise a no-op.
	require.NoError(t, e.Teardown(ctx, taskID, false))
	requireNoLeaks(t, e, taskID)
	require.NoError(t, e.Teardown(ctx, taskID, false), "teardown is idempotent")
}
