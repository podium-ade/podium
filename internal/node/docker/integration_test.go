//go:build integration

package docker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/pkg/spec"
)

const testImage = "alpine:3"

// testTmpdirEnv relocates the parent of shortTempDir. It exists for running this suite
// *inside* a task container against a docker-in-docker sidecar: everything below binds
// host paths into containers, and that daemon resolves a bind source in its own
// filesystem, where the test container's /tmp does not exist. Point this at a directory
// both of them share — a short one, and typically under /workspace.
//
// TMPDIR is deliberately not honoured: on macOS it is a ~90-character path, which is the
// very thing shortTempDir exists to stay clear of.
const testTmpdirEnv = "PODIUM_TEST_TMPDIR"

// shortTempDir is a data dir short enough that a task's event socket fits the AF_UNIX
// path budget, so the executor keeps its sockets in the task's own state directory. Plain
// t.TempDir() on macOS is already ~90 characters and forces the /tmp fallback instead;
// TestEventSocketFallsBackWhenTheDataDirIsDeep covers that path deliberately.
func shortTempDir(t *testing.T) string {
	t.Helper()
	parent := os.Getenv(testTmpdirEnv)
	if parent == "" {
		parent = "/tmp"
	}
	// A relocated parent is a path on a shared volume that nothing has created yet, which
	// is a confusing way for the whole suite to fail on its first line.
	require.NoError(t, os.MkdirAll(parent, 0o750))
	dir, err := os.MkdirTemp(parent, "pdmex")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	return newTestExecutorIn(t, shortTempDir(t))
}

func newTestExecutorIn(t *testing.T, dataDir string) *Executor {
	t.Helper()
	return newTestExecutorWithLog(t, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newTestExecutorWithLog(t *testing.T, dataDir string, logger *slog.Logger) *Executor {
	t.Helper()
	e, err := New(context.Background(), Options{DataDir: dataDir, Logger: logger})
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

// TestCancelOfABareSleepIsPrompt is the whole reason podium-runner exists. Before it, the
// task command was PID 1 itself, the kernel discarded the default-disposition SIGTERM sent
// to a PID namespace's init, and cancelling `sleep 60` cost the full 30s grace and ended in
// exit 137. With the runner as PID 1 the signal is caught and forwarded to the child's
// process group, so the same command dies of SIGTERM in about a second.
func TestCancelOfABareSleepIsPrompt(t *testing.T) {
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
	require.Equal(t, 143, res.ExitCode, "SIGTERM death is 128+15; 137 means the runner was not PID 1")
	require.Less(t, elapsed, 10*time.Second, "a forwarded SIGTERM must not wait out the 30s grace")
	t.Logf("bare `sleep 60` cancelled: finished in %s with exit code %d", elapsed, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.Equal(t, ExitedPayload{ExitCode: 143}, events[len(events)-2].Payload)

	ctx := context.Background()
	require.NoError(t, e.Teardown(ctx, taskID, false))
	requireNoLeaks(t, e, taskID)
}

// TestCancelEscalatesToSIGKILL covers the child that refuses to die. The runner forwards
// SIGTERM, waits PODIUM_KILL_AFTER, then SIGKILLs the whole process group — inside the
// node's own 30s grace, so the engine's SIGKILL never has to fire. The env override keeps
// the test to seconds instead of the 25s default.
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
			Env:     map[string]string{"PODIUM_KILL_AFTER": "1s"},
			Command: []string{"sh", "-c", `trap '' TERM; while :; do sleep 1; done`},
		},
	}, c.ch)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 137, res.ExitCode, "SIGKILL death is 128+9")
	require.Less(t, elapsed, cancelGrace, "the runner must kill the group before the engine's grace expires")
	t.Logf("ignored SIGTERM: finished in %s with exit code %d", elapsed, res.ExitCode)

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

