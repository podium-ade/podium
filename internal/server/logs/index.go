package logs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/podium-ade/podium/internal/server/store"
)

// The roll-up index is what a task's log chunks were, apart from their bytes: for every
// chunk in seq order, its sequence number, which stream it belonged to, how many bytes of
// that stream it carried, when it was written, and the source offset it reported.
//
// The bytes go to the object store one object per stream, and without the index that is
// all a replay has: it cannot say when a line was written, how stdout and stderr were
// braided, or which sequence number a line had while the task was fresh. With it, a
// rolled-up task replays chunk for chunk the way it streamed live.
//
// The encoding is small on purpose, since it lives on the task row: a table of the
// distinct streams, then one record per chunk as varint deltas, the whole zstd-compressed.
//
//	"PLI1"
//	uvarint streams; per stream: uvarint len, stream, uvarint len, sidecar
//	uvarint chunks;  per chunk:  uvarint seq delta, uvarint stream, uvarint length,
//	                             varint ts delta (ns), varint source offset delta

var indexMagic = []byte("PLI1")

// indexEntry is one chunk of a rolled-up log, without its bytes.
type indexEntry struct {
	Seq          uint64
	Stream       string
	Sidecar      string
	Len          int
	TS           time.Time
	SourceOffset int64
}

// artifactName is the rolled-up object this chunk's bytes are in.
func (e indexEntry) artifactName() string {
	return streamArtifactName(store.LogChunk{Stream: e.Stream, Sidecar: e.Sidecar})
}

// indexBuilder collects entries as the roll-up reads chunks, in seq order.
type indexBuilder struct {
	streams map[[2]string]uint64
	names   [][2]string
	body    bytes.Buffer
	count   uint64
	lastSeq uint64
	lastTS  int64
	lastOff int64
}

func newIndexBuilder() *indexBuilder {
	return &indexBuilder{streams: make(map[[2]string]uint64)}
}

func (b *indexBuilder) add(c store.LogChunk) {
	key := [2]string{c.Stream, c.Sidecar}
	id, ok := b.streams[key]
	if !ok {
		id = uint64(len(b.names))
		b.streams[key] = id
		b.names = append(b.names, key)
	}
	ts := c.TS.UnixNano()
	b.body.Write(binary.AppendUvarint(nil, c.Seq-b.lastSeq))
	b.body.Write(binary.AppendUvarint(nil, id))
	b.body.Write(binary.AppendUvarint(nil, uint64(len(c.Bytes))))
	b.body.Write(binary.AppendVarint(nil, ts-b.lastTS))
	b.body.Write(binary.AppendVarint(nil, c.SourceOffset-b.lastOff))
	b.lastSeq, b.lastTS, b.lastOff = c.Seq, ts, c.SourceOffset
	b.count++
}

// bytes is the compressed index, or nil for a task that logged nothing.
func (b *indexBuilder) bytes() ([]byte, error) {
	if b.count == 0 {
		return nil, nil
	}
	var raw bytes.Buffer
	raw.Write(indexMagic)
	raw.Write(binary.AppendUvarint(nil, uint64(len(b.names))))
	for _, n := range b.names {
		for _, s := range n {
			raw.Write(binary.AppendUvarint(nil, uint64(len(s))))
			raw.WriteString(s)
		}
	}
	raw.Write(binary.AppendUvarint(nil, b.count))
	raw.Write(b.body.Bytes())

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(raw.Bytes(), nil), nil
}

// maxIndexStreams and maxIndexEntries bound what a decode will allocate for, so a
// corrupt row is an error and not an out-of-memory.
const (
	maxIndexStreams = 1 << 16
	maxIndexEntries = 1 << 26
	maxChunkLen     = 1 << 30
)

var errBadIndex = errors.New("malformed log roll-up index")

// decodeIndex reads an index back. Nil in, nil out: a task rolled up before the index
// existed has none, and is replayed the old way.
func decodeIndex(data []byte) ([]indexEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadIndex, err)
	}
	if !bytes.HasPrefix(raw, indexMagic) {
		return nil, fmt.Errorf("%w: unknown version", errBadIndex)
	}
	r := bytes.NewReader(raw[len(indexMagic):])

	uv := func() (uint64, error) {
		v, err := binary.ReadUvarint(r)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", errBadIndex, err)
		}
		return v, nil
	}
	str := func() (string, error) {
		n, err := uv()
		if err != nil {
			return "", err
		}
		if n > uint64(r.Len()) {
			return "", fmt.Errorf("%w: string overruns the index", errBadIndex)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("%w: %w", errBadIndex, err)
		}
		return string(buf), nil
	}

	nStreams, err := uv()
	if err != nil {
		return nil, err
	}
	if nStreams > maxIndexStreams {
		return nil, fmt.Errorf("%w: %d streams", errBadIndex, nStreams)
	}
	names := make([][2]string, nStreams)
	for i := range names {
		for j := range names[i] {
			if names[i][j], err = str(); err != nil {
				return nil, err
			}
		}
	}

	count, err := uv()
	if err != nil {
		return nil, err
	}
	if count > maxIndexEntries {
		return nil, fmt.Errorf("%w: %d chunks", errBadIndex, count)
	}
	out := make([]indexEntry, 0, count)
	var seq uint64
	var ts, off int64
	for range count {
		dSeq, err := uv()
		if err != nil {
			return nil, err
		}
		id, err := uv()
		if err != nil {
			return nil, err
		}
		if id >= nStreams {
			return nil, fmt.Errorf("%w: stream %d of %d", errBadIndex, id, nStreams)
		}
		n, err := uv()
		if err != nil {
			return nil, err
		}
		if n > maxChunkLen {
			return nil, fmt.Errorf("%w: a %d-byte chunk", errBadIndex, n)
		}
		dTS, err := binary.ReadVarint(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBadIndex, err)
		}
		dOff, err := binary.ReadVarint(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBadIndex, err)
		}
		seq, ts, off = seq+dSeq, ts+dTS, off+dOff
		out = append(out, indexEntry{
			Seq:          seq,
			Stream:       names[id][0],
			Sidecar:      names[id][1],
			Len:          int(n),
			TS:           time.Unix(0, ts).UTC(),
			SourceOffset: off,
		})
	}
	return out, nil
}
