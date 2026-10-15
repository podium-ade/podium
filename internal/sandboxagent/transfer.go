package sandboxagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

// transfers are the run's callbacks into the node: each one is offered on the events
// stream and blocks the run until the node has moved the bytes and posted a result.
type transfers struct {
	dir  string
	next atomic.Uint64

	mu      sync.Mutex
	pending map[string]*pendingTransfer
	changed chan struct{}
}

type pendingTransfer struct {
	wire.Transfer
	path   string
	result chan wire.TransferResult
}

func newTransfers(dir string) *transfers {
	return &transfers{dir: dir, pending: map[string]*pendingTransfer{}, changed: make(chan struct{})}
}

// offer registers a transfer and waits for the node's answer.
func (t *transfers) offer(ctx context.Context, tr wire.Transfer, path string) (wire.TransferResult, error) {
	tr.ID = strconv.FormatUint(t.next.Add(1), 10)
	p := &pendingTransfer{Transfer: tr, path: path, result: make(chan wire.TransferResult, 1)}
	t.mu.Lock()
	t.pending[tr.ID] = p
	close(t.changed)
	t.changed = make(chan struct{})
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, tr.ID)
		t.mu.Unlock()
	}()
	select {
	case res := <-p.result:
		if res.Error != "" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-ctx.Done():
		return wire.TransferResult{}, ctx.Err()
	}
}

// snapshot is the transfers waiting now, and a channel closed when that changes.
func (t *transfers) snapshot() ([]wire.Transfer, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]wire.Transfer, 0, len(t.pending))
	for _, p := range t.pending {
		out = append(out, p.Transfer)
	}
	return out, t.changed
}

func (t *transfers) get(id string) (*pendingTransfer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pending[id]
	return p, ok
}

func (t *transfers) resolve(id string, res wire.TransferResult) bool {
	p, ok := t.get(id)
	if !ok {
		return false
	}
	select {
	case p.result <- res:
	default:
	}
	return true
}

// UploadArtifact implements docker.ArtifactUploader by staging the file for the node.
func (t *transfers) UploadArtifact(ctx context.Context, up docker.ArtifactUpload) (docker.ArtifactStored, error) {
	f, err := os.CreateTemp(t.dir, "artifact-*")
	if err != nil {
		return docker.ArtifactStored{}, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	n, err := io.Copy(f, up.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return docker.ArtifactStored{}, fmt.Errorf("stage artifact: %w", err)
	}
	res, err := t.offer(ctx, wire.Transfer{Kind: wire.TransferArtifact, Name: up.Name, ContentType: up.ContentType, Size: n}, f.Name())
	return res.Stored, err
}

// workspace bridges docker.Workspace to the node.
func (t *transfers) workspace() *docker.Workspace {
	return &docker.Workspace{
		Restore: func(ctx context.Context) (io.ReadCloser, bool, error) {
			path := filepath.Join(t.dir, "workspace-restore.tar")
			res, err := t.offer(ctx, wire.Transfer{Kind: wire.TransferWorkspaceRestore}, path)
			if err != nil || res.NotFound {
				return nil, false, err
			}
			f, err := os.Open(path)
			if err != nil {
				return nil, false, err
			}
			return removeOnClose{f}, true, nil
		},
		Save: func(ctx context.Context, tarPath string, size int64) error {
			_, err := t.offer(ctx, wire.Transfer{Kind: wire.TransferWorkspaceSave, Size: size}, tarPath)
			return err
		},
	}
}

type removeOnClose struct{ *os.File }

func (r removeOnClose) Close() error {
	err := r.File.Close()
	_ = os.Remove(r.Name())
	return err
}
