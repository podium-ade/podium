package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
)

// archivedChunk is how many bytes of a rolled-up stream one synthetic log event carries.
// It matches the node's own coalescing budget, so a replayed log looks like the live one.
const archivedChunk = 64 << 10

// archived reports whether a task's logs have left Postgres: rolled up into the object
// store and pruned out of task_log_chunks. Until the prune the hot rows are still the
// better answer, and nothing here is used.
func (s *Service) archived(ctx context.Context, taskID string) (store.LogRollUp, bool, error) {
	up, err := s.store.TaskLogRollUp(ctx, taskID)
	if err != nil {
		return up, false, err
	}
	if up.At == nil {
		return up, false, nil
	}
	chunks, err := s.store.ListLogChunks(ctx, taskID, 0, 1)
	if err != nil {
		return up, false, err
	}
	return up, len(chunks) == 0, nil
}

// replayArchived serves a rolled-up, pruned task from the object store.
//
// A task rolled up with an index (index.go) is replayed chunk for chunk as it streamed
// live: each chunk under its own sequence number and timestamp, stdout and stderr braided
// as they were. One rolled up before the index existed is replayed into the gaps instead
// (replayGaps), which keeps the order within each stream and nothing else.
func (s *Service) replayArchived(
	ctx context.Context,
	taskID string,
	fromSeq uint64,
	out chan<- *podiumv1.TaskEvent,
) error {
	events, err := s.allEvents(ctx, taskID)
	if err != nil {
		return err
	}
	streams, err := s.archivedStreams(ctx, taskID)
	if err != nil {
		return err
	}
	defer func() {
		for _, st := range streams {
			_ = st.body.Close()
		}
	}()

	raw, err := s.store.TaskLogIndex(ctx, taskID)
	if err != nil {
		return err
	}
	index, err := decodeIndex(raw)
	if err != nil {
		// The bytes are still all there; only the times and the braiding are not.
		s.logger.WarnContext(ctx, "a rolled-up task's log index is unreadable; replaying without it",
			"task_id", taskID, "error", err)
		index = nil
	}

	send := func(e *podiumv1.TaskEvent) bool {
		if e.GetSeq() <= fromSeq {
			return true
		}
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if index != nil {
		return replayIndexed(taskID, events, index, streams, send)
	}
	return replayGaps(taskID, events, streams, send)
}

// eventDrainer emits stored events, in seq order, up to a sequence number.
type eventDrainer struct {
	taskID string
	events []store.Event
	next   int
	send   func(*podiumv1.TaskEvent) bool
}

// upTo emits every stored event up to and including seq. It reports false once the
// reader has gone.
func (d *eventDrainer) upTo(seq uint64) (bool, error) {
	for d.next < len(d.events) && d.events[d.next].Seq <= seq {
		e, err := eventToProto(d.taskID, d.events[d.next])
		if err != nil {
			return false, err
		}
		d.next++
		if !d.send(e) {
			return false, nil
		}
	}
	return true, nil
}

// replayIndexed walks the index, reading each chunk's bytes off its stream's object and
// emitting it the way chunkToProto emits a hot row, with the stored events braided in by
// sequence number. A stream whose object is missing or ends early drops the chunks it
// cannot back, and the rest of the log still replays.
func replayIndexed(
	taskID string,
	events []store.Event,
	index []indexEntry,
	streams []archivedStream,
	send func(*podiumv1.TaskEvent) bool,
) error {
	readers := make(map[string]io.Reader, len(streams))
	for _, st := range streams {
		dec, err := zstd.NewReader(st.body)
		if err != nil {
			return fmt.Errorf("decompress %s of task %s: %w", st.name, taskID, err)
		}
		defer dec.Close()
		readers[st.name] = dec
	}

	drain := &eventDrainer{taskID: taskID, events: events, send: send}
	for _, e := range index {
		if e.Seq > 0 {
			ok, err := drain.upTo(e.Seq - 1)
			if err != nil || !ok {
				return err
			}
		}
		name := e.artifactName()
		r := readers[name]
		if r == nil {
			continue
		}
		buf := make([]byte, e.Len)
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			chunk := store.LogChunk{
				Seq:          e.Seq,
				Stream:       e.Stream,
				Sidecar:      e.Sidecar,
				TS:           e.TS,
				Bytes:        buf[:n],
				SourceOffset: e.SourceOffset,
			}
			if !send(chunkToProto(taskID, chunk)) {
				return nil
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
				return fmt.Errorf("read %s of task %s: %w", name, taskID, rerr)
			}
			delete(readers, name)
		}
	}
	// Everything after the last chunk: exited and finished among them.
	_, err := drain.upTo(^uint64(0))
	return err
}