// An image the registry will not serve is not a transient failure: every node would be told
// the same thing, and so would this one an hour later. It has to be reported as such, or the
// control plane spends the task's whole attempt budget re-asking a question with one answer.
func TestNonexistentImageFailsWithoutRetryAndLeaksNothing(t *testing.T) {
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
	require.False(t, payload.Retryable, "no node will ever be served an image that does not exist")
	require.True(t, payload.AbortsRun, "the run is over; the control plane must not wait for it")
	require.NotEmpty(t, payload.Message)

	requireNoLeaks(t, e, taskID)
}

// A registry that cannot be reached is the case retrying exists for, and it must survive the
// classification that fails a missing image fast.
func TestAnUnreachableRegistryStaysRetryable(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			// Port 1 answers nothing, so the pull fails on the transport rather than
			// on anything the registry said.
			Image:   "127.0.0.1:1/podium-does-not-exist:dev",
			Command: []string{"true"},
		},
	}, c.ch)
	require.Error(t, err)
	t.Logf("run error: %v", err)

	events := c.finish()
	assertSeq(t, events)
	payload, ok := events[len(events)-1].Payload.(ErrorPayload)
	require.True(t, ok)
	require.True(t, payload.Retryable, "a registry that is down is exactly what a retry is for")
	require.True(t, payload.AbortsRun)

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
	require.True(t, payload.AbortsRun)

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

// ---------------------------------------------------------------------------
// podium-runner as PID 1 (step 04)
// ---------------------------------------------------------------------------

// TestContainerShapePutsTheRunnerAtPID1 pins the container configuration the runner needs:
// the image's entrypoint cleared, the runner prepended to the command, the binary and the
// event socket bind-mounted, and PODIUM_EVENTS_SOCK pointing at the latter.
func TestContainerShapePutsTheRunnerAtPID1(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo $$ >&2; cat /proc/1/comm"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)

	var stdout, stderr strings.Builder
	for _, ev := range events {
		if ev.Kind != KindLog {
			continue
		}
		p := ev.Payload.(LogPayload)
		if p.Stream == StreamStdout {
			stdout.Write(p.Bytes)
		} else {
			stderr.Write(p.Bytes)
		}
	}
	// /proc/1/comm is the basename of what PID 1 was exec'd as: /podium/runner.
	require.Equal(t, "runner\n", stdout.String(), "PID 1 must be the runner, not the task command")
	require.NotEqual(t, "1\n", stderr.String(), "the task command must not be PID 1")

	insp, err := e.cli.ContainerInspect(ctx, containerName(taskID))
	require.NoError(t, err)
	require.Equal(t, strslice.StrSlice{"/podium/runner", "--", "sh", "-c", "echo $$ >&2; cat /proc/1/comm"}, insp.Config.Cmd)
	require.Empty(t, insp.Config.Entrypoint, "the image entrypoint must be cleared")
	require.Contains(t, insp.Config.Env, "PODIUM_EVENTS_SOCK=/podium/events.sock")
	require.Contains(t, insp.Config.Env, "PODIUM_TASK_ID="+taskID)

	targets := map[string]bool{}
	for _, m := range insp.HostConfig.Mounts {
		targets[m.Target] = m.ReadOnly
	}
	ro, ok := targets["/podium/runner"]
	require.True(t, ok, "the runner binary must be bind-mounted: %v", targets)
	require.True(t, ro, "the runner must be mounted read-only")
	require.Equal(t, []string{e.eventsSocketPath(taskID) + ":/podium/events.sock"}, insp.HostConfig.Binds)

	// A short data dir keeps the socket in the task's own state directory.
	require.Empty(t, e.sockDir)
	require.Equal(t, filepath.Join(e.TaskDir(taskID), "events.sock"), e.eventsSocketPath(taskID))
}

