package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestAStopBeforeTheTurnRegistersStillStopsIt: Stop pressed between the turn row reading
// running and the turn taking its stop is remembered and applied when it registers.
func TestAStopBeforeTheTurnRegistersStillStopsIt(t *testing.T) {
	c := &Conductor{}
	stopped, deferred := c.stopHostTurnSoon("C1/1.1")
	assert.False(t, stopped)
	assert.True(t, deferred, "nothing registered yet, so the Stop waits")

	cancelled := false
	c.registerHost("C1/1.1", func() { cancelled = true })
	assert.True(t, cancelled, "registering honours the remembered Stop")
	assert.Empty(t, c.earlyStops, "and forgets it")

	// A second turn on the same ref is not stopped by the one already used.
	again := false
	c.unregisterHost("C1/1.1")
	c.registerHost("C1/1.1", func() { again = true })
	assert.False(t, again)
}

// TestAStopForARegisteredTurnIsImmediate: the ordinary case is not deferred.
func TestAStopForARegisteredTurnIsImmediate(t *testing.T) {
	c := &Conductor{}
	cancelled := false
	c.registerHost("C1/2.1", func() { cancelled = true })
	stopped, deferred := c.stopHostTurnSoon("C1/2.1")
	assert.True(t, stopped)
	assert.False(t, deferred)
	assert.True(t, cancelled)
}

// TestAStaleEarlyStopIsIgnored: a Stop older than earlyStopTTL belongs to nothing that is
// still starting, so a turn registering later on the same ref runs.
func TestAStaleEarlyStopIsIgnored(t *testing.T) {
	c := &Conductor{earlyStops: map[string]time.Time{"C1/3.1": time.Now().Add(-2 * earlyStopTTL)}}
	cancelled := false
	c.registerHost("C1/3.1", func() { cancelled = true })
	assert.False(t, cancelled)
}
