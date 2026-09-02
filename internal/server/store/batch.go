package store

import (
	"context"
	"fmt"
	"math"

	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// AppendBatch writes one node event batch — the non-log events and the log chunks it contains —
// in a single transaction. A node numbers every event of a task from one monotonic seq space, so
// a batch normally straddles both tables; splitting it across two transactions would let a reader
// see the second half of a batch without the first.
//
// Like AppendEvents and AppendLogChunks it is idempotent on (task_id, seq), so a replayed batch
// costs one round trip and changes nothing.
func (s *Store) AppendBatch(ctx context.Context, taskID string, events []Event, chunks []LogChunk) error {
	eventParams := make([]db.AppendEventParams, 0, len(events))
	for _, e := range events {
		if e.Seq > math.MaxInt64 {
			return fmt.Errorf("task %s: event seq %d overflows bigint", taskID, e.Seq)
		}
		payload := e.Payload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		eventParams = append(eventParams, db.AppendEventParams{
			TaskID:  taskID,
			Seq:     int64(e.Seq),
			Kind:    e.Kind,
			Ts:      e.TS.UTC(),
			Payload: payload,
		})
	}
	chunkParams := make([]db.AppendLogChunkParams, 0, len(chunks))
	for _, c := range chunks {
		if c.Seq > math.MaxInt64 {
			return fmt.Errorf("task %s: log chunk seq %d overflows bigint", taskID, c.Seq)
		}
		chunkParams = append(chunkParams, db.AppendLogChunkParams{
			TaskID:  taskID,
			Seq:     int64(c.Seq),
			Stream:  c.Stream,
			Sidecar: ptr(c.Sidecar),
			Ts:      c.TS.UTC(),
			Bytes:   c.Bytes,
		})
	}

	return s.inTx(ctx, func(q *db.Queries) error {
		var batchErr error
		if len(eventParams) > 0 {
			q.AppendEvent(ctx, eventParams).Exec(func(i int, err error) {
				if err != nil && batchErr == nil {
					batchErr = fmt.Errorf("append event %d of task %s: %w", eventParams[i].Seq, taskID, err)
				}
			})
			if batchErr != nil {
				return batchErr
			}
		}
		if len(chunkParams) > 0 {
			q.AppendLogChunk(ctx, chunkParams).Exec(func(i int, err error) {
				if err != nil && batchErr == nil {
					batchErr = fmt.Errorf("append log chunk %d of task %s: %w", chunkParams[i].Seq, taskID, err)
				}
			})
		}
		return batchErr
	})
}
