//go:build integration

package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOneServerLeadsAndAnotherTakesOver: work only one server may do runs on one, and on
// another once the first lets go of its connection.
func TestOneServerLeadsAndAnotherTakesOver(t *testing.T) {
	old := leaderRetry
	leaderRetry = 50 * time.Millisecond
	t.Cleanup(func() { leaderRetry = old })

	url := newDatabase(t)
	a, err := New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	b, err := New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(b.Close)

	var mu sync.Mutex
	leading := map[string]bool{}
	most := 0
	lead := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			leading[name] = true
			most = max(most, len(leading))
			mu.Unlock()
			<-ctx.Done()
			mu.Lock()
			delete(leading, name)
			mu.Unlock()
			return nil
		}
	}
	aCtx, stopA := context.WithCancel(context.Background())
	bCtx, stopB := context.WithCancel(context.Background())
	t.Cleanup(stopA)
	t.Cleanup(stopB)
	go func() { _ = a.RunAsLeader(aCtx, "podium.test", lead("a")) }()
	time.Sleep(100 * time.Millisecond)
	go func() { _ = b.RunAsLeader(bCtx, "podium.test", lead("b")) }()

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	assert.True(t, leading["a"], "the first to ask leads")
	assert.Equal(t, 1, most, "never two leaders")
	mu.Unlock()

	stopA()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		took := leading["b"]
		mu.Unlock()
		if took {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.True(t, leading["b"], "the other takes over")
	assert.Equal(t, 1, most)
}

// TestAJobThatReturnsIsDone: a job that returns nil while it still leads — one that is
// switched off, say — is finished. It is not run again, and RunAsLeader returns, rather than
// taking the lock and a connection over and over as fast as Postgres answers.
func TestAJobThatReturnsIsDone(t *testing.T) {
	old := leaderRetry
	leaderRetry = 50 * time.Millisecond
	t.Cleanup(func() { leaderRetry = old })

	s, err := New(context.Background(), newDatabase(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	var runs atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.RunAsLeader(ctx, "podium.test.off", func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.NoError(t, ctx.Err(), "RunAsLeader returned on its own, not at the deadline")
	assert.Equal(t, int32(1), runs.Load(), "a finished job runs once")

	// And the lock went with it: another caller can lead at once.
	got := make(chan struct{})
	go func() {
		_ = s.RunAsLeader(ctx, "podium.test.off", func(context.Context) error { close(got); return nil })
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the lock was not released")
	}
}
