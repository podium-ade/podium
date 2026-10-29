package logs

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
)

var t0 = time.Date(2026, 10, 28, 0, 8, 12, 483_000_000, time.UTC)

// liveChunks is a task's log as it streamed: stdout and stderr braided, a sidecar, and
// seqs 3 and 6 taken by events.
func liveChunks() []store.LogChunk {
	return []store.LogChunk{
		{Seq: 2, Stream: store.StreamStderr, TS: t0.Add(1 * time.Second), Bytes: []byte("podium-agent: starting\n"), SourceOffset: 23},
		{Seq: 4, Stream: store.StreamStdout, TS: t0.Add(2 * time.Second), Bytes: []byte("tick 1\n"), SourceOffset: 7},
		{Seq: 5, Stream: store.StreamStderr, TS: t0.Add(3 * time.Second), Bytes: []byte("warn: slow\n"), SourceOffset: 34},
		{Seq: 7, Stream: store.StreamStdout, TS: t0.Add(4 * time.Second), Bytes: []byte("tick 2\n"), SourceOffset: 14},
		{Seq: 8, Stream: store.StreamStdout, Sidecar: "db", TS: t0.Add(5 * time.Second), Bytes: []byte("ready\n"), SourceOffset: 6},
	}
}

func liveEvents() []store.Event {
	return []store.Event{
		{Seq: 1, Kind: KindStarted, TS: t0},
		{Seq: 3, Kind: KindMessage, TS: t0.Add(1500 * time.Millisecond), Payload: []byte(`{"type":"progress","text":"hi"}`)},
		{Seq: 6, Kind: KindMessage, TS: t0.Add(3500 * time.Millisecond), Payload: []byte(`{"type":"progress","text":"again"}`)},
		{Seq: 9, Kind: KindExited, TS: t0.Add(6 * time.Second), Payload: []byte(`{"exitCode":0}`)},
	}
}

func buildIndex(t *testing.T, chunks []store.LogChunk) []byte {
	t.Helper()
	b := newIndexBuilder()
	for _, c := range chunks {
		b.add(c)
	}
	data, err := b.bytes()
	require.NoError(t, err)
	return data
}

func TestIndexRoundTrips(t *testing.T) {
	chunks := liveChunks()
	got, err := decodeIndex(buildIndex(t, chunks))
	require.NoError(t, err)
	require.Len(t, got, len(chunks))
	for i, c := range chunks {
		require.Equal(t, indexEntry{
			Seq: c.Seq, Stream: c.Stream, Sidecar: c.Sidecar, Len: len(c.Bytes),
			TS: c.TS, SourceOffset: c.SourceOffset,
		}, got[i])
	}
	require.Equal(t, "sidecar-db", got[4].artifactName())
}

func TestIndexOfNothingIsNil(t *testing.T) {
	data, err := newIndexBuilder().bytes()
	require.NoError(t, err)
	require.Nil(t, data)
	entries, err := decodeIndex(nil)
	require.NoError(t, err)
	require.Nil(t, entries)
}

func TestIndexRefusesWhatItDidNotWrite(t *testing.T) {
	_, err := decodeIndex([]byte("not zstd"))
	require.ErrorIs(t, err, errBadIndex)

	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	_, err = decodeIndex(enc.EncodeAll([]byte("PLI9 something"), nil))
	require.ErrorIs(t, err, errBadIndex)

	// Truncated: the header promises chunks the body does not hold.
	good := buildIndex(t, liveChunks())
	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	raw, err := dec.DecodeAll(good, nil)
	require.NoError(t, err)
	_, err = decodeIndex(enc.EncodeAll(raw[:len(raw)-3], nil))
	require.ErrorIs(t, err, errBadIndex)
}

// archive is what the roll-up stores for chunks: one zstd object per stream.
func archive(t *testing.T, chunks []store.LogChunk) []archivedStream {
	t.Helper()
	bodies := map[string]*bytes.Buffer{}
	order := []string{}
	for _, c := range chunks {
		name := streamArtifactName(c)
		if bodies[name] == nil {
			bodies[name] = &bytes.Buffer{}
			order = append(order, name)
		}
		bodies[name].Write(c.Bytes)
	}
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	out := []archivedStream{}
	for _, name := range order {
		out = append(out, archivedStream{
			name: name,
			ts:   t0.Add(time.Hour),
			body: io.NopCloser(bytes.NewReader(enc.EncodeAll(bodies[name].Bytes(), nil))),
		})
	}
	return out
}

