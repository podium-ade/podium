package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

func TestCheckEngine(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		info       system.Info
		wantErr    string
	}{
		{
			name:       "cgroup v2 on a current api",
			apiVersion: "1.54",
			info:       system.Info{CgroupVersion: "2", OSType: "linux", Architecture: "aarch64"},
		},
		{
			name:       "cgroup v2 on the oldest supported api",
			apiVersion: "1.43",
			info:       system.Info{CgroupVersion: "2", OSType: "linux"},
		},
		{
			name:       "cgroup v1 is rejected with an actionable message",
			apiVersion: "1.54",
			info:       system.Info{CgroupVersion: "1", OSType: "linux"},
			wantErr:    "cgroup v1 is not supported; podium requires cgroup v2",
		},
		{
			name:       "missing cgroup version is rejected",
			apiVersion: "1.54",
			info:       system.Info{OSType: "linux"},
			wantErr:    "did not report a cgroup version",
		},
		{
			name:       "unknown cgroup version is rejected",
			apiVersion: "1.54",
			info:       system.Info{CgroupVersion: "3", OSType: "linux"},
			wantErr:    `unsupported cgroup version "3"`,
		},
		{
			name:       "api older than 1.43 is rejected",
			apiVersion: "1.41",
			info:       system.Info{CgroupVersion: "2", OSType: "linux"},
			wantErr:    "API version 1.41 is too old; podium requires 1.43 or newer",
		},
		{
			name:    "unknown api version is rejected",
			info:    system.Info{CgroupVersion: "2", OSType: "linux"},
			wantErr: "could not determine API version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkEngine(tt.apiVersion, tt.info)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNewRequiresDataDir(t *testing.T) {
	_, err := New(context.Background(), Options{})
	require.ErrorContains(t, err, "DataDir is required")
}

func TestEmitterSeqIsOrderedUnderConcurrency(t *testing.T) {
	const emitters, perEmitter = 8, 200

	ch := make(chan Event, emitters*perEmitter)
	em := newEmitter(context.Background(), ch)

	var wg sync.WaitGroup
	for range emitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perEmitter {
				em.emit(KindLog, LogPayload{Stream: StreamStdout, Bytes: []byte("x")})
			}
		}()
	}
	wg.Wait()
	close(ch)

	var got []uint64
	for ev := range ch {
		got = append(got, ev.Seq)
	}
	require.Len(t, got, emitters*perEmitter)
	for i, seq := range got {
		require.Equal(t, uint64(i+1), seq, "seq must be delivered in order with no gaps")
	}
}

func TestResourceNames(t *testing.T) {
	const taskID = "task_01jabcdefghijklmnopqrstuv"
	require.Equal(t, "podium-"+taskID, networkName(taskID))
	require.Equal(t, "podium-ws-"+taskID, volumeName(taskID))
	require.Equal(t, "podium-"+taskID, containerName(taskID))
	require.Equal(t, map[string]string{
		"podium.task":  taskID,
		"podium.lease": "lease_01j",
		"podium.role":  "task",
	}, taskLabels(taskID, "lease_01j"))
}

func TestContainerEnvIsDeterministic(t *testing.T) {
	req := Request{TaskID: "task_1", LeaseID: "lease_1"}
	req.Spec.Env = map[string]string{"B": "2", "A": "1", "C": "3"}

	require.Equal(t, []string{
		"A=1", "B=2", "C=3",
		"PODIUM_TASK_ID=task_1",
		"PODIUM_LEASE_ID=lease_1",
		"PODIUM_WORKDIR=/workspace",
		"PODIUM_EVENTS_SOCK=/podium/events.sock",
	}, containerEnv(req, "/workspace"))
}

func TestRunnerArch(t *testing.T) {
	for _, engine := range []string{"x86_64", "amd64"} {
		got, err := runnerArch(engine)
		require.NoError(t, err)
		require.Equal(t, "amd64", got, "engine architecture %q", engine)
	}
	for _, engine := range []string{"aarch64", "arm64", "armv8l"} {
		got, err := runnerArch(engine)
		require.NoError(t, err)
		require.Equal(t, "arm64", got, "engine architecture %q", engine)
	}
	_, err := runnerArch("riscv64")
	require.ErrorContains(t, err, "riscv64")
	require.ErrorContains(t, err, "linux/amd64")
}