// TestRunnerReapsOrphanedGrandchildren is the other half of being PID 1: a process whose
// own parent exits is re-parented onto the runner and would stay a zombie without a
// wait loop.
func TestRunnerReapsOrphanedGrandchildren(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image: testImage,
			// The subshell exits immediately, orphaning its sleep onto PID 1.
			Command: []string{"sh", "-c", "( sleep 1 & ) ; sleep 3; ps -o pid,stat,args"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	var out strings.Builder
	for _, ev := range c.finish() {
		if ev.Kind == KindLog {
			out.Write(ev.Payload.(LogPayload).Bytes)
		}
	}
	t.Logf("ps inside the container:\n%s", out.String())
	require.NotContains(t, out.String(), "defunct", "a zombie survived: %s", out.String())
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		require.NotEqual(t, "Z", fields[1], "zombie process left behind: %q", line)
	}
}

// TestSpecWithoutACommandRunsTheImageCommand: the runner displaces the image's entrypoint,
// so a spec that names no command has to get the image's own back.
func TestSpecWithoutACommandRunsTheImageCommand(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: testImage},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	assertSeq(t, c.finish())

	insp, err := e.cli.ContainerInspect(ctx, containerName(taskID))
	require.NoError(t, err)
	require.Equal(t, strslice.StrSlice{"/podium/runner", "--", "/bin/sh"}, insp.Config.Cmd)
}

// TestEventSocketFallsBackWhenTheDataDirIsDeep covers the other socket location: a data dir
// too deep for the AF_UNIX path budget (t.TempDir() on macOS already is) pushes the socket
// into a private directory, and teardown still removes it.
func TestEventSocketFallsBackWhenTheDataDirIsDeep(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))

	e := newTestExecutorIn(t, deep)
	require.NotEmpty(t, e.sockDir, "a deep data dir must fall back to a private socket dir")
	require.True(t, strings.HasPrefix(e.sockDir, "/tmp/"), "socket dir = %s", e.sockDir)

	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)
	sock := e.eventsSocketPath(taskID)
	require.LessOrEqual(t, len(sock), maxUnixPath)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: testImage, Command: []string{"sh", "-c", "exit 5"}},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 5, res.ExitCode)
	assertSeq(t, c.finish())

	require.NoError(t, e.Teardown(context.Background(), taskID, false))
	_, err = os.Stat(sock)
	require.True(t, os.IsNotExist(err), "teardown must remove the event socket, stat err = %v", err)
}

// createOrphan builds and starts a task container the way a node that has since died would
// have left it: the runner at PID 1 and an event socket path nothing is listening on.
func createOrphan(t *testing.T, e *Executor, taskID, leaseID string, cmd []string) string {
	t.Helper()
	ctx := context.Background()
	labels := taskLabels(taskID, leaseID)

	sock := e.eventsSocketPath(taskID)
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close()) // the socket file survives; nothing accepts on it

	initFalse := false
	created, err := e.cli.ContainerCreate(ctx, &container.Config{
		Image:      testImage,
		Entrypoint: strslice.StrSlice{},
		Cmd:        append([]string{runnerTarget, "--"}, cmd...),
		WorkingDir: "/",
		Env: []string{
			"PODIUM_TASK_ID=" + taskID,
			"PODIUM_EVENTS_SOCK=" + eventsTarget,
			"PODIUM_WORKDIR=/",
		},
		Labels: labels,
	}, &container.HostConfig{
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: e.runnerPath, Target: runnerTarget, ReadOnly: true},
		},
		Binds:         []string{sock + ":" + eventsTarget},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		Init:          &initFalse,
	}, nil, nil, containerName(taskID))
	require.NoError(t, err)
	require.NoError(t, e.cli.ContainerStart(ctx, created.ID, container.StartOptions{}))
	return created.ID
}

