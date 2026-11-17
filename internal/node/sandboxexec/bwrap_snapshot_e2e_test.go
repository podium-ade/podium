package sandboxexec

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
	"github.com/podium-ade/podium/pkg/spec"
)

// controlPlane is the node's view of the session's image record, in memory.
type controlPlane struct {
	mu       sync.Mutex
	image    string
	puts     []string
	restores int
	bases    []string
	ttl      time.Duration
}

func (c *controlPlane) hooks(publish bool) *docker.Workspace {
	img := &docker.WorkspaceImage{
		Provider: "bwrap",
		Get: func(context.Context) (string, time.Duration, bool, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.image, c.ttl, c.image != "", nil
		},
		Put: func(_ context.Context, image string) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.image = image
			c.puts = append(c.puts, image)
			return nil
		},
	}
	if publish {
		img.Base = func(_ context.Context, tarPath string, _ int64) error {
			names, err := tarNames(tarPath)
			c.mu.Lock()
			c.bases = append(c.bases, strings.Join(names, ","))
			c.mu.Unlock()
			return err
		}
	}
	return &docker.Workspace{
		Restore: func(context.Context) (io.ReadCloser, bool, error) {
			c.mu.Lock()
			c.restores++
			c.mu.Unlock()
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{Name: "workspace/", Typeflag: tar.TypeDir, Mode: 0o755})
			_ = tw.WriteHeader(&tar.Header{Name: "workspace/seed.txt", Mode: 0o644, Size: 5})
			_, _ = tw.Write([]byte("seed\n"))
			_ = tw.Close()
			return io.NopCloser(&buf), true, nil
		},
		// What a node without images would store: never called when images are kept.
		Save: func(context.Context, string, int64) error {
			panic("a session kept as an image saved a tar")
		},
		Image: img,
	}
}

func tarNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, h.Name)
	}
}

// TestASessionKeptAsAProviderSnapshotEndToEnd is a conversation's workspace on a provider that
// keeps directories as images of its own, through the real agent: the first task's workspace is
// kept as an image rather than a tar, the next task finds its files there without a restore,
// and a session whose image the provider has dropped starts over from the tar path and stops
// pointing at the image. Same environment as TestASandboxNodeRunsATaskFromItsPoolEndToEnd.
func TestASessionKeptAsAProviderSnapshotEndToEnd(t *testing.T) {
	bin, agent := os.Getenv("PODIUM_TEST_BWRAP"), os.Getenv("PODIUM_TEST_SANDBOX_AGENT")
	if bin == "" || agent == "" {
		t.Skip("set PODIUM_TEST_BWRAP and PODIUM_TEST_SANDBOX_AGENT to run the sandbox path end to end")
	}
	p := &sandboxtest.BwrapSnapshots{Bwrap: &sandboxtest.Bwrap{Dir: t.TempDir(), Bin: bin}}
	e, err := New(Options{
		Provider: p, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent: func() ([]byte, error) { return os.ReadFile(agent) },
	})
	require.NoError(t, err)
	provider, ok := e.KeepsWorkspaceImages()
	require.True(t, ok)
	assert.Equal(t, "bwrap", provider)

	cp := &controlPlane{ttl: 48 * time.Hour}
	run := func(id, script string, publish bool) string {
		sp := spec.TaskSpec{Image: "alpine:3", Command: []string{"sh", "-c", script}}
		sp.ApplyDefaults()
		ch := make(chan docker.Event, 256)
		var stdout strings.Builder
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range ch {
				if pl, ok := ev.Payload.(docker.LogPayload); ok && pl.Stream == docker.StreamStdout {
					stdout.Write(pl.Bytes)
				}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := e.Run(ctx, docker.Request{TaskID: id, LeaseID: "lease-" + id, Spec: sp, Workspace: cp.hooks(publish)}, ch)
		close(ch)
		<-done
		require.NoError(t, err, "stdout so far: %s", stdout.String())
		require.Equal(t, 0, res.ExitCode, "stdout: %s", stdout.String())
		require.NoError(t, e.Teardown(context.Background(), id, false))
		return stdout.String()
	}

	out := run("t1", `cat /workspace/seed.txt; echo first > /workspace/note.txt`, false)
	assert.Contains(t, out, "seed", "a session with no image yet is restored the tar way")
	require.Len(t, cp.puts, 1, "the workspace was kept as an image")
	first := cp.puts[0]
	assert.True(t, p.Has(first))
	assert.Equal(t, 48*time.Hour, p.TTLs[first], "with the lifetime the control plane gave")
	assert.Equal(t, 1, cp.restores)

	out = run("t2", `cat /workspace/note.txt; echo second >> /workspace/note.txt`, true)
	assert.Contains(t, out, "first", "the next task found the files the image kept")
	assert.Equal(t, 1, cp.restores, "and restored no tar over them")
	require.Len(t, cp.puts, 2)
	assert.NotEqual(t, first, cp.puts[1], "it kept a new image")
	require.Len(t, cp.bases, 1, "a task that publishes a base still stores it as a tar")
	assert.Contains(t, cp.bases[0], "workspace/note.txt")

	require.NoError(t, p.DeleteSnapshot(context.Background(), cp.puts[1]))
	out = run("t3", `cat /workspace/seed.txt; test -e /workspace/note.txt && echo stale || echo fresh`, false)
	assert.Contains(t, out, "seed")
	assert.Contains(t, out, "fresh", "an image the provider dropped is not mounted")
	require.Len(t, cp.puts, 4)
	assert.Equal(t, "", cp.puts[2], "the session stopped pointing at the image that is gone")
	assert.NotEmpty(t, cp.puts[3], "and kept a new one")
	assert.Equal(t, 2, cp.restores)
}
