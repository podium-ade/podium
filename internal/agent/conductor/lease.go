package conductor

// More than one conductor on one database.
//
// A conductor keeps a session's in-flight state in memory — whether a turn is running, the
// message that arrived during it, the warm task to inject into — and that is right for one
// process and wrong for two: a second conductor that hears from the same conversation would
// start a second turn beside the first. So every piece of work a conductor does that another
// could also do is LEASED in the database (work_leases): a session's turns, a delegated task
// being followed, a turn that has no task to find it by.
//
//   - A conductor serves a session only while it holds "session:<id>". One that hears a
//     message for a session another conductor holds hands it over: it injects it into the
//     owner's warm task when there is one, and otherwise leaves it on the lease, where the
//     owner takes it as its next turn instead of letting go.
//   - Leases are renewed while the work goes on and expire when a conductor stops renewing,
//     which is what dying looks like. Every conductor looks for such work periodically
//     (adopt) and takes it over: a running task's turn is followed from where the relay
//     stopped, and the relayed ledger keeps anything from being said twice.
//   - A replica's id is stable across its own restarts (host and listen address by default),
//     so a restarted conductor picks up its own work at once rather than waiting out its own
//     leases.
//
// With one conductor every lease is simply taken, and nothing changes but a few writes.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// DefaultLeaseTTL is how long a lease lasts without being renewed: how long another conductor
// waits before deciding this one is gone. It is renewed at a third of that.
const DefaultLeaseTTL = 30 * time.Second

// turnLease is the key of a turn that has no task to find it by — a host turn, or an event the
// conductor delegates itself — so a restart elsewhere does not fail it while it is alive.
func turnLease(turnID string) string { return "turn:" + turnID }

// newReplicaID is an id for a conductor that was given none: random, so it is unique, and
// therefore not stable across restarts — which only costs a restart a lease's wait.
func newReplicaID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "conductor-" + hex.EncodeToString(b[:])
}

// leasing reports whether this conductor shares its work through leases at all.
func (c *Conductor) leasing() bool {
	return c.store != nil && c.replicaID != ""
}

// acquire takes key for this conductor. It answers true when there is nothing to lease, and
// when the database cannot be asked: a conductor that cannot reach its database cannot do the
// work either, and refusing here would turn a blip into lost messages.
func (c *Conductor) acquire(ctx context.Context, key string) bool {
	if !c.leasing() {
		return true
	}
	ok, err := c.store.AcquireLease(ctx, key, c.replicaID, c.leaseTTL)
	if err != nil {
		c.logger.WarnContext(ctx, "taking a lease failed; doing the work anyway", "key", key, "error", err)
		ok = true
	}
	if ok {
		c.mu.Lock()
		c.heldLeases[key] = true
		c.mu.Unlock()
	}
	return ok
}

// release lets go of key. When another conductor handed a message over meanwhile, the lease
// is kept and the message returned: it is this conductor's next turn.
func (c *Conductor) release(ctx context.Context, key string) *InboundEvent {
	if !c.leasing() {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	released, err := c.store.ReleaseLease(ctx, key, c.replicaID)
	if err != nil {
		c.logger.WarnContext(ctx, "releasing a lease failed; it will expire", "key", key, "error", err)
		released = true
	}
	if released {
		c.mu.Lock()
		delete(c.heldLeases, key)
		c.mu.Unlock()
		return nil
	}
	raw, err := c.store.TakePending(ctx, key, c.replicaID)
	if err != nil || raw == nil {
		if err != nil {
			c.logger.WarnContext(ctx, "taking a handed-off message failed", "key", key, "error", err)
		}
		// Nothing to take after all, so let go for real.
		if _, err := c.store.ReleaseLease(ctx, key, c.replicaID); err == nil {
			c.mu.Lock()
			delete(c.heldLeases, key)
			c.mu.Unlock()
		}
		return nil
	}
	var ev InboundEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.logger.WarnContext(ctx, "a handed-off message does not parse; dropping it", "key", key, "error", err)
		return c.release(ctx, key)
	}
	return &ev
}

