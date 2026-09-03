package scheduler

import (
	"os"
	"strconv"
	"time"
)

// FastTimersEnv shrinks every interval in Timing tenfold when it is set to a true value.
// Tests set it so a scenario that waits out a 120s offline threshold takes 12 seconds
// instead. It is read once, by TimingFromEnv, and never consulted again.
const FastTimersEnv = "PODIUM_TEST_FAST_TIMERS"

// FastTimerDivisor is how much PODIUM_TEST_FAST_TIMERS=1 shrinks the intervals by.
const FastTimerDivisor = 10

// minInterval is the floor a shrunk interval is clamped to. Below about a millisecond a
// ticker costs more than the work it schedules, and a test that depends on sub-millisecond
// timing is measuring the runtime, not Podium.
const minInterval = time.Millisecond

// Timing is every clock the scheduler runs on, in one struct so a test can shrink them all
// together and so an operator reading the code finds the whole policy in one place.
//
// The defaults are the canonical numbers from the design: heartbeat every 10s, unreachable
// at 30s, offline at 120s, a 15s provisioning deadline, a 30s SIGTERM→SIGKILL grace on the
// node and 60s before the server gives up waiting for a cancelled task's node.
type Timing struct {
	// Tick is how often queued work is claimed. A pg_notify wake-up beats it to the punch
	// for anything freshly submitted; this is the backstop.
	Tick time.Duration
	// Watchdog is how often leases, timeouts, cancels and node health are swept.
	Watchdog time.Duration
	// LeaseTTL is how long an assignment's lease lasts before the task has proved it is
	// being worked on.
	LeaseTTL time.Duration
	// ProvisioningDeadline is how long a node has, after Assign, to say provisioning. Past
	// it the assignment is revoked and the task requeued.
	ProvisioningDeadline time.Duration
	// LeaseGrace is added to the spec's timeout to give a live task's lease its expiry, so
	// a lease never expires under a task that is still legitimately running.
	LeaseGrace time.Duration
	// UnreachableAfter is how long without a heartbeat makes a node unreachable. Its tasks
	// keep running: a node that cannot talk is not a node that has stopped working.
	UnreachableAfter time.Duration
	// OfflineAfter is how long without a heartbeat makes a node offline and expires its
	// leases.
	OfflineAfter time.Duration
	// CancelGrace is how long a cancelled task is given to end by itself before the server
	// writes the terminal status without the node.
	CancelGrace time.Duration
	// ClaimLimit is how many queued tasks one tick considers. It is a batch size, not a
	// duration, and PODIUM_TEST_FAST_TIMERS does not touch it.
	ClaimLimit int
}

// DefaultTiming is the shipped policy.
func DefaultTiming() Timing {
	return Timing{
		Tick:                 500 * time.Millisecond,
		Watchdog:             5 * time.Second,
		LeaseTTL:             2 * time.Minute,
		ProvisioningDeadline: 15 * time.Second,
		LeaseGrace:           5 * time.Minute,
		UnreachableAfter:     30 * time.Second,
		OfflineAfter:         120 * time.Second,
		CancelGrace:          60 * time.Second,
		ClaimLimit:           50,
	}
}

// TimingFromEnv is DefaultTiming, shrunk when PODIUM_TEST_FAST_TIMERS says so.
func TimingFromEnv() Timing {
	t := DefaultTiming()
	if fastTimers(os.Getenv(FastTimersEnv)) {
		t = t.Fast()
	}
	return t
}

// Fast returns the same policy with every interval divided by FastTimerDivisor.
func (t Timing) Fast() Timing {
	t.Tick = shrink(t.Tick)
	t.Watchdog = shrink(t.Watchdog)
	t.LeaseTTL = shrink(t.LeaseTTL)
	t.ProvisioningDeadline = shrink(t.ProvisioningDeadline)
	t.LeaseGrace = shrink(t.LeaseGrace)
	t.UnreachableAfter = shrink(t.UnreachableAfter)
	t.OfflineAfter = shrink(t.OfflineAfter)
	t.CancelGrace = shrink(t.CancelGrace)
	return t
}

func shrink(d time.Duration) time.Duration {
	return max(d/FastTimerDivisor, minInterval)
}

// fastTimers accepts what strconv.ParseBool accepts, so 1, true and TRUE all work and
// anything else — including the empty string — is off.
func fastTimers(v string) bool {
	on, err := strconv.ParseBool(v)
	return err == nil && on
}
