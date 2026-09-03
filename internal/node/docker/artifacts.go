package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"
)

// AutoArtifactDir is collected from the task container when it exits: every regular file
// under it becomes an artifact named by its path relative to the directory. It is inside
// the workspace volume, so a sidecar or an earlier step can put things there too.
const AutoArtifactDir = "/workspace/.podium/artifacts"

// MaxArtifactBytes mirrors the control plane's own limit. The node checks it first so a
// 600 MB file is refused before it crosses the wire rather than after; the server's check
// is the authoritative one.
const MaxArtifactBytes int64 = 512 << 20

// artifactUploadTimeout bounds one artifact's copy-and-upload.
const artifactUploadTimeout = 10 * time.Minute

// ArtifactUpload is one file on its way from a container to the control plane.
type ArtifactUpload struct {
	TaskID      string
	LeaseID     string
	Name        string
	ContentType string
	Size        int64
	Body        io.Reader
}

// ArtifactStored is what the control plane says it kept.
type ArtifactStored struct {
	ID        string
	ObjectKey string
	Size      int64
}

// ArtifactUploader hands an artifact to the control plane. The node daemon implements it
// with NodeService.UploadArtifact; a nil one means this node cannot store artifacts, which
// a task must survive.
type ArtifactUploader interface {
	UploadArtifact(ctx context.Context, up ArtifactUpload) (ArtifactStored, error)
}

// ArtifactPayload reports one artifact the control plane has stored. The node turns it
// into a TaskEvent of kind artifact.
type ArtifactPayload struct {
	ArtifactID  string
	Name        string
	ObjectKey   string
	SizeBytes   int64
	ContentType string
}

// artifactCollector copies files out of one task's container and uploads them.
//
// Uploads run on their own goroutines so the runner's event socket is never blocked by
// one: the socket has a 2s write deadline and a failed write retires it for the rest of
// the run, so a synchronous 200 MB upload would cost the task every event after it. They
// are serialised against each other by a mutex — a task that drops ten screenshots at once
// should not open ten uploads — and the run waits for all of them before it emits exited,
// which is what keeps artifact events inside the documented ordering.
type artifactCollector struct {
	e   *Executor
	req Request
	em  *emitter
	cid string

	wg  sync.WaitGroup
	seq sync.Mutex
}

func (e *Executor) newArtifactCollector(req Request, em *emitter, cid string) *artifactCollector {
	return &artifactCollector{e: e, req: req, em: em, cid: cid}
}

// enabled reports whether anything can be stored at all.
func (c *artifactCollector) enabled() bool { return c != nil && c.req.Artifacts != nil }

// submit starts one artifact upload in the background.
func (c *artifactCollector) submit(ctx context.Context, name, containerPath, contentType string) {
	if !c.enabled() {
		c.fail(name, errors.New("this node has no artifact uploader; the control plane may have no object store configured"))
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.seq.Lock()
		defer c.seq.Unlock()
		if err := c.copyOne(ctx, name, containerPath, contentType); err != nil {
			c.fail(name, err)
		}
	}()
}

// wait blocks until every submitted upload has finished, or the budget runs out.
func (c *artifactCollector) wait(timeout time.Duration) {
	if c == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		c.e.log.Warn("artifact uploads did not finish in time", "task", c.req.TaskID, "timeout", timeout)
	}
}

// copyOne copies a single file out of the container and uploads it. Docker's copy API
// answers with a tar stream even for one file, so the header is where the size comes from
// — which matters, because the object store wants a length and nothing else knows it.
func (c *artifactCollector) copyOne(ctx context.Context, name, containerPath, contentType string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactUploadTimeout)
	defer cancel()

	rc, _, err := c.e.cli.CopyFromContainer(ctx, c.cid, containerPath)
	if err != nil {
		return fmt.Errorf("copy %s out of the container: %w", containerPath, err)
	}
	defer func() { _ = rc.Close() }()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%s holds no regular file", containerPath)
			}
			return fmt.Errorf("read %s: %w", containerPath, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		return c.upload(ctx, name, contentType, hdr.Size, tr)
	}
}

// collectDir uploads every regular file under dir, named by its path relative to dir. It
// runs after the container has exited and before exited is emitted, so it is synchronous:
// nothing else is competing for the container at that point.
//
// A missing directory is the ordinary case — most tasks never write one — and is not an
// error.
func (c *artifactCollector) collectDir(ctx context.Context, dir string) {
	if !c.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactUploadTimeout)
	defer cancel()

	rc, _, err := c.e.cli.CopyFromContainer(ctx, c.cid, dir)
	if err != nil {
		c.e.log.Debug("no artifact directory to collect", "task", c.req.TaskID, "dir", dir, "error", err)
		return
	}
	defer func() { _ = rc.Close() }()

	// Docker roots the tar at the directory's own basename, so "artifacts/shots/a.png"
	// has to lose its first element to become the artifact's name.
	root := path.Base(dir)
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.e.log.Warn("reading the artifact directory failed",
					"task", c.req.TaskID, "dir", dir, "error", err)
			}
			return
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := path.Clean(hdr.Name)
		name := strings.TrimPrefix(clean, root+"/")
		if name == "" || name == clean {
			// Not under the directory that was asked for. Docker does not produce this.
			continue
		}
		c.seq.Lock()
		err = c.upload(ctx, name, "", hdr.Size, tr)
		c.seq.Unlock()
		if err != nil {
			c.fail(name, err)
		}
	}
}

// upload hands one file's bytes to the control plane and emits the artifact event.
func (c *artifactCollector) upload(ctx context.Context, name, contentType string, size int64, body io.Reader) error {
	if size > MaxArtifactBytes {
		return fmt.Errorf("%d bytes is over the %d MB artifact limit", size, MaxArtifactBytes>>20)
	}
	stored, err := c.req.Artifacts.UploadArtifact(ctx, ArtifactUpload{
		TaskID:      c.req.TaskID,
		LeaseID:     c.req.LeaseID,
		Name:        name,
		ContentType: contentType,
		Size:        size,
		Body:        io.LimitReader(body, size),
	})
	if err != nil {
		return err
	}
	c.em.emit(KindArtifact, ArtifactPayload{
		ArtifactID:  stored.ID,
		Name:        name,
		ObjectKey:   stored.ObjectKey,
		SizeBytes:   stored.Size,
		ContentType: contentType,
	})
	return nil
}

// fail reports an artifact that could not be stored.
//
// It is always retryable, which here means "this did not break the task". A task does not
// need artifacts to run, and a non-retryable error event would move the task to failed —
// so a screenshot that was too big, or an object store that was down, would fail a run
// that otherwise did exactly what it was asked to. That trade is never worth making.
func (c *artifactCollector) fail(name string, err error) {
	c.e.log.Warn("storing an artifact failed", "task", c.req.TaskID, "name", name, "error", err)
	c.em.emit(KindError, ErrorPayload{
		Message:   fmt.Sprintf("artifact %q was not stored: %v", name, err),
		Retryable: true,
	})
}
