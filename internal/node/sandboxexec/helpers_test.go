//go:build boat || modal || sandbox

package sandboxexec

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/podium-ade/podium/internal/node/docker"
)

type collector struct {
	mu     sync.Mutex
	events []docker.Event
}

func (c *collector) run(ch <-chan docker.Event) {
	for ev := range ch {
		c.mu.Lock()
		c.events = append(c.events, ev)
		c.mu.Unlock()
	}
}

func (c *collector) logs(stream string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.events {
		if p, ok := ev.Payload.(docker.LogPayload); ok && p.Stream == stream {
			b.Write(p.Bytes)
		}
	}
	return b.String()
}

func (c *collector) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		k := ev.Kind
		if p, ok := ev.Payload.(docker.ErrorPayload); ok {
			k += "(" + p.Message + ")"
		}
		out = append(out, k)
	}
	return out
}

type memUploader struct {
	mu  sync.Mutex
	got map[string]string
}

func (u *memUploader) UploadArtifact(_ context.Context, up docker.ArtifactUpload) (docker.ArtifactStored, error) {
	b, err := io.ReadAll(up.Body)
	u.mu.Lock()
	u.got[up.Name] = string(b)
	u.mu.Unlock()
	return docker.ArtifactStored{ID: "a-" + up.Name, ObjectKey: "k/" + up.Name, Size: int64(len(b))}, err
}

// seedTar is a workspace tar holding seed.txt, laid out the way the executor's own tars are.
func seedTar(t *testing.T) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "workspace/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "workspace/seed.txt", Mode: 0o644, Size: 5})
	_, _ = tw.Write([]byte("seed\n"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}
