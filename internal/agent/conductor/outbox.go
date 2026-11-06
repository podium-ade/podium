package conductor

// Delivery: how an answer reaches the surface a turn started on.
//
// The web chat is the record (Phase 3 of docs/agent.md), so an answer
// is written there first, and the copy on Slack or GitHub is a delivery the conductor owes.
// Before this, a post that failed was a log line and an answer nobody in the thread saw. Now
// it is a row in chat_outbox: attempted at once, retried with backoff by whichever conductor
// gets to it, given up on after outboxMaxAttempts, and visible in the web chat as "not
// delivered" with a retry a person can press.
//
// Routing follows from where the turn started, because a turn's Source is where it started:
//
//	web-started    → the web chat only (the chat source is not mirrored; nothing is owed)
//	Slack-started  → that thread, plus the web record
//	GitHub-started → that pull request, plus every Slack thread bound to it, plus the record
//
// Only what a person has to see goes through here: an answer, a failure, a question. Progress
// is edited in place and superseded by the answer; an activity line is for the web chat. A
// lost progress edit is not worth a retry queue, and retrying one later would put a stale
// "working on it" under the answer.
//
// The first attempt is inline, in the turn, exactly where the post always was: a turn that
// posts and then attaches files or reacts still does those in order, and a source's own
// state (the GitHub status comment, the Slack placeholder) sees the post when it always did.
// The outbox row exists before that attempt, due a grace period later, so a conductor that
// dies mid-post leaves work another conductor finishes. Delivery is therefore at least once:
// a crash after the surface accepted the post and before the row was marked sent posts it
// again. Losing an answer is the worse failure.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// outboxGrace is how long a row written before its inline attempt waits before anyone
	// else may take it. It covers the slowest post a source makes (a GitHub review with a
	// status comment is several API calls), so a live attempt is never doubled.
	outboxGrace = 2 * time.Minute
	// outboxLease is how long a claimed row is held by the conductor working it.
	outboxLease = 2 * time.Minute
	// outboxPoll is how often the worker looks for due rows.
	outboxPoll = 5 * time.Second
	// outboxPage is how many rows one poll claims.
	outboxPage = 10
	// outboxMaxAttempts is when a delivery is given up on and shown as failed.
	outboxMaxAttempts = 6
	// outboxBaseDelay and outboxMaxDelay bound the backoff: 10s, 20s, 40s, … capped.
	outboxBaseDelay = 10 * time.Second
	outboxMaxDelay  = 15 * time.Minute
)

// durableOut is whether an Outbound is one a person has to see, and so is delivered through
// the outbox.
func durableOut(t string) bool {
	return t == OutFinal || t == OutFailure || t == OutQuestion
}

// outboxBackoff is the delay before attempt n+1, after n attempts have failed.
func outboxBackoff(attempts int) time.Duration {
	return backoffFrom(outboxBaseDelay, attempts)
}

// retryDelay is outboxBackoff on this conductor's base, which a test shortens.
func (c *Conductor) retryDelay(attempts int) time.Duration {
	if c.outboxBase < 0 {
		return 0
	}
	if c.outboxBase > 0 {
		return backoffFrom(c.outboxBase, attempts)
	}
	return outboxBackoff(attempts)
}

func backoffFrom(base time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(attempts-1)))
	if d > outboxMaxDelay || d <= 0 {
		return outboxMaxDelay
	}
	return d
}

// delivery is the bot message a post belongs to, carried in the context so a source that
// fans an answer out (the GitHub source, to bound Slack threads) can hand each copy to the
// outbox under the same message.
type delivery struct {
	chatID string
	seq    uint64
	// retry is set when the outbox worker is re-sending one row. A fan-out during a retry
	// does nothing: each fanned-out copy has its own row and its own retries, and sending
	// them again with the primary would double every one that already landed.
	retry bool
}

type deliveryKey struct{}

func withDelivery(ctx context.Context, d delivery) context.Context {
	return context.WithValue(ctx, deliveryKey{}, d)
}

func deliveryFrom(ctx context.Context) (delivery, bool) {
	d, ok := ctx.Value(deliveryKey{}).(delivery)
	return d, ok
}

// Deliverer is what a source that fans an answer out to another source is given, so each
// copy is delivered (and retried) like the primary one. DeliverTo returns false when there
// is no delivery to hang the copy on — a post outside a turn, a conductor with no store —
// and the source posts the copy itself, as it always did.
type Deliverer interface {
	DeliverTo(ctx context.Context, kind, ref string, out Outbound) bool
}

// deliverySetter is the optional half of Source the conductor hands itself to.
type deliverySetter interface {
	SetDeliverer(Deliverer)
}

func payloadOf(out Outbound) store.OutboxPayload {
	return store.OutboxPayload{
		Type: out.Type, Text: out.Text, TaskID: out.TaskID,
		Playbook: out.Playbook, SourcePlaybook: out.SourcePlaybook,
	}
}

