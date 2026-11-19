package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"log/slog"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// countingUploader counts each name it was handed.
type countingUploader struct {
	mu  sync.Mutex
	got map[string]int
}

func (u *countingUploader) UploadArtifact(_ context.Context, up ArtifactUpload) (ArtifactStored, error) {
	if _, err := io.Copy(io.Discard, up.Body); err != nil {
		return ArtifactStored{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.got[up.Name]++
	return ArtifactStored{ID: "art_" + up.Name, Size: up.Size}, nil
}

type fakeFile struct {
	body    string
	modTime time.Time
}

// copyTar answers as the engine's copy API does: a tar rooted at the path's base name.
func copyTar(t *testing.T, root string, files map[string]fakeFile) io.ReadCloser {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, f := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: path.Join(root, name), Typeflag: tar.TypeReg, Mode: 0o644,
			Size: int64(len(f.body)), ModTime: f.modTime,
		}))
		_, err := tw.Write([]byte(f.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return io.NopCloser(&buf)
}

// TestCollectingOnExitSkipsAFileAlreadyStoredUnchanged: a warm turn stores the screenshots
// its answer names with `artifact add` when it answers. The collection when it exits must not
// store them a second time — the same name twice is two attachments — but must store one the
// task changed after, and everything it never stored.
func TestCollectingOnExitSkipsAFileAlreadyStoredUnchanged(t *testing.T) {
	then := time.Date(2026, 11, 19, 3, 27, 0, 0, time.UTC)
	files := map[string]fakeFile{
		"shot.png":    {body: "PNG", modTime: then},
		"changed.png": {body: "PNG2", modTime: then.Add(time.Minute)},
		"other.txt":   {body: "report", modTime: then},
	}
	up := &countingUploader{got: map[string]int{}}
	col := (&Executor{log: slog.New(slog.DiscardHandler)}).newArtifactCollector(
		Request{TaskID: "task_1", LeaseID: "lease_1", Artifacts: up},
		newEmitter(context.Background(), make(chan Event, 64)), "")
	col.copyFrom = func(_ context.Context, p string) (io.ReadCloser, error) {
		if p == AutoArtifactDir {
			return copyTar(t, path.Base(p), files), nil
		}
		name := path.Base(p)
		f := files[name]
		if name == "changed.png" {
			// What it looked like when it was stored: since then it was written again.
			f = fakeFile{body: "PNG", modTime: then}
		}
		return copyTar(t, "", map[string]fakeFile{name: f}), nil
	}

	col.submit(context.Background(), "shot.png", AutoArtifactDir+"/shot.png", "")
	col.submit(context.Background(), "changed.png", AutoArtifactDir+"/changed.png", "")
	col.wait(time.Second)
	col.collectDir(context.Background(), AutoArtifactDir)

	require.Equal(t, map[string]int{"shot.png": 1, "changed.png": 2, "other.txt": 1}, up.got)
}
