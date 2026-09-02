package docker

import (
	"context"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/system"
	"github.com/stretchr/testify/require"
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
	}, containerEnv(req, "/workspace"))
}