func TestEventSocketPathStaysInsideTheUnixBudget(t *testing.T) {
	const taskID = "task_01jabcdefghijklmnopqrstuv"

	// A sane data dir keeps the socket next to the task's other state.
	dir, err := socketDirFor("/var/lib/podium-node")
	require.NoError(t, err)
	require.Empty(t, dir)
	e := &Executor{dataDir: "/var/lib/podium-node"}
	require.Equal(t, "/var/lib/podium-node/tasks/"+taskID+"/events.sock", e.eventsSocketPath(taskID))
	require.LessOrEqual(t, len(e.eventsSocketPath(taskID)), maxUnixPath)

	// A data dir deep enough to blow the AF_UNIX budget moves the sockets aside.
	deep := "/Users/somebody/Library/Application Support/podium/nodes/worker-01/state"
	dir, err = socketDirFor(deep)
	require.NoError(t, err)
	require.NotEmpty(t, dir)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	e = &Executor{dataDir: deep, sockDir: dir}
	require.Equal(t, filepath.Join(dir, taskID+".sock"), e.eventsSocketPath(taskID))
	require.LessOrEqual(t, len(e.eventsSocketPath(taskID)), maxUnixPath)
}

func TestApplyResources(t *testing.T) {
	var hc container.HostConfig
	applyResources(&hc, spec.Resources{CPU: 0.5, MemoryMB: 64, PIDs: 50})

	assert.Equal(t, int64(500_000_000), hc.NanoCPUs)
	assert.Equal(t, int64(64*1024*1024), hc.Memory)
	assert.Equal(t, hc.Memory, hc.MemorySwap, "swap must be pinned to memory so a task never swaps")
	require.NotNil(t, hc.PidsLimit)
	assert.Equal(t, int64(50), *hc.PidsLimit)
}

func TestApplyResourcesLeavesUnsetLimitsAlone(t *testing.T) {
	var hc container.HostConfig
	applyResources(&hc, spec.Resources{})

	assert.Zero(t, hc.NanoCPUs)
	assert.Zero(t, hc.Memory)
	assert.Zero(t, hc.MemorySwap)
	assert.Nil(t, hc.PidsLimit)
}

func TestApplyTaskHardening(t *testing.T) {
	var hc container.HostConfig
	applyTaskHardening(&hc, spec.Hardening{Capabilities: []string{"chown", "CAP_NET_BIND_SERVICE"}})

	assert.Equal(t, []string{noNewPrivileges}, hc.SecurityOpt)
	assert.Equal(t, strslice.StrSlice{"ALL"}, hc.CapDrop)
	assert.Equal(t, strslice.StrSlice{"CHOWN", "NET_BIND_SERVICE"}, hc.CapAdd, "capabilities are normalised for the engine")
	assert.False(t, hc.ReadonlyRootfs)
	assert.Equal(t, secretsTmpfs, hc.Tmpfs[secretsPath])
	assert.NotContains(t, hc.Tmpfs, tmpPath, "a writable rootfs needs no tmpfs on /tmp")
	assert.NotContains(t, hc.SecurityOpt, "seccomp=unconfined")
}

func TestApplyTaskHardeningReadOnlyRootfs(t *testing.T) {
	var hc container.HostConfig
	applyTaskHardening(&hc, spec.Hardening{ReadOnlyRootfs: true})

	assert.True(t, hc.ReadonlyRootfs)
	assert.Equal(t, tmpTmpfs, hc.Tmpfs[tmpPath])
	assert.Equal(t, secretsTmpfs, hc.Tmpfs[secretsPath])
	assert.Empty(t, hc.CapAdd)
}

