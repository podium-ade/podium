// Package snapshots stores one workspace tar per conversation, and one base tar per
// repository, in the same S3 bucket artifacts use. Nodes never talk to S3. They stream
// the tar through the control plane, and this package writes the object and the row.
package snapshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/internal/server/artifacts"
	"github.com/podium-ade/podium/internal/server/store"
)

// MaxBytes is the largest workspace tar Podium will store. It is separate from the
// artifact cap: a checkout with installed dependencies does not fit in 512 MB.
const MaxBytes int64 = 8 << 30

// ErrDisabled is returned when no object store is configured.
var ErrDisabled = errors.New("snapshots: no object store is configured (set PODIUM_S3_ENDPOINT)")

// ErrTooLarge is returned for a tar over MaxBytes.
var ErrTooLarge = fmt.Errorf("snapshots: larger than the %d GB limit", MaxBytes>>30)

// ErrNotFound is returned when neither a session snapshot nor a base snapshot exists.
var ErrNotFound = errors.New("snapshots: not found")

// rows is the pointer half of a snapshot. *store.Store implements it.
type rows interface {
	PutWorkspaceSnapshot(ctx context.Context, in store.WorkspaceSnapshot) (string, error)
	GetWorkspaceSnapshot(ctx context.Context, sessionID string) (store.WorkspaceSnapshot, error)
	PutWorkspaceBase(ctx context.Context, in store.WorkspaceSnapshot) (string, error)
	GetWorkspaceBase(ctx context.Context, repo string) (store.WorkspaceSnapshot, error)
}

// Service writes workspace tars to S3 and the pointer to Postgres.
type Service struct {
	store  rows
	s3     *artifacts.S3
	logger *slog.Logger
}

// New returns a Service. A nil *S3 means snapshots are disabled.
func New(st rows, s3 *artifacts.S3, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, s3: s3, logger: logger}
}

// Enabled reports whether an object store is configured.
func (s *Service) Enabled() bool { return s != nil && s.s3 != nil }

// Upload is one tar on its way in. SessionID and Repo are mutually exclusive.
type Upload struct {
	SessionID string
	Repo      string
	NodeID    string
	TaskID    string
	Size      int64
	Body      io.Reader
}

// Stored is what an upload left behind.
type Stored struct {
	ObjectKey string
	SizeBytes int64
	SHA256    string
}

// Put streams a tar into the object store and points the row at it. The previous object
// is deleted only after the row points at the new one. A failed upload leaves the old row.
func (s *Service) Put(ctx context.Context, up Upload) (Stored, error) {
	if !s.Enabled() {
		return Stored{}, ErrDisabled
	}
	session := up.SessionID != ""
	base := up.Repo != ""
	if session == base {
		return Stored{}, errors.New("snapshots: set exactly one of session_id and repo")
	}
	if up.Size > MaxBytes {
		return Stored{}, ErrTooLarge
	}

	id := ids.New("snap")
	var key string
	if session {
		key = "snapshots/sessions/" + sanitize(up.SessionID) + "/" + id + ".tar"
	} else {
		key = "snapshots/bases/" + sanitize(up.Repo) + "/" + id + ".tar"
	}

	counter := &hashingReader{r: io.LimitReader(up.Body, MaxBytes+1), h: sha256.New()}
	size := up.Size
	if size > MaxBytes || size < 0 {
		size = -1
	}
	written, err := s.s3.Put(ctx, key, counter, size, "application/x-tar")
	if err != nil {
		return Stored{}, err
	}
	if counter.n > MaxBytes {
		s.remove(ctx, key)
		return Stored{}, ErrTooLarge
	}
	if written <= 0 {
		written = counter.n
	}
	sum := hex.EncodeToString(counter.h.Sum(nil))
	row := store.WorkspaceSnapshot{
		SessionID: up.SessionID,
		Repo:      up.Repo,
		ObjectKey: key,
		SizeBytes: written,
		SHA256:    sum,
		NodeID:    up.NodeID,
		TaskID:    up.TaskID,
	}
	var previous string
	if session {
		previous, err = s.store.PutWorkspaceSnapshot(ctx, row)
	} else {
		previous, err = s.store.PutWorkspaceBase(ctx, row)
	}
	if err != nil {
		s.remove(ctx, key)
		return Stored{}, err
	}
	if previous != "" {
		s.remove(ctx, previous)
	}
	return Stored{ObjectKey: key, SizeBytes: written, SHA256: sum}, nil
}

// Open returns the bytes of a session snapshot, or the repository base when the session
// has none. ErrNotFound means both are missing.
func (s *Service) Open(ctx context.Context, sessionID, repo string) (store.WorkspaceSnapshot, io.ReadCloser, error) {
	if !s.Enabled() {
		return store.WorkspaceSnapshot{}, nil, ErrDisabled
	}
	row, err := s.lookup(ctx, sessionID, repo)
	if err != nil {
		return store.WorkspaceSnapshot{}, nil, err
	}
	body, err := s.s3.Get(ctx, row.ObjectKey)
	if err != nil {
		return store.WorkspaceSnapshot{}, nil, err
	}
	return row, body, nil
}

func (s *Service) lookup(ctx context.Context, sessionID, repo string) (store.WorkspaceSnapshot, error) {
	if sessionID != "" {
		row, err := s.store.GetWorkspaceSnapshot(ctx, sessionID)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.WorkspaceSnapshot{}, err
		}
	}
	if repo != "" {
		row, err := s.store.GetWorkspaceBase(ctx, repo)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.WorkspaceSnapshot{}, err
		}
	}
	return store.WorkspaceSnapshot{}, ErrNotFound
}

func (s *Service) remove(ctx context.Context, key string) {
	if err := s.s3.Remove(context.WithoutCancel(ctx), key); err != nil {
		s.logger.WarnContext(ctx, "removing a workspace snapshot object failed", "key", key, "error", err)
	}
}

func sanitize(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unnamed"
	}
	if len(out) > 128 {
		return out[:128]
	}
	return out
}

type hashingReader struct {
	r io.Reader
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (c *hashingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		_, _ = c.h.Write(p[:n])
	}
	return n, err
}
