package store

import (
	"context"
	"fmt"
)

// TaskEventsChannel is the pg_notify channel task-event wake-ups travel on.
const TaskEventsChannel = "podium_task_events"

// NotifyTaskEvents wakes every SubscribeTaskEvents listener for taskID. Call it after the
// transaction that stored the events has committed.
func (s *Store) NotifyTaskEvents(ctx context.Context, taskID string) error {
	if err := s.q.NotifyTaskEvents(ctx, taskID); err != nil {
		return fmt.Errorf("notify task events for %s: %w", taskID, err)
	}
	return nil
}

// SubscribeTaskEvents takes a connection out of the pool, puts it on LISTEN and streams the
// task IDs that have new events. The payload is a wake-up, not the event itself: read the rows
// from the store. The channel closes when ctx is cancelled or the connection drops, so
// cancelling ctx is how a caller unsubscribes.
//
// The connection is hijacked, not borrowed: a connection that has run LISTEN must never go
// back into the pool. One subscription per server process is the intended shape — fan out to
// individual clients in memory.
func (s *Store) SubscribeTaskEvents(ctx context.Context) (<-chan string, error) {
	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire listen connection: %w", err)
	}
	if _, err := pooled.Exec(ctx, "listen "+TaskEventsChannel); err != nil {
		pooled.Release()
		return nil, fmt.Errorf("listen on %s: %w", TaskEventsChannel, err)
	}
	conn := pooled.Hijack()

	out := make(chan string, 64)
	go func() {
		defer close(out)
		defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
		for {
			n, err := conn.WaitForNotification(ctx)
			if err != nil {
				return
			}
			select {
			case out <- n.Payload:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