func collect(fromSeq uint64) (func(*podiumv1.TaskEvent) bool, *[]*podiumv1.TaskEvent) {
	var got []*podiumv1.TaskEvent
	return func(e *podiumv1.TaskEvent) bool {
		if e.GetSeq() > fromSeq {
			got = append(got, e)
		}
		return true
	}, &got
}

func TestReplayIndexedIsTheLiveStream(t *testing.T) {
	chunks := liveChunks()
	index, err := decodeIndex(buildIndex(t, chunks))
	require.NoError(t, err)
	send, got := collect(0)
	require.NoError(t, replayIndexed("task_1", liveEvents(), index, archive(t, chunks), send))

	// Exactly what the hot rows would have replayed: every seq, time and byte where it was.
	want := []*podiumv1.TaskEvent{}
	byseq := map[uint64]*podiumv1.TaskEvent{}
	for _, c := range chunks {
		byseq[c.Seq] = chunkToProto("task_1", c)
	}
	for _, e := range liveEvents() {
		p, err := eventToProto("task_1", e)
		require.NoError(t, err)
		byseq[e.Seq] = p
	}
	for seq := uint64(1); seq <= 9; seq++ {
		want = append(want, byseq[seq])
	}
	require.Len(t, *got, len(want))
	for i := range want {
		require.Equal(t, want[i].GetSeq(), (*got)[i].GetSeq())
		require.Equal(t, want[i].GetTs().AsTime(), (*got)[i].GetTs().AsTime(), "seq %d", want[i].GetSeq())
		require.Equal(t, want[i].GetKind(), (*got)[i].GetKind())
		require.Equal(t, want[i].GetLog().GetBytes(), (*got)[i].GetLog().GetBytes())
		require.Equal(t, want[i].GetLog().GetStream(), (*got)[i].GetLog().GetStream())
		require.Equal(t, want[i].GetLog().GetSidecarName(), (*got)[i].GetLog().GetSidecarName())
		require.Equal(t, want[i].GetLog().GetSourceOffset(), (*got)[i].GetLog().GetSourceOffset())
	}
}

func TestReplayIndexedResumesAfterFromSeq(t *testing.T) {
	chunks := liveChunks()
	index, err := decodeIndex(buildIndex(t, chunks))
	require.NoError(t, err)
	send, got := collect(5)
	require.NoError(t, replayIndexed("task_1", liveEvents(), index, archive(t, chunks), send))

	seqs := []uint64{}
	for _, e := range *got {
		seqs = append(seqs, e.GetSeq())
	}
	require.Equal(t, []uint64{6, 7, 8, 9}, seqs)
	// The bytes skipped before from_seq were still read off the stream: seq 7 is its own.
	require.Equal(t, []byte("tick 2\n"), (*got)[1].GetLog().GetBytes())
}

func TestReplayIndexedSurvivesAMissingStream(t *testing.T) {
	chunks := liveChunks()
	index, err := decodeIndex(buildIndex(t, chunks))
	require.NoError(t, err)
	streams := archive(t, chunks)
	without := []archivedStream{}
	for _, st := range streams {
		if st.name != store.StreamStderr {
			without = append(without, st)
		}
	}
	send, got := collect(0)
	require.NoError(t, replayIndexed("task_1", liveEvents(), index, without, send))

	seqs := []uint64{}
	for _, e := range *got {
		seqs = append(seqs, e.GetSeq())
	}
	require.Equal(t, []uint64{1, 3, 4, 6, 7, 8, 9}, seqs)
}

func TestReplayGapsStillServesAnUnindexedTask(t *testing.T) {
	chunks := liveChunks()
	send, got := collect(0)
	require.NoError(t, replayGaps("task_1", liveEvents(), archive(t, chunks), send))

	var logBytes []byte
	for _, e := range *got {
		logBytes = append(logBytes, e.GetLog().GetBytes()...)
	}
	// Every byte is there, one stream after the other, under the archive's own time.
	require.Contains(t, string(logBytes), "podium-agent: starting\nwarn: slow\n")
	require.Contains(t, string(logBytes), "tick 1\ntick 2\n")
	require.Equal(t, KindExited, kindNames[(*got)[len(*got)-1].GetKind()])
}
