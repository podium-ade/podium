//go:build integration

package docker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// dindImage carries both halves of what these tests need: the daemon and the CLI that
// talks to it, so one image is the sidecar AND the task. It must already be on this
// engine, like every other image the suite uses.
const dindImage = "docker:28-dind"

// dindGraphPath is the VOLUME docker:dind declares. Every dind container therefore gets an
// anonymous volume, and it is where the nested daemon writes every layer it pulls —
// gigabytes for a real dev stack, per task.
const dindGraphPath = "/var/lib/docker"

// dindReady is the probe for a nested daemon. Two minutes rather than the 60s default:
// dockerd takes ~17s to listen on an unloaded arm64 Docker Desktop, and a busy CI box is
// slower than that by more than the margin.
var dindReady = spec.Readiness{TCPPort: 2375, Timeout: spec.Duration(2 * time.Minute)}

// dindSidecar is the sidecar half of docker-in-docker: privileged so the daemon can make
// its own cgroups and mount its own overlay, sharing the workspace so a bind source under
// /workspace resolves inside it, and with TLS off so the task reaches it on plain 2375
// over the task's own private bridge.
func dindSidecar() spec.Sidecar {
	return spec.Sidecar{
		Image:          dindImage,
		Env:            map[string]string{"DOCKER_TLS_CERTDIR": ""},
		Readiness:      dindReady,
		Privileged:     true,
		ShareWorkspace: true,
	}
}

// newPrivilegedTestExecutor is a node whose operator turned --allow-privileged-sidecars
// on. Every other executor in this suite leaves it off, which is the default and the
// refusal TestPrivilegedSidecarIsRefusedWithoutTheNodeFlag asserts.
func newPrivilegedTestExecutor(t *testing.T) *Executor {
	t.Helper()
	e, err := New(context.Background(), Options{
		DataDir:                 shortTempDir(t),
		AllowPrivilegedSidecars: true,
		Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	return e
}

// anonymousVolumeAt returns the name of the anonymous volume a container has at dest, or
// "" if it has none there.
func anonymousVolumeAt(t *testing.T, e *Executor, containerName, dest string) string {
	t.Helper()
	insp, err := e.cli.ContainerInspect(context.Background(), containerName)
	require.NoError(t, err)
	for _, m := range insp.Mounts {
		if m.Destination == dest {
			return m.Name
		}
	}
	return ""
}

// TestDindSidecarRunsNestedContainersAgainstTheSharedWorkspace is what the whole feature
// exists for: a task that runs `docker run` against a daemon beside it, with a bind source
// under /workspace. It fails without `privileged` (the daemon cannot start), and it fails
// without `share_workspace` (the daemon resolves /workspace in its own filesystem and
// finds nothing), so one assertion covers both fields.
//
// The nested `docker run` pulls alpine:3 from the internet, unlike everything else in this
// suite: the daemon under test is brand new and its image store is empty by construction.
func TestDindSidecarRunsNestedContainersAgainstTheSharedWorkspace(t *testing.T) {
	e := newPrivilegedTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image: dindImage,
			Command: []string{"sh", "-c",
				"echo written-by-the-task > /workspace/ping.txt && " +
					"docker -H tcp://dind:2375 run --rm -v /workspace:/ws " + testImage + " cat /ws/ping.txt"},
			Sidecars: map[string]spec.Sidecar{"dind": dindSidecar()},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.NotContains(t, kindsOf(events), KindError)
	steps := stepsOf(events)
	require.Contains(t, steps, "sidecar/dind="+stepReady)
	assert.Contains(t, taskOutput(events), "written-by-the-task",
		"the nested daemon read the task's own workspace")

	insp, err := e.cli.ContainerInspect(context.Background(), sidecarContainerName(taskID, "dind"))
	require.NoError(t, err)
	assert.True(t, insp.HostConfig.Privileged)
	assert.Contains(t, insp.HostConfig.SecurityOpt, noNewPrivileges,
		"privilege does not buy a sidecar the right to escalate further")
	assert.Contains(t, insp.HostConfig.Mounts, mount.Mount{
		Type:   mount.TypeVolume,
		Source: volumeName(taskID),
		Target: workspacePath,
	}, "the sidecar sees the task's workspace volume at the task's own path")
}

// TestDindGraphVolumeIsRemovedWithTheTask is the leak this step closes. docker:dind
// declares VOLUME /var/lib/docker, so the engine makes an anonymous volume for every dind
// sidecar; teardown used to remove the container without it and leave the daemon's whole
// image store on the node for good.
//
// No readiness probe here on purpose: the volume exists the moment the container is
// created, so nothing is gained by waiting two minutes for dockerd to listen.
func TestDindGraphVolumeIsRemovedWithTheTask(t *testing.T) {
	e := newPrivilegedTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	sc := dindSidecar()
	sc.Readiness = spec.Readiness{}

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:    testImage,
			Command:  []string{"true"},
			Sidecars: map[string]spec.Sidecar{"dind": sc},
		}),
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	c.finish()

	graph := anonymousVolumeAt(t, e, sidecarContainerName(taskID, "dind"), dindGraphPath)
	require.NotEmpty(t, graph, "docker:dind is expected to declare VOLUME "+dindGraphPath)
	_, err = e.cli.VolumeInspect(context.Background(), graph)
	require.NoError(t, err, "the graph volume should exist while the sidecar does")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, e.Teardown(ctx, taskID, false))

	_, err = e.cli.VolumeInspect(ctx, graph)
	assert.True(t, cerrdefs.IsNotFound(err), "the dind graph volume leaked (err=%v)", err)
	requireNoLeaks(t, e, taskID)
}

// TestPrivilegedSidecarIsRefusedWithoutTheNodeFlag: the gate is the node's, not the
// spec's. The task fails at provisioning, before the sidecar is fetched or created, with a
// message that names the sidecar and the flag — and it is not retryable, because Podium
// places on labels alone and a requeue would draw from the same pool and print this three
// times.
func TestPrivilegedSidecarIsRefusedWithoutTheNodeFlag(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:    testImage,
			Command:  []string{"true"},
			Sidecars: map[string]spec.Sidecar{"dind": dindSidecar()},
		}),
	}, c.ch)
	require.Error(t, err)
	require.ErrorIs(t, err, errPrivilegedNotAllowed)

	events := c.finish()
	assertSeq(t, events)
	kinds := kindsOf(events)
	require.Equal(t, KindError, kinds[len(kinds)-1])
	require.NotContains(t, kinds, KindStep, "the sidecar is never created, so it reports no step")
	require.NotContains(t, kinds, KindStarted, "the task container must never start")
	require.NotContains(t, kinds, KindExited)

	payload, ok := events[len(events)-1].Payload.(ErrorPayload)
	require.True(t, ok)
	assert.False(t, payload.Retryable)
	assert.Contains(t, payload.Message, "sidecar dind")
	assert.Contains(t, payload.Message, "--allow-privileged-sidecars")

	requireNoLeaks(t, e, taskID)
}
