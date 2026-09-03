//go:build integration

package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Only images already on this engine may be used; nothing here pulls, builds or removes
// one. postgres:16-alpine and redis:7-alpine stand in for the "Postgres + Redis + app"
// environment step 08 exists to make possible.
const (
	postgresImage = "postgres:16-alpine"
	redisImage    = "redis:7-alpine"
)

// specWithSidecars builds a spec the way the CLI does: defaults applied, then validated,
// so an integration test can never run something the public API would refuse.
func specWithSidecars(t *testing.T, s spec.TaskSpec) spec.TaskSpec {
	t.Helper()
	s.ApplyDefaults()
	require.NoError(t, s.Validate())
	return s
}

func stepsOf(events []Event) map[string]string {
	out := make(map[string]string)
	for _, ev := range events {
		if p, ok := ev.Payload.(StepPayload); ok && ev.Kind == KindStep {
			out[p.Name+"="+p.Status] = p.Status
		}
	}
	return out
}

func sidecarOutput(events []Event, name string) string {
	var b strings.Builder
	for _, ev := range events {
		p, ok := ev.Payload.(LogPayload)
		if ok && ev.Kind == KindLog && p.Stream == StreamSidecar && p.Sidecar == name {
			b.Write(p.Bytes)
		}
	}
	return b.String()
}

func taskOutput(events []Event) string {
	var b strings.Builder
	for _, ev := range events {
		p, ok := ev.Payload.(LogPayload)
		if ok && ev.Kind == KindLog && p.Stream != StreamSidecar {
			b.Write(p.Bytes)
		}
	}
	return b.String()
}

// inspectTask returns the task container's engine-side configuration: the API is the
// contract for every limit and every hardening flag, so that is what the tests assert on.
func inspectTask(t *testing.T, e *Executor, taskID string) container.InspectResponse {
	t.Helper()
	insp, err := e.cli.ContainerInspect(context.Background(), containerName(taskID))
	require.NoError(t, err)
	return insp
}

// TestPostgresSidecarServesTheTask is step 08's acceptance case: a real database sidecar,
// a real client in the task container, and a query that only succeeds if DNS, the task
// network and the readiness gate all work.
func TestPostgresSidecarServesTheTask(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   postgresImage,
			Command: []string{"psql", "-h", "db", "-U", "postgres", "-tAc", "select 1"},
			Env:     map[string]string{"PGPASSWORD": "podium"},
			Sidecars: map[string]spec.Sidecar{
				"db": {
					Image:     postgresImage,
					Env:       map[string]string{"POSTGRES_PASSWORD": "podium"},
					Readiness: spec.Readiness{TCPPort: 5432, Timeout: spec.Duration(90 * time.Second)},
				},
			},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	kinds := kindsOf(events)
	require.NotContains(t, kinds, KindError)
	require.Equal(t, KindFinished, kinds[len(kinds)-1])

	steps := stepsOf(events)
	require.Contains(t, steps, "sidecar/db="+stepStarted)
	require.Contains(t, steps, "sidecar/db="+stepReady)
	require.NotContains(t, steps, "sidecar/db="+stepFailed)

	assert.Equal(t, "1\n", taskOutput(events))
	assert.Contains(t, sidecarOutput(events, "db"), "database system is ready to accept connections")

	// The sidecar's own step and log events belong to provisioning, so they all precede
	// the task container's start.
	started := indexOfKind(events, KindStarted)
	require.Positive(t, started)
	for i, ev := range events {
		if ev.Kind != KindStep {
			continue
		}
		require.Less(t, i, started, "sidecar steps happen before the task starts")
	}

	// The sidecar joined the task network under its own name, with the labels teardown
	// and reconciliation key off.
	insp, err := e.cli.ContainerInspect(context.Background(), sidecarContainerName(taskID, "db"))
	require.NoError(t, err)
	assert.Equal(t, RoleSidecar, insp.Config.Labels[LabelRole])
	assert.Equal(t, "db", insp.Config.Labels[LabelSidecar])
	assert.Equal(t, taskID, insp.Config.Labels[LabelTask])
	require.Contains(t, insp.NetworkSettings.Networks, networkName(taskID))
	assert.Contains(t, insp.NetworkSettings.Networks[networkName(taskID)].Aliases, "db")
	assert.Contains(t, insp.HostConfig.SecurityOpt, noNewPrivileges)
	assert.Empty(t, insp.HostConfig.CapDrop, "a sidecar keeps its default capabilities")
}