// replayGaps is the replay of a task rolled up before the index existed.
//
// The seq space the log chunks occupied is gone with them, so the log is put back into the
// gaps: the surviving task_events rows keep their own sequence numbers, and every number
// between 1 and the recorded high-water mark that no event claims carries one chunk of the
// decompressed stream. Ordering is therefore still strictly ascending and from_seq still
// means what it means everywhere else, which is what keeps `podium logs --from-seq` and
// the UI's resume working on a task this old.
//
// What is lost is the interleaving *between* streams: stdout is replayed as one run and
// then stderr, because the object store holds one object per stream and nothing records
// how they were braided together. Within a stream the order is exact.
func replayGaps(
	taskID string,
	events []store.Event,
	streams []archivedStream,
	send func(*podiumv1.TaskEvent) bool,
) error {
	occupied := make(map[uint64]struct{}, len(events))
	for _, e := range events {
		occupied[e.Seq] = struct{}{}
	}

	seq := uint64(0)
	drain := &eventDrainer{taskID: taskID, events: events, send: send}
	for _, st := range streams {
		if err := func() error {
			dec, derr := zstd.NewReader(st.body)
			if derr != nil {
				return fmt.Errorf("decompress %s of task %s: %w", st.name, taskID, derr)
			}
			defer dec.Close()

			buf := make([]byte, archivedChunk)
			for {
				n, rerr := io.ReadFull(dec, buf)
				if n > 0 {
					// Take the next free sequence number, skipping the ones the
					// stored events already own.
					for {
						seq++
						if _, taken := occupied[seq]; !taken {
							break
						}
					}
					ok, eerr := drain.upTo(seq - 1)
					if eerr != nil {
						return eerr
					}
					if !ok {
						return nil
					}
					if !send(archivedLogEvent(taskID, st, seq, buf[:n])) {
						return nil
					}
				}
				if rerr != nil {
					if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
						return nil
					}
					return fmt.Errorf("read %s of task %s: %w", st.name, taskID, rerr)
				}
			}
		}(); err != nil {
			return err
		}
	}

	// Everything the log did not displace: the events above the last synthetic chunk,
	// exited and finished among them.
	_, err := drain.upTo(^uint64(0))
	return err
}

// archivedStream is one rolled-up log object, opened.
type archivedStream struct {
	name    string
	stream  podiumv1.LogChunk_Stream
	sidecar string
	// ts is when the roll-up wrote the object. Without an index the chunks' own timestamps
	// went with the rows, and stamping every replayed chunk with the moment its stream was
	// archived is the only honest answer left.
	ts   time.Time
	body io.ReadCloser
}

// archivedStreams opens a task's rolled-up log objects, stdout first, then stderr, then
// any sidecars by name — the order a reader expects rather than the order the object store
// happens to list them in.
func (s *Service) archivedStreams(ctx context.Context, taskID string) ([]archivedStream, error) {
	if s.archive == nil || !s.archive.Enabled() {
		return nil, nil
	}
	rows, err := s.store.ListArtifactsOfKind(ctx, taskID, store.ArtifactKindLog)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		return streamRank(rows[i].Name) < streamRank(rows[j].Name)
	})

	out := make([]archivedStream, 0, len(rows))
	for _, r := range rows {
		body, oerr := s.archive.Open(ctx, r)
		if oerr != nil {
			for _, o := range out {
				_ = o.body.Close()
			}
			return nil, oerr
		}
		st := archivedStream{name: r.Name, body: body, ts: r.CreatedAt, stream: podiumv1.LogChunk_STREAM_STDOUT}
		switch {
		case r.Name == store.StreamStderr:
			st.stream = podiumv1.LogChunk_STREAM_STDERR
		case strings.HasPrefix(r.Name, "sidecar-"):
			st.stream = podiumv1.LogChunk_STREAM_SIDECAR
			st.sidecar = strings.TrimPrefix(r.Name, "sidecar-")
		}
		out = append(out, st)
	}
	return out, nil
}

// streamRank sorts stdout before stderr before the sidecars.
func streamRank(name string) string {
	switch name {
	case store.StreamStdout:
		return "0"
	case store.StreamStderr:
		return "1"
	default:
		return "2" + name
	}
}

func archivedLogEvent(taskID string, st archivedStream, seq uint64, data []byte) *podiumv1.TaskEvent {
	body := make([]byte, len(data))
	copy(body, data)
	return &podiumv1.TaskEvent{
		TaskId: taskID,
		Seq:    seq,
		Kind:   podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Ts:     timestamppb.New(st.ts),
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream:      st.stream,
			SidecarName: st.sidecar,
			Bytes:       body,
		}},
	}
}

// allEvents reads every stored task_events row for a task, in seq order.
func (s *Service) allEvents(ctx context.Context, taskID string) ([]store.Event, error) {
	var out []store.Event
	next := uint64(0)
	for {
		page, err := s.store.ListEvents(ctx, taskID, next, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return out, nil
		}
		out = append(out, page...)
		next = page[len(page)-1].Seq + 1
	}
}
