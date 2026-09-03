package store

import (
	"context"
	"fmt"
)

// The pg_notify channels the control plane listens on.
const (
	// TaskEventsChannel carries task-event wake-ups.
	TaskEventsChannel = "podium_task_events"
	// TasksQueuedChannel carries the id of every task that becomes queued. A database
	// trigger raises it, so it fires for a fresh submission and for a requeue alike, and
	// it is what lets the scheduler answer a `podium run` in milliseconds rather than on
	// its next tick.
	TasksQueuedChannel = "podium_tasks_queued"
)

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
	return s.subscribe(ctx, TaskEventsChannel)
}

// SubscribeQueuedTasks streams the id of every task that becomes queued, from the database
// trigger 0004_scheduler.sql installs. Like SubscribeTaskEvents it hijacks a connection, so
// one subscription per server process is the intended shape.
func (s *Store) SubscribeQueuedTasks(ctx context.Context) (<-chan string, error) {
	return s.subscribe(ctx, TasksQueuedChannel)
}

func (s *Store) subscribe(ctx context.Context, channel string) (<-chan string, error) {
	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire listen connection: %w", err)
	}
	if _, err := pooled.Exec(ctx, "listen "+channel); err != nil {
		pooled.Release()
		return nil, fmt.Errorf("listen on %s: %w", channel, err)
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
