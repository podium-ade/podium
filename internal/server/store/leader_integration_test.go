//go:build integration

package store

import (
	"context"
	"sync"
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