// TestAdoptRunningContainerWhoseRunnerSocketIsGone is the node-restart case. The socket the
// runner connected to died with the process that listened on it, and no new one can ever be
// accepted, so adoption must fall back to Docker API events instead of waiting for a
// connection that will never come.
func TestAdoptRunningContainerWhoseRunnerSocketIsGone(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()
	leaseID := ids.NewLease()
	teardownAfter(t, e, taskID)

	createOrphan(t, e, taskID, leaseID, []string{"sh", "-c", "echo alive; sleep 3; exit 7"})

	c := newCollector()
	start := time.Now()
	res, err := e.Adopt(ctx, AdoptRequest{TaskID: taskID, LeaseID: leaseID, FromSeq: 10}, c.ch)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 7, res.ExitCode)
	require.Less(t, elapsed, 60*time.Second, "adoption must not wait on a socket nobody will connect to")
	t.Logf("adopted a container with no runner socket in %s, exit %d", elapsed, res.ExitCode)

	events := c.finish()
	require.NotEmpty(t, events)
	require.Equal(t, uint64(11), events[0].Seq, "an adopted run continues the previous seq space")
	kinds := kindsOf(events)
	require.Equal(t, KindFinished, kinds[len(kinds)-1])
	require.Equal(t, KindExited, kinds[len(kinds)-2])
	require.NotContains(t, kinds, KindStarted, "started belongs to the incarnation that started the container")

	var out strings.Builder
	for _, ev := range events {
		if ev.Kind == KindLog {
			out.Write(ev.Payload.(LogPayload).Bytes)
		}
	}
	require.Contains(t, out.String(), "alive")

	// The only step an adopted run emits is the node's own marker. Anything else would
	// mean a runner was reporting into a socket nobody owns.
	var steps []string
	for _, ev := range events {
		if ev.Kind == KindStep {
			steps = append(steps, ev.Payload.(StepPayload).Name)
		}
	}
	require.Equal(t, []string{StepReattached}, steps)
	// Whether the runner notices is engine-dependent: on a native Linux engine connect(2)
	// is refused, while Docker Desktop's socket forwarder accepts and then drops. Either
	// way the runner runs the command and the exit code above is the proof.
}

// TestAdoptAnExitedContainerWithoutASocket is the same hole on the other side of the exit:
// the container finished while no daemon was attached and the socket file is gone entirely.
func TestAdoptAnExitedContainerWithoutASocket(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: testImage, Command: []string{"sh", "-c", "echo done; exit 4"}},
	}, c.ch)
	require.NoError(t, err)
	assertSeq(t, c.finish())

	// The socket goes with the daemon that owned it.
	require.NoError(t, os.RemoveAll(e.eventsSocketPath(taskID)))

	c2 := newCollector()
	start := time.Now()
	res, err := e.Adopt(ctx, AdoptRequest{TaskID: taskID, LeaseID: ids.NewLease(), FromSeq: 100}, c2.ch)
	require.NoError(t, err)
	require.Equal(t, 4, res.ExitCode)
	require.Less(t, time.Since(start), 30*time.Second)

	kinds := kindsOf(c2.finish())
	require.Equal(t, KindFinished, kinds[len(kinds)-1])
	require.Equal(t, KindExited, kinds[len(kinds)-2])
}

// TestRunnerEventsReachTheExecutor proves the event socket really carries the protocol: the
// executor's started event is the runner's, reported after it forked the task command, not
// the Docker API's guess right after ContainerStart.
func TestRunnerEventsReachTheExecutor(t *testing.T) {
	var logs safeBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newTestExecutorWithLog(t, shortTempDir(t), logger)

	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: testImage, Command: []string{"sh", "-c", "echo hi"}},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	assertSeq(t, c.finish())

	line := logs.String()
	require.Contains(t, line, "runner started the task command", "no started event arrived over the socket:\n%s", line)
	require.NotContains(t, line, "falling back to the docker api")
	require.NotContains(t, line, "no runner connected")
	t.Logf("executor log:\n%s", line)
}

// safeBuffer is a slog sink usable from the executor's goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