func outboundOf(p store.OutboxPayload) Outbound {
	return Outbound{
		Type: p.Type, Text: p.Text, TaskID: p.TaskID,
		Speaker: Speaker{Playbook: p.Playbook, SourcePlaybook: p.SourcePlaybook},
	}
}

// postDurable is post for an answer, a failure or a question in a mirrored conversation:
// record it, owe it, attempt it. It returns the surface's id for the post, as post does, and
// false when the conversation is not one the outbox covers (the caller posts as before).
func (c *Conductor) postDurable(ctx context.Context, src Source, ref string, out Outbound) (string, bool) {
	if c.store == nil || src == nil {
		return "", false
	}
	if _, mirrored := c.mirrorKey(src, ref); !mirrored {
		return "", false
	}
	row, ok := c.mirrorSaidRow(ctx, src, ref, out)
	if !ok {
		// No record to hang a delivery on. Post as before: the thread still hears it.
		return "", false
	}
	it, err := c.store.EnqueueOutbox(ctx, row.ChatID, row.Seq, src.Kind(), ref, payloadOf(out),
		time.Now().Add(outboxGrace))
	if err != nil {
		c.logger.WarnContext(ctx, "recording a delivery failed; posting without a retry behind it",
			"ref", ref, "error", err)
		id, perr := src.Post(withDelivery(ctx, delivery{chatID: row.ChatID, seq: row.Seq}), ref, out)
		if perr != nil {
			c.logger.WarnContext(ctx, "posting to the conversation failed", "ref", ref, "type", out.Type, "error", perr)
			return "", true
		}
		return id, true
	}
	id, _ := c.attemptDelivery(withDelivery(ctx, delivery{chatID: row.ChatID, seq: row.Seq}), src, it, out, true)
	return id, true
}

// attemptDelivery makes one attempt at one row and records how it went. first is the inline
// attempt, whose row was just written and is counted here; a worker's claim already counted.
func (c *Conductor) attemptDelivery(
	ctx context.Context, src Source, it store.OutboxItem, out Outbound, first bool,
) (string, error) {
	attempts := it.Attempts
	if first {
		ok, err := c.store.BeginOutboxAttempt(ctx, it.ID)
		if err != nil || !ok {
			c.logger.WarnContext(ctx, "starting a delivery attempt failed; the worker will make it",
				"outbox_id", it.ID, "error", err)
			return "", err
		}
		attempts++
	}
	id, err := src.Post(ctx, it.SurfaceRef, out)
	if err != nil {
		c.metric(func(m *Metrics) { m.OutboxDeliveries.WithLabelValues(it.SurfaceKind, "retry").Inc() })
		giveUp := attempts >= outboxMaxAttempts
		next := time.Now().Add(c.retryDelay(attempts))
		if merr := c.store.MarkOutboxRetry(ctx, it.ID, err.Error(), next, giveUp); merr != nil {
			c.logger.ErrorContext(ctx, "recording a failed delivery failed", "outbox_id", it.ID, "error", merr)
		}
		if giveUp {
			c.metric(func(m *Metrics) { m.OutboxDeliveries.WithLabelValues(it.SurfaceKind, "failed").Inc() })
			c.logger.ErrorContext(ctx, "gave up delivering an answer; the web chat shows it as not delivered",
				"outbox_id", it.ID, "surface", it.SurfaceKind, "ref", it.SurfaceRef, "attempts", attempts, "error", err)
			c.announceDelivery(ctx, it.ChatID, it.MessageSeq)
		} else {
			c.logger.WarnContext(ctx, "delivering an answer failed; it will be retried",
				"outbox_id", it.ID, "surface", it.SurfaceKind, "attempts", attempts, "next", next, "error", err)
		}
		return "", err
	}
	if merr := c.store.MarkOutboxSent(ctx, it.ID, id); merr != nil {
		c.logger.ErrorContext(ctx, "recording a delivery failed after it landed; it may be sent again",
			"outbox_id", it.ID, "error", merr)
	}
	if id != "" {
		if _, serr := c.store.SetChatMessageExternalID(ctx, it.ChatID, it.MessageSeq, id); serr != nil {
			c.logger.WarnContext(ctx, "recording where an answer landed failed", "outbox_id", it.ID, "error", serr)
		}
	}
	c.metric(func(m *Metrics) { m.OutboxDeliveries.WithLabelValues(it.SurfaceKind, "sent").Inc() })
	if !first || it.Attempts > 0 {
		// A retry that landed changes what the web chat shows under the message.
		c.announceDelivery(ctx, it.ChatID, it.MessageSeq)
	}
	return id, nil
}