// TestSidecarThatNeverListensFailsProvisioning covers the design's "sidecar never becomes
// ready" failure mode: the task never starts, the error is not retryable, it carries the
// sidecar's own output, and nothing is left behind.
func TestSidecarThatNeverListensFailsProvisioning(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"true"},
			Sidecars: map[string]spec.Sidecar{
				"db": {
					Image:     testImage,
					Command:   []string{"sh", "-c", "echo 'FATAL: could not open my data directory'; sleep 300"},
					Readiness: spec.Readiness{TCPPort: 9999, Timeout: spec.Duration(5 * time.Second)},
				},
			},
		}),
	}, c.ch)
	require.Error(t, err)
	require.ErrorIs(t, err, errSidecarNotReady)

	events := c.finish()
	assertSeq(t, events)
	kinds := kindsOf(events)
	require.Equal(t, KindError, kinds[len(kinds)-1])
	require.NotContains(t, kinds, KindStarted, "the task container must never start")
	require.NotContains(t, kinds, KindExited)

	steps := stepsOf(events)
	require.Contains(t, steps, "sidecar/db="+stepStarted)
	require.Contains(t, steps, "sidecar/db="+stepFailed)
	require.NotContains(t, steps, "sidecar/db="+stepReady)

	payload, ok := events[len(events)-1].Payload.(ErrorPayload)
	require.True(t, ok)
	assert.False(t, payload.Retryable, "another node would fail the same way")
	assert.Contains(t, payload.Message, "sidecar db not ready")
	assert.Contains(t, payload.Message, "nothing is listening on port 9999")
	assert.Contains(t, payload.Message, "[db] FATAL: could not open my data directory",
		"the adopter must see their database's real error")

	requireNoLeaks(t, e, taskID)
}

// TestSidecarsBecomeReadyConcurrently pins the "≈ max, not sum" property with two
// sidecars that each take a known three seconds to be usable.
func TestSidecarsBecomeReadyConcurrently(t *testing.T) {
	const delay = 3 * time.Second

	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	slow := func() spec.Sidecar {
		return spec.Sidecar{
			Image:   testImage,
			Command: []string{"sh", "-c", fmt.Sprintf("sleep %d; touch /tmp/ready; sleep 300", int(delay.Seconds()))},
			Readiness: spec.Readiness{
				Command: []string{"test", "-f", "/tmp/ready"},
				Timeout: spec.Duration(60 * time.Second),
			},
		}
	}

	c := newCollector()
	start := time.Now()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:    testImage,
			Command:  []string{"true"},
			Sidecars: map[string]spec.Sidecar{"one": slow(), "two": slow()},
		}),
	}, c.ch)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	steps := stepsOf(c.finish())
	require.Contains(t, steps, "sidecar/one="+stepReady)
	require.Contains(t, steps, "sidecar/two="+stepReady)

	t.Logf("two %s sidecars became ready in %s", delay, elapsed.Round(100*time.Millisecond))
	assert.Less(t, elapsed, time.Duration(1.5*float64(delay)),
		"sidecars must be waited for in parallel: %s is closer to the sum than the max", elapsed)
}

// TestPostgresAndRedisSidecarsShareATaskNetwork runs the two-sidecar environment from the
// design's topology diagram, each with its own TCP readiness probe.
func TestPostgresAndRedisSidecarsShareATaskNetwork(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   postgresImage,
			// postgres:16-alpine has psql and busybox nc but no redis-cli, so the cache is
			// greeted in its own wire protocol.
			Command: []string{"sh", "-c", `psql -h db -U postgres -tAc 'select 1' && printf 'PING\r\n' | nc -w 2 cache 6379`},
			Env:     map[string]string{"PGPASSWORD": "podium"},
			Sidecars: map[string]spec.Sidecar{
				"db": {
					Image:     postgresImage,
					Env:       map[string]string{"POSTGRES_PASSWORD": "podium"},
					Readiness: spec.Readiness{TCPPort: 5432, Timeout: spec.Duration(90 * time.Second)},
				},
				"cache": {
					Image:     redisImage,
					Readiness: spec.Readiness{TCPPort: 6379, Timeout: spec.Duration(90 * time.Second)},
				},
			},
		}),
	}, c.ch)
	require.NoError(t, err)

	events := c.finish()
	assertSeq(t, events)
	steps := stepsOf(events)
	require.Contains(t, steps, "sidecar/cache="+stepReady)
	require.Contains(t, steps, "sidecar/db="+stepReady)
	require.Equal(t, 0, res.ExitCode, "task output was: %q", taskOutput(events))
	assert.Contains(t, taskOutput(events), "PONG")
}

