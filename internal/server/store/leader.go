package store

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"
)

// leaderRetry is how often a server that is not the leader asks again, and how often the
// leader checks that the connection holding its lock is still alive.
var leaderRetry = 10 * time.Second

// RunAsLeader runs fn while this server holds the advisory lock named name, and only then. The
// lock lives on a connection of its own: a server that dies, or loses its database, drops the
// connection and with it the lock, and another server's next try takes it. fn's context ends
// when the lock is lost, and only then does RunAsLeader take another turn at leading.
//
// RunAsLeader returns when ctx ends or when fn returns on its own. A nil return means the job
// is finished, not that it wants to run again: a job that is switched off returns at once
// (log roll-up with no object store, snapshot retention with no store), and running it again
// would take a connection and the lock, release both and repeat as fast as Postgres answers —
// a loop that once opened ~100 connections a second and exhausted the host's ports.
//
// It is for work exactly one server should do — rolling task logs up into the object store,
// pruning snapshots — where two servers would race on the same rows and objects.
func (s *Store) RunAsLeader(ctx context.Context, name string, fn func(context.Context) error) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	key := int64(h.Sum64())
	for {
		if ctx.Err() != nil {
			return nil
		}
		pooled, err := s.pool.Acquire(ctx)
		if err != nil {
			if !sleepCtx(ctx, leaderRetry) {
				return nil
			}
			continue
		}
		conn := pooled.Hijack()
		var got bool
		if err := conn.QueryRow(ctx, "select pg_try_advisory_lock($1)", key).Scan(&got); err != nil || !got {
			_ = conn.Close(context.WithoutCancel(ctx))
			if !sleepCtx(ctx, leaderRetry) {
				return nil
			}
			continue
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- fn(runCtx) }()
		var ferr error
		lost := false
	held:
		for {
			select {
			case ferr = <-done:
				break held
			case <-time.After(leaderRetry):
				if err := conn.Ping(ctx); err != nil {
					// The lock went with the connection; somebody else may hold it already.
					lost = true
					cancel()
					<-done
					break held
				}
			}
		}
		cancel()
		_ = conn.Close(context.WithoutCancel(ctx))
		if ferr != nil {
			return fmt.Errorf("leader %s: %w", name, ferr)
		}
		if ctx.Err() != nil || !lost {
			return nil
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
