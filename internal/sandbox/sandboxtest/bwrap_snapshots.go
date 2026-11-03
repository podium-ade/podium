package sandboxtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

// BwrapSnapshots is Bwrap with directory snapshots, the way Modal keeps a workspace: an image
// is a copy of the directory under Dir/images, and mounting one copies it back.
type BwrapSnapshots struct {
	*Bwrap

	next atomic.Uint64
	mu   sync.Mutex
	// TTLs is the lifetime each image was made with.
	TTLs map[string]time.Duration
}

// Capabilities implements sandbox.Provider.
func (b *BwrapSnapshots) Capabilities() sandbox.Capabilities {
	c := b.Bwrap.Capabilities()
	c.DirSnapshot = true
	return c
}

func (b *BwrapSnapshots) image(id string) string { return filepath.Join(b.Dir, "images", id) }

// SnapshotDir implements sandbox.Snapshotter.
func (b *BwrapSnapshots) SnapshotDir(_ context.Context, id, path string, ttl time.Duration) (string, error) {
	sb, err := b.get(id)
	if err != nil {
		return "", err
	}
	src, err := sb.hostPath(path)
	if err != nil {
		return "", err
	}
	img := fmt.Sprintf("im-%d", b.next.Add(1))
	if err := os.MkdirAll(filepath.Dir(b.image(img)), 0o755); err != nil {
		return "", err
	}
	if out, err := exec.Command("cp", "-a", src, b.image(img)).CombinedOutput(); err != nil {
		return "", fmt.Errorf("snapshot %s: %w: %s", path, err, out)
	}
	b.mu.Lock()
	if b.TTLs == nil {
		b.TTLs = map[string]time.Duration{}
	}
	b.TTLs[img] = ttl
	b.mu.Unlock()
	return img, nil
}

// MountSnapshot implements sandbox.Snapshotter.
func (b *BwrapSnapshots) MountSnapshot(_ context.Context, id, path, image string) error {
	sb, err := b.get(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(b.image(image)); err != nil {
		return &sandbox.Error{Op: "mount", Code: "not_found", Err: err}
	}
	dst, err := sb.hostPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("cp", "-a", b.image(image)+"/.", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s: %w: %s", image, err, out)
	}
	return nil
}

// DeleteSnapshot implements sandbox.Snapshotter.
func (b *BwrapSnapshots) DeleteSnapshot(_ context.Context, image string) error {
	return os.RemoveAll(b.image(image))
}

// Has reports whether the image exists.
func (b *BwrapSnapshots) Has(image string) bool {
	_, err := os.Stat(b.image(image))
	return err == nil
}

var _ sandbox.Snapshotter = (*BwrapSnapshots)(nil)