// DeliverTo implements Deliverer: one fanned-out copy of the answer the context is posting,
// owed and attempted like the primary.
func (c *Conductor) DeliverTo(ctx context.Context, kind, ref string, out Outbound) bool {
	d, ok := deliveryFrom(ctx)
	if !ok || c.store == nil || !durableOut(out.Type) {
		return false
	}
	if d.retry {
		// This copy has its own row; see delivery.retry.
		return true
	}
	src := c.sourceOfKind(kind)
	if src == nil {
		return false
	}
	it, err := c.store.EnqueueOutbox(ctx, d.chatID, d.seq, kind, ref, payloadOf(out), time.Now().Add(outboxGrace))
	if err != nil {
		c.logger.WarnContext(ctx, "recording a fanned-out delivery failed; posting it without a retry behind it",
			"kind", kind, "ref", ref, "error", err)
		return false
	}
	// The copy is posted with no delivery in its context: it is not itself fanned out again.
	_, _ = c.attemptDelivery(stripDelivery(ctx), src, it, out, true)
	return true
}

// stripDelivery is ctx without the delivery value. context has no delete, so the value is
// shadowed with a zero one that deliveryFrom reports as absent.
func stripDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, deliveryKey{}, nil)
}

// sourceOfKind is the live source of one kind, nil when this conductor has none.
func (c *Conductor) sourceOfKind(kind string) Source {
	for _, s := range c.sourceList() {
		if s.Kind() == kind {
			return s
		}
	}
	return nil
}

// deliverOutbox is the worker: it claims due rows and retries them until ctx ends.
func (c *Conductor) deliverOutbox(ctx context.Context) {
	if c.store == nil {
		return
	}
	t := time.NewTicker(outboxPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.deliverDue(ctx)
	}
}

// deliverDue claims one page of due rows and attempts each. It returns how many it claimed,
// which a test drives directly.
func (c *Conductor) deliverDue(ctx context.Context) int {
	items, err := c.store.ClaimOutbox(ctx, outboxPage, outboxLease)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.WarnContext(ctx, "claiming due deliveries failed", "error", err)
		}
		return 0
	}
	c.deliverClaimed(ctx, items)
	return len(items)
}

// deliverClaimed attempts deliveries this conductor has claimed.
func (c *Conductor) deliverClaimed(ctx context.Context, items []store.OutboxItem) {
	for _, it := range items {
		src := c.sourceOfKind(it.SurfaceKind)
		if src == nil {
			// This conductor does not run that source (it was disconnected, or this
			// replica was started without it). The row is left for one that does, on the
			// same backoff, so a source that never comes back still ends as failed.
			err := fmt.Errorf("no %s source on this conductor", it.SurfaceKind)
			giveUp := it.Attempts >= outboxMaxAttempts
			if merr := c.store.MarkOutboxRetry(ctx, it.ID, err.Error(),
				time.Now().Add(c.retryDelay(it.Attempts)), giveUp); merr != nil {
				c.logger.ErrorContext(ctx, "recording a failed delivery failed", "outbox_id", it.ID, "error", merr)
			}
			if giveUp {
				c.announceDelivery(ctx, it.ChatID, it.MessageSeq)
			}
			continue
		}
		dctx := withDelivery(ctx, delivery{chatID: it.ChatID, seq: it.MessageSeq, retry: true})
		_, _ = c.attemptDelivery(dctx, src, it, outboundOf(it.Payload), false)
	}
}

// retryAttemptTimeout bounds the attempt a Retry starts. It is the request's no longer: the
// person who pressed Retry is told it was queued, and the attempt runs on whatever the
// browser does next.
const retryAttemptTimeout = time.Minute

// RetryDelivery puts one message's failed deliveries back in the queue and attempts them now,
// rather than at the worker's next poll, so the person who pressed Retry sees it land. It
// returns ErrNothingToRetry when the message has no failed delivery.
func (c *Conductor) RetryDelivery(ctx context.Context, chatID string, seq uint64) error {
	if c.store == nil {
		return errors.New("this conductor has no database")
	}
	n, err := c.store.RetryOutbox(ctx, chatID, seq)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNothingToRetry
	}
	c.announceDelivery(ctx, chatID, seq)
	// Only this message's deliveries, and not on the request: a browser that cancels must
	// not fail the attempt half way (burning an attempt, or leaving the row claimed for the
	// lease), and a Retry must not post other chats' answers on its own clock. The chat hears
	// the outcome through announceDelivery as each attempt ends.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), retryAttemptTimeout)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel()
		items, err := c.store.ClaimOutboxFor(actx, chatID, seq, outboxLease)
		if err != nil {
			c.logger.WarnContext(actx, "claiming a retried delivery failed",
				"chat_id", chatID, "seq", seq, "error", err)
			return
		}
		c.deliverClaimed(actx, items)
	}()
	return nil
}

// ErrNothingToRetry is RetryDelivery on a message with no failed delivery.
var ErrNothingToRetry = errors.New("that message has no failed delivery to retry")

// DeliveryWatcher is told when a message's deliveries change, so the web chat can redraw
// "not delivered" without a reload. The chat broadcaster implements it.
type DeliveryWatcher interface {
	DeliveryChanged(chatID string, seq uint64)
}

func (c *Conductor) announceDelivery(_ context.Context, chatID string, seq uint64) {
	if w, ok := c.mirror.(DeliveryWatcher); ok {
		w.DeliveryChanged(chatID, seq)
	}
}
