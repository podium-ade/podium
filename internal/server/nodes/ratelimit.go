package nodes

import (
	"net"
	"sync"
	"time"
)

// Enrollment budget: a real node enrolls once, ever. Five attempts a minute leaves room for a
// retry loop and a fat-fingered operator while making an enrollment-token guessing run useless.
const (
	enrollBurst  = 5
	enrollWindow = time.Minute
)

// rateLimiter is a fixed-window counter keyed by remote IP. A window is exact enough for a
// budget this small, and it costs one map entry per caller instead of one goroutine.
type rateLimiter struct {
	burst  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	count int
	since time.Time
}

func newRateLimiter(burst int, w time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{burst: burst, window: w, now: now, windows: make(map[string]*window)}
}

// allow records one attempt from key and reports whether it is within budget.
func (r *rateLimiter) allow(key string) bool {
	t := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.windows[key]
	if !ok || t.Sub(w.since) >= r.window {
		// Sweep here rather than on a timer: the map only grows while callers arrive, so
		// the arrival of a caller is exactly when it is worth tidying.
		r.sweep(t)
		r.windows[key] = &window{count: 1, since: t}
		return true
	}
	if w.count >= r.burst {
		return false
	}
	w.count++
	return true
}

func (r *rateLimiter) sweep(t time.Time) {
	for k, w := range r.windows {
		if t.Sub(w.since) >= r.window {
			delete(r.windows, k)
		}
	}
}

// rateKey is the IP an attempt came from. Every port of one machine shares a budget; a caller
// with no parseable address shares one bucket, which is the safe way to fail.
func rateKey(remoteAddr string) string {
	if remoteAddr == "" {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
