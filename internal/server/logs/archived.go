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

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
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
	occupied := make(map[uint64]struct{}, len(events))
	for _, e := range events {
		occupied[e.Seq] = struct{}{}
	}

	streams, err := s.archivedStreams(ctx, taskID)
	if err != nil {
		return err
	}

	seq := uint64(0)
	next := 0 // index into events
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
	// advance emits every stored event up to and including seq.
	drainEvents := func(upto uint64) (bool, error) {
		for next < len(events) && events[next].Seq <= upto {
			e, cerr := eventToProto(taskID, events[next])
			if cerr != nil {
				return false, cerr
			}
			next++
			if !send(e) {
				return false, nil
			}
		}
		return true, nil
	}

	for _, st := range streams {
		if err := func() error {
			defer func() { _ = st.body.Close() }()
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
					ok, eerr := drainEvents(seq - 1)
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
	_, err = drainEvents(^uint64(0))
	return err
}

// archivedStream is one rolled-up log object, opened.
type archivedStream struct {
	name    string
	stream  podiumv1.LogChunk_Stream
	sidecar string
	// ts is when the roll-up wrote the object. The chunks' own timestamps went with the
	// rows, and stamping every replayed chunk with the moment its stream was archived is
	// the only honest answer left.
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