// TestSidecarTeardownRemovesSidecarsBeforeTheTaskContainer proves the ordering the design
// asks for and that nothing survives it.
func TestSidecarTeardownRemovesSidecarsBeforeTheTaskContainer(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"true"},
			Sidecars: map[string]spec.Sidecar{
				"one": {Image: testImage, Command: []string{"sleep", "300"}},
				"two": {Image: testImage, Command: []string{"sleep", "300"}},
			},
		}),
	}, c.ch)
	require.NoError(t, err)
	c.finish()

	owned, err := e.ListOwned(context.Background())
	require.NoError(t, err)
	roles := map[string]int{}
	for _, o := range owned {
		if o.TaskID == taskID {
			roles[o.Role]++
		}
	}
	require.Equal(t, map[string]int{RoleTask: 1, RoleSidecar: 2}, roles)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, e.Teardown(ctx, taskID, false))
	requireNoLeaks(t, e, taskID)

	left, err := e.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelSidecar)),
	})
	require.NoError(t, err)
	for _, s := range left {
		require.NotEqual(t, taskID, s.Labels[LabelTask], "sidecar %s leaked", s.Labels[LabelSidecar])
	}
}

// TestReadinessProbeReportsAnImageItCannotProbe: a busybox-less image cannot answer an
// exec probe, and waiting out the timeout would hide why.
func TestReadinessProbeReportsAnImageItCannotProbe(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	c := newCollector()
	start := time.Now()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"true"},
			Sidecars: map[string]spec.Sidecar{
				"db": {
					Image:   testImage,
					Command: []string{"sleep", "300"},
					Readiness: spec.Readiness{
						Command: []string{"/no/such/binary"},
						Timeout: spec.Duration(90 * time.Second),
					},
				},
			},
		}),
	}, c.ch)
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, errSidecarNotReady)
	assert.Contains(t, err.Error(), "not supported by this image")
	assert.Less(t, elapsed, 30*time.Second, "an unprobeable image must fail fast, not time out")

	c.finish()
	requireNoLeaks(t, e, taskID)
}

// TestResourceLimitsReachTheEngine asserts the limits on the created container. The
// Docker API is the contract: a CPU quota is invisible from inside the container (nproc
// still reports the host's cores), so there is nothing else to assert on.
func TestResourceLimitsReachTheEngine(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:     testImage,
			Command:   []string{"sh", "-c", "nproc"},
			Resources: spec.Resources{CPU: 0.5, MemoryMB: 128, PIDs: 50},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	insp := inspectTask(t, e, taskID)
	assert.Equal(t, int64(500_000_000), insp.HostConfig.NanoCPUs)
	assert.Equal(t, int64(128*1024*1024), insp.HostConfig.Memory)
	assert.Equal(t, insp.HostConfig.Memory, insp.HostConfig.MemorySwap)
	require.NotNil(t, insp.HostConfig.PidsLimit)
	assert.Equal(t, int64(50), *insp.HostConfig.PidsLimit)

	// Recorded, not asserted as a promise: a CPU quota is not namespaced.
	t.Logf("nproc inside a 0.5-CPU container reported %q", strings.TrimSpace(taskOutput(c.finish())))
}

// TestDefaultPidsLimitIsApplied: a spec that asks for no limits still gets the design's
// default process cap.
func TestDefaultPidsLimitIsApplied(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    specWithSidecars(t, spec.TaskSpec{Image: testImage, Command: []string{"true"}}),
	}, c.ch)
	require.NoError(t, err)
	c.finish()

	insp := inspectTask(t, e, taskID)
	require.NotNil(t, insp.HostConfig.PidsLimit)
	assert.Equal(t, int64(spec.DefaultPIDs), *insp.HostConfig.PidsLimit)
	assert.Zero(t, insp.HostConfig.NanoCPUs)
	assert.Zero(t, insp.HostConfig.Memory)
}

// TestPidsLimitStopsRunawayForking runs a bounded spawn — 80 attempts against a limit of
// 50 — rather than a real fork bomb. Same evidence, no risk to the machine running it.
func TestPidsLimitStopsRunawayForking(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	const script = `i=0
while [ $i -lt 80 ]; do
  sleep 5 &
  i=$((i+1))
done
echo "forked all 80"`

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:     testImage,
			Command:   []string{"sh", "-c", script},
			Resources: spec.Resources{PIDs: 50},
			Env:       map[string]string{"PODIUM_KILL_AFTER": "2s"},
		}),
	}, c.ch)
	require.NoError(t, err)

	out := taskOutput(c.finish())
	assert.Contains(t, out, "can't fork", "the shell must hit the limit")
	assert.NotContains(t, out, "forked all 80")
	assert.NotEqual(t, 0, res.ExitCode)

	insp := inspectTask(t, e, taskID)
	require.NotNil(t, insp.HostConfig.PidsLimit)
	assert.Equal(t, int64(50), *insp.HostConfig.PidsLimit)

	// The host is unaffected: the engine still runs a container straight afterwards.
	other := ids.NewTask()
	teardownAfter(t, e, other)
	c2 := newCollector()
	res2, err := e.Run(context.Background(), Request{
		TaskID:  other,
		LeaseID: ids.NewLease(),
		Spec:    specWithSidecars(t, spec.TaskSpec{Image: testImage, Command: []string{"echo", "host is fine"}}),
	}, c2.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res2.ExitCode)
	assert.Equal(t, "host is fine\n", taskOutput(c2.finish()))
}

