package store

import (
	"context"
	"fmt"
	"math"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// AppendEvents writes a batch of task events and returns the task's high-water mark: the
// highest seq stored for taskID after the write. Re-appending a batch is a no-op because
// (task_id, seq) is the primary key, so a node replaying from its buffer costs nothing.
func (s *Store) AppendEvents(ctx context.Context, taskID string, events []Event) (uint64, error) {
	params := make([]db.AppendEventParams, 0, len(events))
	for _, e := range events {
		if e.Seq > math.MaxInt64 {
			return 0, fmt.Errorf("task %s: event seq %d overflows bigint", taskID, e.Seq)
		}
		payload := e.Payload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		params = append(params, db.AppendEventParams{
			TaskID:  taskID,
			Seq:     int64(e.Seq),
			Kind:    e.Kind,
			Ts:      e.TS.UTC(),
			Payload: payload,
		})
	}

	var high int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if len(params) > 0 {
			var batchErr error
			q.AppendEvent(ctx, params).Exec(func(i int, err error) {
				if err != nil && batchErr == nil {
					batchErr = fmt.Errorf("append event %d of task %s: %w", params[i].Seq, taskID, err)
				}
			})
			if batchErr != nil {
				return batchErr
			}
		}
		h, err := q.MaxEventSeq(ctx, taskID)
		if err != nil {
			return fmt.Errorf("read event high-water mark of task %s: %w", taskID, err)
		}
		high = h
		return nil
	})
	if err != nil {
		return 0, err
	}
	return uint64(high), nil
}

// ListEvents returns events with seq >= fromSeq in seq order.
func (s *Store) ListEvents(ctx context.Context, taskID string, fromSeq uint64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if fromSeq > math.MaxInt64 {
		return nil, fmt.Errorf("task %s: fromSeq %d overflows bigint", taskID, fromSeq)
	}
	rows, err := s.q.ListEvents(ctx, db.ListEventsParams{
		TaskID:    taskID,
		FromSeq:   int64(fromSeq),
		PageLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list events of task %s: %w", taskID, err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, Event{
			Seq:     uint64(r.Seq),
			Kind:    r.Kind,
			TS:      r.Ts.UTC(),
			Payload: r.Payload,
		})
	}
	return out, nil
}
