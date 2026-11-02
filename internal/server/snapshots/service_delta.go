package snapshots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/podium-ade/podium/internal/server/store"
)

// manifestKey is where a base's manifest is stored, beside it.
func manifestKey(baseKey string) string { return baseKey + ".manifest.json" }

func (s *Service) putManifest(ctx context.Context, baseKey string, m manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.s3.Put(ctx, manifestKey(baseKey), bytes.NewReader(raw), int64(len(raw)), "application/json")
	return err
}

func (s *Service) getManifest(ctx context.Context, baseKey string) (manifest, error) {
	body, err := s.s3.Get(ctx, manifestKey(baseKey))
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	var m manifest
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// deltaFile is a difference written to a temporary file, deleted when it is closed.
type deltaFile struct {
	*os.File
	size int64
}

func (d *deltaFile) Close() error {
	err := d.File.Close()
	_ = os.Remove(d.Name())
	return err
}

// asDelta reads a session's upload into a difference from the base the session started from,
// when it started from one this service has a manifest of. ok is false when it did not, and
// then nothing has been read from body. An upload past MaxBytes is ErrTooLarge either way.
func (s *Service) asDelta(ctx context.Context, sessionID string, body io.Reader) (*deltaFile, string, bool, error) {
	origin, err := s.store.GetSessionOrigin(ctx, sessionID)
	if err != nil || origin == "" {
		return nil, "", false, nil
	}
	m, err := s.getManifest(ctx, origin)
	if err != nil {
		return nil, "", false, nil
	}
	f, err := os.CreateTemp("", "podium-snapshot-delta-*.tar")
	if err != nil {
		return nil, "", false, err
	}
	d := &deltaFile{File: f}
	counted := &countingReader{r: body}
	st, err := writeDelta(f, counted, m)
	if err == nil && counted.n > MaxBytes {
		err = ErrTooLarge
	}
	if err == nil {
		d.size, err = f.Seek(0, io.SeekCurrent)
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = d.Close()
		if errors.Is(err, ErrTooLarge) {
			return nil, "", false, err
		}
		return nil, "", false, fmt.Errorf("snapshots: make the difference: %w", err)
	}
	s.logger.DebugContext(ctx, "storing a session snapshot as a difference from its base",
		"session_id", sessionID, "base", origin, "kept", st.Kept, "unchanged", st.Unchanged,
		"deleted", st.Deleted, "upload_bytes", counted.n, "stored_bytes", d.size)
	return d, origin, true, nil
}

// merged is the whole tree a difference stands for, streamed as it is merged with its base.
func (s *Service) merged(ctx context.Context, row store.WorkspaceSnapshot) (io.ReadCloser, error) {
	deltaBody, err := s.s3.Get(ctx, row.ObjectKey)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "podium-snapshot-merge-*.tar")
	if err != nil {
		_ = deltaBody.Close()
		return nil, err
	}
	_, err = io.Copy(f, deltaBody)
	_ = deltaBody.Close()
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	base, err := s.s3.Get(ctx, row.BaseKey)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("snapshots: the base %s a snapshot is a difference from: %w", row.BaseKey, err)
	}
	pr, pw := io.Pipe()
	go func() {
		err := mergeDelta(pw, base, f)
		_ = base.Close()
		_ = f.Close()
		_ = os.Remove(f.Name())
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// closeOnEOF closes w when r is done, so whatever reads the other end of a tee finishes.
type closeOnEOF struct {
	r io.Reader
	w *io.PipeWriter
}

func (c *closeOnEOF) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		_ = c.w.CloseWithError(err)
	}
	return n, err
}

// sweepRetiredBases deletes the replaced bases nothing restores from any more.
func (s *Service) sweepRetiredBases(ctx context.Context) {
	keys, err := s.store.UnreferencedRetiredBases(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "listing retired bases failed", "error", err)
		return
	}
	for _, key := range keys {
		s.remove(ctx, key)
		s.remove(ctx, manifestKey(key))
		if err := s.store.DeleteRetiredBase(ctx, key); err != nil {
			s.logger.WarnContext(ctx, "forgetting a retired base failed", "key", key, "error", err)
		}
	}
}