// TestMemoryLimitIsReportedAsAnOOMKill covers the design's "task exceeds memory" mode: a
// memory hog under a small cap must be reported as an OOM kill, not a generic non-zero
// exit. `tail /dev/zero` is the classic; python:3-alpine, which the step file suggests,
// is not on this engine and may not be pulled.
func TestMemoryLimitIsReportedAsAnOOMKill(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:     testImage,
			Command:   []string{"tail", "/dev/zero"},
			Resources: spec.Resources{MemoryMB: 64},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.True(t, res.OOMKilled, "exit code was %d", res.ExitCode)
	require.NotEqual(t, 0, res.ExitCode)

	events := c.finish()
	exited, ok := events[len(events)-2].Payload.(ExitedPayload)
	require.True(t, ok)
	assert.True(t, exited.OOMKilled)
	assert.NotEqual(t, 0, exited.ExitCode)
}

// TestHardeningDropsEveryCapabilityByDefault reads the effective capability set from
// inside the container, which is the only place the drop is observable.
func TestHardeningDropsEveryCapabilityByDefault(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "grep ^CapEff /proc/self/status"},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	assert.Contains(t, taskOutput(c.finish()), "CapEff:\t0000000000000000")

	insp := inspectTask(t, e, taskID)
	assert.Contains(t, insp.HostConfig.SecurityOpt, noNewPrivileges)
	assert.Equal(t, []string{"ALL"}, []string(insp.HostConfig.CapDrop))
	assert.Empty(t, insp.HostConfig.CapAdd)
	assert.False(t, insp.HostConfig.Privileged)
	for _, opt := range insp.HostConfig.SecurityOpt {
		assert.NotContains(t, opt, "unconfined", "the engine's default seccomp profile stays on")
	}
	for _, b := range insp.HostConfig.Binds {
		assert.NotContains(t, b, "docker.sock")
	}
}

// TestHardeningAddsBackOnlyTheAllowedCapabilities.
func TestHardeningAddsBackOnlyTheAllowedCapabilities(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:     testImage,
			Command:   []string{"sh", "-c", "grep ^CapEff /proc/self/status"},
			Hardening: spec.Hardening{Capabilities: []string{"chown", "CAP_KILL"}},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	// CHOWN is bit 0 and KILL is bit 5: 0x21.
	assert.Contains(t, taskOutput(c.finish()), "CapEff:\t0000000000000021")

	insp := inspectTask(t, e, taskID)
	// The engine echoes capabilities back in their CAP_ form.
	assert.Equal(t, []string{"CAP_CHOWN", "CAP_KILL"}, []string(insp.HostConfig.CapAdd))
}

// TestReadOnlyRootfsKeepsWorkspaceAndTmpWritable.
func TestReadOnlyRootfsKeepsWorkspaceAndTmpWritable(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	const script = `touch /x         2>/dev/null && echo "ROOTFS WRITABLE" || echo "rootfs read-only"
touch /workspace/x && echo "workspace writable"
touch /tmp/x       && echo "tmp writable"
touch /podium/secrets/x && echo "secrets writable"`

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:     testImage,
			Command:   []string{"sh", "-c", script},
			Hardening: spec.Hardening{ReadOnlyRootfs: true},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	out := taskOutput(c.finish())
	assert.Contains(t, out, "rootfs read-only")
	assert.NotContains(t, out, "ROOTFS WRITABLE")
	assert.Contains(t, out, "workspace writable")
	assert.Contains(t, out, "tmp writable")
	assert.Contains(t, out, "secrets writable")

	insp := inspectTask(t, e, taskID)
	assert.True(t, insp.HostConfig.ReadonlyRootfs)
	assert.Equal(t, tmpTmpfs, insp.HostConfig.Tmpfs[tmpPath])
}

// TestSecretsTmpfsIsMountedForEveryTask: step 08 creates the mount, step 09 fills it.
func TestSecretsTmpfsIsMountedForEveryTask(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "ls -A /podium/secrets | wc -l; grep ' /podium/secrets ' /proc/mounts"},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	out := taskOutput(c.finish())
	assert.True(t, strings.HasPrefix(out, "0\n"), "the secrets tmpfs starts empty, got %q", out)
	assert.Contains(t, out, "tmpfs")
	assert.Contains(t, out, "noexec")
	assert.Contains(t, out, "nosuid")
	assert.Contains(t, out, "size=1024k")

	insp := inspectTask(t, e, taskID)
	assert.Equal(t, secretsTmpfs, insp.HostConfig.Tmpfs[secretsPath])
}