// handOff gives a message to the conductor serving its session, and reports whether one was.
// False means nobody holds the session any more, and the caller should take it itself.
func (c *Conductor) handOff(ctx context.Context, sessionID string, ev InboundEvent) bool {
	if !c.leasing() {
		return false
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		c.logger.ErrorContext(ctx, "a message could not be encoded for its owner", "error", err)
		return false
	}
	warm, ok, err := c.store.HandOffToOwner(ctx, store.SessionLease(sessionID), c.replicaID, raw)
	if err != nil {
		c.logger.WarnContext(ctx, "handing a message to its session's owner failed", "session_id", sessionID, "error", err)
		return false
	}
	if !ok {
		return false
	}
	if warm == "" {
		c.logger.InfoContext(ctx, "another conductor serves this session; handed the message over",
			"session_id", sessionID)
		return true
	}
	// The owner's task is warm, so the message goes straight into it from here: the control
	// plane does not care which conductor asks.
	if err := c.podium.InjectTask(ctx, warm, ev.Text); err != nil {
		c.logger.WarnContext(ctx, "injecting into another conductor's warm task failed; the message "+
			"is in the conversation for its next turn", "session_id", sessionID, "task_id", warm, "error", err)
		return true
	}
	c.logger.InfoContext(ctx, "injected a message into another conductor's warm task",
		"session_id", sessionID, "task_id", warm)
	return true
}

// noteWarm records a session's waiting task on its lease, so a conductor that does not own the
// session can inject into it. Empty clears it.
func (c *Conductor) noteWarm(ctx context.Context, sessionID, taskID string) {
	if !c.leasing() {
		return
	}
	if err := c.store.SetWarmTask(context.WithoutCancel(ctx), store.SessionLease(sessionID), c.replicaID, taskID); err != nil {
		c.logger.WarnContext(ctx, "recording a session's warm task failed", "session_id", sessionID, "error", err)
	}
}

// renewLeases keeps this conductor's work its own until ctx ends, and looks for work a dead
// conductor left behind at the same pace.
func (c *Conductor) renewLeases(ctx context.Context) {
	if !c.leasing() {
		return
	}
	tick := time.NewTicker(c.leaseTTL / 3)
	defer tick.Stop()
	adoptEvery := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		c.mu.Lock()
		keys := make([]string, 0, len(c.heldLeases))
		for k := range c.heldLeases {
			keys = append(keys, k)
		}
		c.mu.Unlock()
		if err := c.store.RenewLeases(ctx, c.replicaID, keys, c.leaseTTL); err != nil {
			c.logger.WarnContext(ctx, "renewing leases failed", "leases", len(keys), "error", err)
		}
		// Adopting is a scan of every running turn and delegation, so once a lease's length
		// rather than every renewal.
		if adoptEvery++; adoptEvery%3 == 0 {
			c.adopt(ctx)
		}
	}
}

// adopt takes over work whose conductor stopped renewing its leases: a running turn with a
// task, and a delegated task, each followed from where the relay stopped. Work this
// conductor is already doing is skipped, and so is work with no task yet — it has nothing to
// follow, and its own conductor finishes or fails it.
func (c *Conductor) adopt(ctx context.Context) {
	c.recoverWork(ctx, false)
}

// WhileLeader runs fn for as long as this conductor holds key, and only then: it waits for the
// lease, runs fn under a context that ends when the lease is lost, and waits again. It is for
// work exactly one conductor should do — polling a source that every conductor could poll, and
// each would then start the same ticket. fn's error ends it, as fn's own Run would.
func (c *Conductor) WhileLeader(ctx context.Context, key string, fn func(context.Context) error) error {
	if !c.leasing() {
		return fn(ctx)
	}
	every := c.leaseTTL / 3
	for {
		for !c.acquire(ctx, key) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(every):
			}
		}
		c.logger.InfoContext(ctx, "this conductor leads", "key", key, "replica", c.replicaID)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- fn(runCtx) }()
		lost := false
		for !lost {
			select {
			case err := <-done:
				cancel()
				c.release(ctx, key)
				return err
			case <-time.After(every):
				lost = !c.acquire(ctx, key)
			}
		}
		c.logger.WarnContext(ctx, "this conductor no longer leads; stopping", "key", key)
		cancel()
		<-done
		c.mu.Lock()
		delete(c.heldLeases, key)
		c.mu.Unlock()
	}
}
