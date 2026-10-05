//go:build integration

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestWorkspaceRoundTrip writes a file, tars the named volume, and imports that tar into
// a second task's volume. docker commit would miss this volume; the copy is the snapshot.
func TestWorkspaceRoundTrip(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()

	var saved []byte
	first := ids.NewTask()
	teardownAfter(t, e, first)
	c := newCollector()
	res, err := e.Run(ctx, Request{
		TaskID:  first,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo -n one > /workspace/note.txt; echo -n secret > /podium/secrets/token"},
		},
		Workspace: &Workspace{
			Save: func(_ context.Context, tarPath string, size int64) error {
				b, rerr := os.ReadFile(tarPath)
				if rerr != nil {
					return rerr
				}
				saved = b
				require.Equal(t, int64(len(b)), size)
				return nil
			},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.NotEmpty(t, saved)
	names := tarNames(t, saved)
	require.Contains(t, names, "workspace/note.txt")
	for _, name := range names {
		require.NotContains(t, name, "secrets")
		require.NotContains(t, name, "token")
	}

	_, err = e.cli.ContainerInspect(ctx, "podium-wscopy-"+first)
	require.True(t, cerrdefs.IsNotFound(err), "the copy helper must be gone before the run returns")

	second := ids.NewTask()
	teardownAfter(t, e, second)
	c2 := newCollector()
	res, err = e.Run(ctx, Request{
		TaskID:  second,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"cat", "/workspace/note.txt"},
		},
		Workspace: &Workspace{
			Restore: func(context.Context) (io.ReadCloser, bool, error) {
				return io.NopCloser(bytes.NewReader(saved)), true, nil
			},
		},
	}, c2.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, "one", stdoutOf(c2.finish()))
}

// TestEphemeralRunRemovesTheWorkspaceVolume is the one-shot path: no workspace hooks,
// and teardown with keepWorkspace false removes the volume.
func TestEphemeralRunRemovesTheWorkspaceVolume(t *testing.T) {
	e := newTestExecutor(t)
	ctx := context.Background()
	taskID := ids.NewTask()

	c := newCollector()
	_, err := e.Run(ctx, Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec:    spec.TaskSpec{Image: testImage, Command: []string{"true"}},
	}, c.ch)
	require.NoError(t, err)
	_, err = e.cli.VolumeInspect(ctx, volumeName(taskID))
	require.NoError(t, err, "the volume exists until teardown")

	require.NoError(t, e.Teardown(ctx, taskID, false))
	_, err = e.cli.VolumeInspect(ctx, volumeName(taskID))
	require.True(t, cerrdefs.IsNotFound(err))
}

func stdoutOf(events []Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind != KindLog {
			continue
		}
		p, ok := ev.Payload.(LogPayload)
		if ok && p.Stream == StreamStdout {
			b.Write(p.Bytes)
		}
	}
	return b.String()
}

func tarNames(t *testing.T, raw []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		require.NoError(t, err)
		names = append(names, hdr.Name)
	}
}
