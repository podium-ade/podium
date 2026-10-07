//go:build integration

package docker

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestTaskRuntimeStaysOffTheSidecar pins the split: the operator's runtime is the task
// container's, and a sidecar keeps the engine default. runc is named explicitly because
// this machine has it; runsc is covered when the engine actually has it.
func TestTaskRuntimeStaysOffTheSidecar(t *testing.T) {
	for _, runtime := range []string{"runc", "runsc"} {
		t.Run(runtime, func(t *testing.T) {
			if !engineHasRuntime(t, runtime) {
				t.Skipf("this engine has no %s runtime", runtime)
			}
			e := newRuntimeExecutor(t, runtime)
			ctx := context.Background()
			taskID := ids.NewTask()
			teardownAfter(t, e, taskID)

			c := newCollector()
			res, err := e.Run(ctx, Request{
				TaskID:  taskID,
				LeaseID: ids.NewLease(),
				Spec: spec.TaskSpec{
					Image:   testImage,
					Command: []string{"true"},
					Sidecars: map[string]spec.Sidecar{
						"nap": {Image: testImage, Command: []string{"sleep", "30"}},
					},
				},
			}, c.ch)
			require.NoError(t, err)
			require.Equal(t, 0, res.ExitCode)

			task, err := e.cli.ContainerInspect(ctx, containerName(taskID))
			require.NoError(t, err)
			require.Equal(t, runtime, task.HostConfig.Runtime)

			// Docker fills an unset HostConfig.Runtime with the engine default on
			// inspect, so a sidecar we never assigned still reports "runc". The
			// split shows up as the sidecar staying on that default while the
			// task uses the runtime the operator named.
			side, err := e.cli.ContainerInspect(ctx, sidecarContainerName(taskID, "nap"))
			require.NoError(t, err)
			require.Equal(t, engineDefaultRuntime(t), side.HostConfig.Runtime)
		})
	}
}

func newRuntimeExecutor(t *testing.T, runtime string) *Executor {
	t.Helper()
	e, err := New(context.Background(), Options{
		DataDir: shortTempDir(t),
		Runtime: runtime,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	return e
}

func engineInfo(t *testing.T) (defaultRuntime string, runtimes map[string]struct{}) {
	t.Helper()
	e := newTestExecutor(t)
	info, err := e.cli.Info(context.Background())
	require.NoError(t, err)
	names := make(map[string]struct{}, len(info.Runtimes))
	for name := range info.Runtimes {
		names[name] = struct{}{}
	}
	return info.DefaultRuntime, names
}

func engineHasRuntime(t *testing.T, name string) bool {
	t.Helper()
	_, runtimes := engineInfo(t)
	_, ok := runtimes[name]
	return ok
}

func engineDefaultRuntime(t *testing.T) string {
	t.Helper()
	name, _ := engineInfo(t)
	require.NotEmpty(t, name)
	return name
}
