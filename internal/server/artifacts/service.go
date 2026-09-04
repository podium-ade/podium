package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// ErrDisabled is returned by every call when no object store is configured. It is not a
// failure of the task that asked: a Podium with no PODIUM_S3_ENDPOINT is still a task
// runner, it just cannot keep files.
var ErrDisabled = errors.New("artifacts: no object store is configured (set PODIUM_S3_ENDPOINT)")

// ErrTooLarge is returned for an artifact over MaxArtifactBytes.
var ErrTooLarge = fmt.Errorf("artifacts: larger than the %d MB limit", MaxArtifactBytes>>20)

// Service records artifacts: it writes the bytes to the object store and the row to
// Postgres, in that order, so a row always describes an object that exists.
type Service struct {
	store  *store.Store
	s3     *S3
	logger *slog.Logger
}

// New returns a Service. A nil *S3 is legitimate and means artifacts are disabled.
func New(st *store.Store, s3 *S3, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, s3: s3, logger: logger}
}

// Enabled reports whether an object store is configured.
func (s *Service) Enabled() bool { return s != nil && s.s3 != nil }

// Ready is the /readyz probe. A disabled service is ready: nothing depends on it.
func (s *Service) Ready(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	return s.s3.Ready(ctx)
}

// EnsureBucket creates the bucket if it is missing.
func (s *Service) EnsureBucket(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return s.s3.EnsureBucket(ctx)
}

// Upload is one artifact on its way in.
type Upload struct {
	TaskID      string
	Kind        string
	Name        string
	ContentType string
	// Size is what the producer expects to send, or 0 when it does not know. It is used
	// for an early rejection only; what is counted is what arrives.
	Size int64
	Body io.Reader
}

// Put streams an upload into the object store and records it.
//
// The bytes go first and the row second: a row that names a missing object is a download
// that fails for no visible reason, while an object with no row is invisible and costs
// only space. If the row cannot be written the object is removed again.
func (s *Service) Put(ctx context.Context, up Upload) (store.Artifact, error) {
	if !s.Enabled() {
		return store.Artifact{}, ErrDisabled
	}
	if up.TaskID == "" || up.Name == "" {
		return store.Artifact{}, errors.New("artifacts: task_id and name are required")
	}
	if up.Size > MaxArtifactBytes {
		return store.Artifact{}, ErrTooLarge
	}
	if up.Kind == "" {
		up.Kind = store.ArtifactKindFile
	}

	id := ids.NewArtifact()
	key := ArtifactKey(up.TaskID, id, up.Name)
	if up.Kind == store.ArtifactKindLog {
		key = LogKey(up.TaskID, up.Name)
	}

	// The cap is enforced on the way past, not on the declared size: a producer that lies
	// about its length must not be able to fill the bucket. limitedReader stops one byte
	// over the limit so the overrun is detectable rather than silently truncated.
	counter := &hashingReader{r: io.LimitReader(up.Body, MaxArtifactBytes+1), h: sha256.New()}
	size := up.Size
	// A negative size is the caller saying it does not know; -1 is how that reaches
	// minio-go. Zero is not that: an empty file is a legitimate artifact whose length is
	// known exactly, and calling it unknown makes the client stream it with no
	// Content-Length, which an S3 endpoint answers with MissingContentLength.
	if size > MaxArtifactBytes || size < 0 {
		size = -1
	}
	written, err := s.s3.Put(ctx, key, counter, size, up.ContentType)
	if err != nil {
		return store.Artifact{}, err
	}
	if counter.n > MaxArtifactBytes {
		if rerr := s.s3.Remove(context.WithoutCancel(ctx), key); rerr != nil {
			s.logger.WarnContext(ctx, "removing an oversized artifact failed", "key", key, "error", rerr)
		}
		return store.Artifact{}, ErrTooLarge
	}
	if written <= 0 {
		written = counter.n
	}

	art, err := s.store.CreateArtifact(ctx, store.NewArtifact{
		ID:          id,
		TaskID:      up.TaskID,
		Kind:        up.Kind,
		Name:        up.Name,
		ObjectKey:   key,
		SizeBytes:   written,
		ContentType: up.ContentType,
		SHA256:      hex.EncodeToString(counter.h.Sum(nil)),
	})
	if err != nil {
		if rerr := s.s3.Remove(context.WithoutCancel(ctx), key); rerr != nil {
			s.logger.WarnContext(ctx, "removing an unrecorded artifact failed", "key", key, "error", rerr)
		}
		return store.Artifact{}, err
	}
	s.logger.InfoContext(ctx, "artifact stored",
		"task_id", up.TaskID, "artifact_id", art.ID, "kind", art.Kind,
		"name", art.Name, "key", art.ObjectKey, "bytes", art.SizeBytes)
	return art, nil
}

// List returns a task's artifacts.
func (s *Service) List(ctx context.Context, taskID string) ([]store.Artifact, error) {
	return s.store.ListArtifacts(ctx, taskID)
}

// Get reads one artifact row.
func (s *Service) Get(ctx context.Context, id string) (store.Artifact, error) {
	return s.store.GetArtifact(ctx, id)
}

// Open streams an artifact's bytes out of the object store.
func (s *Service) Open(ctx context.Context, a store.Artifact) (io.ReadCloser, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	return s.s3.Get(ctx, a.ObjectKey)
}

// PresignGet mints a download URL for an artifact and says when it stops working.
func (s *Service) PresignGet(ctx context.Context, a store.Artifact) (*url.URL, time.Time, error) {
	if !s.Enabled() {
		return nil, time.Time{}, ErrDisabled
	}
	u, err := s.s3.PresignGet(ctx, a.ObjectKey, a.Name, PresignTTL)
	if err != nil {
		return nil, time.Time{}, err
	}
	return u, time.Now().UTC().Add(PresignTTL), nil
}

// hashingReader counts and hashes what passes through it, so an upload's size and digest
// come out of the same pass the object store reads.
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
