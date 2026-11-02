package snapshots

// A session snapshot as a difference from the repository base it started from.
//
// Most of a conversation's workspace is its repository at nearly the commit the base was built
// from, so storing the whole tree for every conversation stores the same files over and over.
// When a session's snapshot is uploaded and the session was started from a base this service
// has a manifest of, only what differs is kept: every entry that is new or changed, plus a
// marker naming what the session deleted. Opening it streams the base without those, then the
// difference — the same full tar a node has always been sent. A node never knows.

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// deltaMarker is the entry a difference ends with: what it deleted from its base. It is at the
// archive's root, outside workspace/, and never reaches a node.
const deltaMarker = ".podium-snapshot-delta.json"

// inMemoryEntry is the largest entry a difference holds in memory while deciding whether it
// changed; a larger one goes through a temporary file.
const inMemoryEntry = 8 << 20

// manifest is a tar's entries: every path, and for a regular file the SHA-256 of its content.
// An entry that is not a regular file maps to "".
type manifest map[string]string

// readManifest reads the manifest of the tar r.
func readManifest(r io.Reader) (manifest, error) {
	out := manifest{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		sum := ""
		if hdr.Typeflag == tar.TypeReg {
			h := sha256.New()
			if _, err := io.Copy(h, tr); err != nil {
				return nil, err
			}
			sum = hex.EncodeToString(h.Sum(nil))
		}
		out[hdr.Name] = sum
	}
}

type deltaMarkerDoc struct {
	Deleted []string `json:"deleted"`
}

// deltaStats is what writing a difference kept and dropped.
type deltaStats struct {
	Kept, Unchanged, Deleted int
}

// writeDelta writes to w the entries of the tar upload that are not in base unchanged, and a
// marker naming base's paths upload no longer has.
func writeDelta(w io.Writer, upload io.Reader, base manifest) (deltaStats, error) {
	var st deltaStats
	tr := tar.NewReader(upload)
	tw := tar.NewWriter(w)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, err
		}
		seen[hdr.Name] = true
		if hdr.Typeflag == tar.TypeDir {
			if _, ok := base[hdr.Name]; ok {
				// The base makes it, and before what is in it.
				st.Unchanged++
				continue
			}
		}
		if hdr.Typeflag != tar.TypeReg {
			// New directories and links: a header each, kept as they are.
			if err := tw.WriteHeader(hdr); err != nil {
				return st, err
			}
			st.Kept++
			continue
		}
		body, sum, err := hashed(tr, hdr.Size)
		if err != nil {
			return st, err
		}
		if want, ok := base[hdr.Name]; ok && want == sum {
			_ = body.Close()
			st.Unchanged++
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = body.Close()
			return st, err
		}
		_, err = io.Copy(tw, body)
		_ = body.Close()
		if err != nil {
			return st, err
		}
		st.Kept++
	}
	var doc deltaMarkerDoc
	for name := range base {
		if !seen[name] {
			doc.Deleted = append(doc.Deleted, name)
		}
	}
	st.Deleted = len(doc.Deleted)
	raw, err := json.Marshal(doc)
	if err != nil {
		return st, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: deltaMarker, Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return st, err
	}
	if _, err := tw.Write(raw); err != nil {
		return st, err
	}
	return st, tw.Close()
}

// hashed reads one entry's content and its SHA-256, and hands the content back to be read
// again: from memory for a small entry, from a temporary file for a large one.
func hashed(r io.Reader, size int64) (io.ReadCloser, string, error) {
	h := sha256.New()
	if size <= inMemoryEntry {
		var buf bytes.Buffer
		if _, err := io.Copy(io.MultiWriter(&buf, h), r); err != nil {
			return nil, "", err
		}
		return io.NopCloser(&buf), hex.EncodeToString(h.Sum(nil)), nil
	}
	f, err := os.CreateTemp("", "podium-snapshot-entry-*")
	if err != nil {
		return nil, "", err
	}
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, "", err
	}
	return &tempFile{f}, hex.EncodeToString(h.Sum(nil)), nil
}

type tempFile struct{ *os.File }

func (t *tempFile) Close() error {
	err := t.File.Close()
	_ = os.Remove(t.Name())
	return err
}

// mergeDelta writes to w the full tar a difference stands for: base's entries, less those the
// difference replaces or deleted, then the difference's own. delta is read twice — once for
// what it names, once to copy it — so it is a file, not a stream.
func mergeDelta(w io.Writer, base io.Reader, delta io.ReadSeeker) error {
	replaced := map[string]bool{}
	var doc deltaMarkerDoc
	dr := tar.NewReader(delta)
	for {
		hdr, err := dr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the difference: %w", err)
		}
		if hdr.Name == deltaMarker {
			if err := json.NewDecoder(dr).Decode(&doc); err != nil {
				return fmt.Errorf("read the difference's marker: %w", err)
			}
			continue
		}
		replaced[hdr.Name] = true
	}
	for _, name := range doc.Deleted {
		replaced[name] = true
	}

	tw := tar.NewWriter(w)
	br := tar.NewReader(base)
	for {
		hdr, err := br.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the base: %w", err)
		}
		if replaced[hdr.Name] {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, br); err != nil {
			return err
		}
	}
	if _, err := delta.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dr = tar.NewReader(delta)
	for {
		hdr, err := dr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == deltaMarker {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, dr); err != nil {
			return err
		}
	}
	return tw.Close()
}
