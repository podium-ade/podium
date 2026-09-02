package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// AppendLogChunks writes a batch of log chunks and returns the task's log high-water mark.
// Like AppendEvents it is idempotent on (task_id, seq).
func (s *Store) AppendLogChunks(ctx context.Context, taskID string, chunks []LogChunk) (uint64, error) {
	params := make([]db.AppendLogChunkParams, 0, len(chunks))
	for _, c := range chunks {
		if c.Seq > math.MaxInt64 {
			return 0, fmt.Errorf("task %s: log chunk seq %d overflows bigint", taskID, c.Seq)
		}
		params = append(params, db.AppendLogChunkParams{
			TaskID:  taskID,
			Seq:     int64(c.Seq),
			Stream:  c.Stream,
			Sidecar: ptr(c.Sidecar),
			Ts:      c.TS.UTC(),
			Bytes:   c.Bytes,
		})
	}

	var high int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if len(params) > 0 {
			var batchErr error
			q.AppendLogChunk(ctx, params).Exec(func(i int, err error) {
				if err != nil && batchErr == nil {
					batchErr = fmt.Errorf("append log chunk %d of task %s: %w", params[i].Seq, taskID, err)
				}
			})
			if batchErr != nil {
				return batchErr
			}
		}
		h, err := q.MaxLogChunkSeq(ctx, taskID)
		if err != nil {
			return fmt.Errorf("read log high-water mark of task %s: %w", taskID, err)
		}
		high = h
		return nil
	})
	if err != nil {
		return 0, err
	}
	return uint64(high), nil
}

// ListLogChunks returns chunks with seq >= fromSeq in seq order.
func (s *Store) ListLogChunks(ctx context.Context, taskID string, fromSeq uint64, limit int) ([]LogChunk, error) {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if fromSeq > math.MaxInt64 {
		return nil, fmt.Errorf("task %s: fromSeq %d overflows bigint", taskID, fromSeq)
	}
	rows, err := s.q.ListLogChunks(ctx, db.ListLogChunksParams{
		TaskID:    taskID,
		FromSeq:   int64(fromSeq),
		PageLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list log chunks of task %s: %w", taskID, err)
	}
	out := make([]LogChunk, 0, len(rows))
	for _, r := range rows {
		out = append(out, LogChunk{
			Seq:     uint64(r.Seq),
			Stream:  r.Stream,
			Sidecar: deref(r.Sidecar),
			TS:      r.Ts.UTC(),
			Bytes:   r.Bytes,
		})
	}
	return out, nil
}

// PruneLogChunks deletes hot log chunks older than olderThan and reports how many went. Rolled
// up logs live in the object store (step 10); this only trims the Postgres tail.
func (s *Store) PruneLogChunks(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := s.q.PruneLogChunks(ctx, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune log chunks: %w", err)
	}
	return n, nil
}
