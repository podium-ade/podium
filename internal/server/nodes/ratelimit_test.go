package nodes

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clock is a hand-cranked time source: the limiter's whole behaviour is about time, and a test
// that sleeps is a test that is slow and occasionally wrong.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestRateLimiterAllowsTheBurstThenRefuses(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)}
	r := newRateLimiter(5, time.Minute, c.now)

	for i := range 5 {
		require.True(t, r.allow("100.105.227.25"), "attempt %d should be allowed", i+1)
	}
	require.False(t, r.allow("100.105.227.25"))
	require.False(t, r.allow("100.105.227.25"))
}

func TestRateLimiterBudgetsPerAddress(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Now()}
	r := newRateLimiter(5, time.Minute, c.now)

	for range 5 {
		require.True(t, r.allow("100.105.227.25"))
	}
	require.False(t, r.allow("100.105.227.25"))
	require.True(t, r.allow("100.84.71.97"), "one noisy node must not lock out another")
}

func TestRateLimiterRecoversAfterTheWindow(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Now()}
	r := newRateLimiter(5, time.Minute, c.now)

	for range 5 {
		require.True(t, r.allow("100.105.227.25"))
	}
	require.False(t, r.allow("100.105.227.25"))

	c.advance(59 * time.Second)
	require.False(t, r.allow("100.105.227.25"), "still inside the window")

	c.advance(2 * time.Second)
	require.True(t, r.allow("100.105.227.25"))
}

func TestRateLimiterForgetsIdleAddresses(t *testing.T) {
	t.Parallel()
	c := &clock{t: time.Now()}
	r := newRateLimiter(5, time.Minute, c.now)

	for i := range 100 {
		require.True(t, r.allow(string(rune('a'+i%26))+"-"+time.Duration(i).String()))
	}
	c.advance(2 * time.Minute)
	require.True(t, r.allow("fresh"))

	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.windows, 1, "the sweep should have dropped every expired window")
}

func TestRateLimiterIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()
	r := newRateLimiter(5, time.Minute, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.allow("100.105.227.25") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 5, allowed)
}

func TestRateKey(t *testing.T) {
	t.Parallel()
	require.Equal(t, "100.105.227.25", rateKey("100.105.227.25:41234"))
	require.Equal(t, "fd7a:115c:a1e0::1", rateKey("[fd7a:115c:a1e0::1]:443"))
	require.Equal(t, "unknown", rateKey(""))
	require.Equal(t, "not-an-address", rateKey("not-an-address"))
}