func TestApplySidecarHardeningOnlyDeniesNewPrivileges(t *testing.T) {
	var hc container.HostConfig
	applySidecarHardening(&hc)

	assert.Equal(t, []string{noNewPrivileges}, hc.SecurityOpt)
	assert.Empty(t, hc.CapDrop, "a stock database image usually needs its default capabilities")
	assert.False(t, hc.ReadonlyRootfs)
	assert.Empty(t, hc.Tmpfs)
}

func TestCanDialTaskNetworks(t *testing.T) {
	assert.True(t, canDialTaskNetworks("linux", "linux"))
	assert.False(t, canDialTaskNetworks("darwin", "linux"), "docker desktop's bridges are inside a VM")
	assert.False(t, canDialTaskNetworks("windows", "linux"))
	assert.False(t, canDialTaskNetworks("linux", "windows"))
}

func TestSidecarNamesAndLabels(t *testing.T) {
	assert.Equal(t, "podium-task_1-db", sidecarContainerName("task_1", "db"))
	assert.Equal(t, map[string]string{
		LabelTask:    "task_1",
		LabelLease:   "lease_1",
		LabelRole:    RoleSidecar,
		LabelSidecar: "db",
	}, sidecarLabels("task_1", "lease_1", "db"))
}

func TestSidecarEnvIsSorted(t *testing.T) {
	assert.Equal(t,
		[]string{"A=1", "B=2", "C=3"},
		sidecarEnv(map[string]string{"C": "3", "A": "1", "B": "2"}))
	assert.Empty(t, sidecarEnv(nil))
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'/healthz'`, shellQuote("/healthz"))
	assert.Equal(t, `'a'\''b'`, shellQuote("a'b"))
}

func TestTrimProbeOutput(t *testing.T) {
	assert.Equal(t, "(no output)", trimProbeOutput([]byte("  \n ")))
	assert.Equal(t, "boom", trimProbeOutput([]byte("boom\n")))
	assert.Len(t, trimProbeOutput(bytes.Repeat([]byte("x"), 500)), 200+len("…"))
}

// The registry's answer decides whether a pull is worth trying again. Every message here is
// one this engine really produced: a denial or a missing reference is the same answer every
// node would get and the same answer in an hour, while a registry that cannot be reached is
// exactly what a retry is for.
func TestPullFailuresAreClassifiedByWhatTheRegistrySaid(t *testing.T) {
	permanent := map[string]string{
		"the repository does not exist": "Error response from daemon: pull access denied for " +
			"does-not-exist, repository does not exist or may require 'docker login'",
		"the tag does not exist": `Error response from daemon: failed to resolve reference ` +
			`"docker.io/library/alpine:0.0.0-nope": docker.io/library/alpine:0.0.0-nope: not found`,
		"the manifest is unknown":     "manifest unknown: manifest unknown",
		"the registry wants a login":  "unauthorized: authentication required",
		"the reference is not a name": "invalid reference format",
	}
	for name, msg := range permanent {
		t.Run(name, func(t *testing.T) {
			err := pullFailed("img:tag", msg)
			require.ErrorIs(t, err, errImageUnavailable)
			require.Contains(t, err.Error(), msg, "the registry's own words are what an operator acts on")
		})
	}

	transient := map[string]string{
		"the registry refused the connection": `Error response from daemon: failed to resolve ` +
			`reference "127.0.0.1:1/nope:latest": failed to do request: Head ` +
			`"https://127.0.0.1:1/v2/nope/manifests/latest": dial tcp 127.0.0.1:1: connect: connection refused`,
		"the registry is overloaded": "toomanyrequests: You have reached your pull rate limit",
		"the registry is unwell":     "received unexpected HTTP status: 503 Service Unavailable",
		"the pull timed out":         "context deadline exceeded",
	}
	for name, msg := range transient {
		t.Run(name, func(t *testing.T) {
			err := pullFailed("img:tag", msg)
			require.NotErrorIs(t, err, errImageUnavailable,
				"failing a task for a registry blip is worse than spending one attempt on it")
			require.Contains(t, err.Error(), msg)
		})
	}
}
