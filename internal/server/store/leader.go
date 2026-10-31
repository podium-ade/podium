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
// when the lock is lost; RunAsLeader returns when ctx ends or fn returns an error.
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
	held:
		for {
			select {
			case ferr = <-done:
				break held
			case <-time.After(leaderRetry):
				if err := conn.Ping(ctx); err != nil {
					// The lock went with the connection; somebody else may hold it already.
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
		if ctx.Err() != nil {
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
