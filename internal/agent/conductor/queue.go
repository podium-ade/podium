package conductor

// The turn queue (multiplayer sessions, docs/agent.md).
//
// A session answers one thing at a time. What is put to Podium while it is answering waits
// in turn_queue (0034), in order, and becomes a turn of its own — run as the person who
// asked, with their credentials worked out when the turn STARTS. Before this there was one
// slot (sessionState.next, work_leases.pending) and the latest message overwrote it, which
// was right while a conversation had one person in it and wrong the moment two people tag
// Podium at once: the second person's turn would have run as the first.
//
// One person's run of messages is still one turn on Slack and GitHub. Consecutive rows from the
// same person on the same surface are taken together and the latest is the instruction, exactly
// as before; the earlier ones are in the transcript. Not from the web chat, whose composer
// queues: each message there is a turn of its own, and one sent while Podium was answering
// waits out of the record until its turn starts (InboundEvent.Held).
//
// The protocol across conductors is "write first, then try to serve":
//
//   - accept writes the row, then tries to take the session's lease. A conductor that gets
//     it serves the queue from the head; one that does not has left its row where the
//     owner's loop will find it.
//   - an owner whose loop finds the queue empty lets go of the lease and then looks ONCE
//     more. A row written while it was letting go is either seen by that look, or was
//     written before its writer tried the lease — which it then found free.
//   - a conductor that dies with rows queued leaves them on a session with no live lease;
//     adopt finds those (OrphanedQueues) and serves them.
//
// Injection into a turn that is still up (a warm host turn, a running chat-host turn, a
// task that asked a question or has answered and is waiting) is kept, and only for a message
// from the same person on the same surface: that task runs with its starter's credentials
// and answers where its turn began, so anybody else's message is a turn of its own.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/store"
)

// QueueWatcher is told when the turns waiting in a chat's session change, so every screen
// showing that chat redraws its queue. The web chat implements it.
type QueueWatcher interface {
	QueueChanged(chatID string)
}

// state is the session's in-memory half, made on first use. The caller holds c.mu.
func (c *Conductor) state(sessionID string) *sessionState {
	st := c.sessions[sessionID]
	if st == nil {
		st = &sessionState{}
		c.sessions[sessionID] = st
	}
	return st
}

// askerOf is who a message's turn would run as, and where it would answer: the two things a
// running turn has to share with a message before the message may be injected into it.
func (c *Conductor) askerOf(src Source, ev InboundEvent) (login, kind string) {
	kind = ev.SourceKind
	if src != nil && src.Kind() != "" {
		kind = src.Kind()
	}
	return strings.TrimSpace(ev.Login), kind
}

// sameAsker reports whether ev is from the person, on the surface, whose turn st last ran.
// The caller holds c.mu.
func (c *Conductor) sameAsker(st *sessionState, src Source, ev InboundEvent) bool {
	login, kind := c.askerOf(src, ev)
	return st.turnLogin == login && st.turnKind == kind
}

// setTurnAsker records whose turn a session is running, and of which playbook, for
// sameAsker and for the lease's warm task. Called as each turn starts.
func (c *Conductor) setTurnAsker(sessionID string, src Source, ev InboundEvent, playbook string) {
	login, kind := c.askerOf(src, ev)
	c.mu.Lock()
	st := c.state(sessionID)
	st.turnLogin, st.turnKind, st.turnPlaybook = login, kind, playbook
	c.mu.Unlock()
}

// enqueue writes ev at the back of its session's queue. A database that refuses the write
// does not lose the message: it is kept in this process and served after the queue.
func (c *Conductor) enqueue(ctx context.Context, src Source, sess store.Session, ev InboundEvent) {
	login, kind := c.askerOf(src, ev)
	ev.SourceKind = kind
	raw, err := json.Marshal(ev)
	var row store.QueuedTurn
	if err == nil {
		row, err = c.store.EnqueueTurn(ctx, store.QueuedTurn{
			SessionID:  sess.ID,
			SourceKind: kind,
			Login:      login,
			Author:     ev.Author,
			ChatID:     ev.ChatID,
			MessageSeq: ev.ChatSeq,
			Event:      raw,
		})
	}
	if err != nil {
		c.logger.WarnContext(ctx, "queueing a turn in the database failed; keeping it in this process",
			"session_id", sess.ID, "error", err)
		c.mu.Lock()
		st := c.state(sess.ID)
		st.local = append(st.local, ev)
		c.mu.Unlock()
	} else if ev.Placed != nil {
		// Where it went, read back from the database: the rows queued before it and the
		// turn running, whichever conductor is running it. Exact at the moment it was
		// queued; the queue frame keeps it current from then on.
		if ahead, perr := c.store.QueuePosition(ctx, sess.ID, row.ID); perr == nil {
			placed(ev, ahead)
		}
	}
	c.queueChanged(ctx, sess)
}

// placed tells the sender where its message went, when it asked (InboundEvent.Placed).
func placed(ev InboundEvent, ahead int) {
	if ev.Placed != nil {
		ev.Placed(ahead)
	}
}

// kick serves the session if nobody is: this conductor if it can take the lease, otherwise
// whoever holds it, whose loop finds the queued row.
func (c *Conductor) kick(ctx context.Context, src Source, sess store.Session, j job) {
	c.mu.Lock()
	st := c.state(sess.ID)
	if st.running {
		c.mu.Unlock()
		return
	}
	st.running = true
	c.mu.Unlock()

	key := store.SessionLease(sess.ID)
	if !c.acquire(ctx, key) {
		c.mu.Lock()
		st.running = false
		c.mu.Unlock()
		c.logger.InfoContext(ctx, "another conductor serves this session; the turn is queued for it",
			"session_id", sess.ID)
		return
	}
	next, nsrc, nj, ok := c.popNext(ctx, src, sess, j)
	if !ok {
		// Somebody else took it between our write and our lease, or it was removed — or the
		// queue counts rows nothing can pop right now (a pop that errors, rows locked by a
		// concurrent delete). Let go without the immediate recheck: that recheck would kick
		// again at once and, with rows that keep failing to pop, recurse without end. A retry
		// a little later, backing off, is what a queue that is really there needs.
		c.releaseSession(ctx, sess.ID, key)
		c.retryQueue(ctx, sess.ID)
		return
	}
	c.mu.Lock()
	st.emptyPops = 0
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.serve(ctx, nsrc, sess, nj, next)
	}()
}

// popNext takes the next turn of a session: the head of the queue, and every row after it
// from the same person on the same surface. The source and job are the queued message's own:
// a web message in a mirrored Slack conversation answers in the web chat, whatever the turn
// before it answered.
func (c *Conductor) popNext(
	ctx context.Context, src Source, sess store.Session, j job,
) (InboundEvent, Source, job, bool) {
	for {
		row, ok, err := c.store.PopQueuedTurn(ctx, sess.ID)
		if err != nil {
			c.logger.WarnContext(ctx, "taking the next queued turn failed", "session_id", sess.ID, "error", err)
		}
		if !ok {
			c.mu.Lock()
			st := c.state(sess.ID)
			if len(st.local) == 0 {
				c.mu.Unlock()
				return InboundEvent{}, nil, job{}, false
			}
			ev := st.local[0]
			st.local = st.local[1:]
			c.mu.Unlock()
			c.queueChanged(ctx, sess)
			s, nj := c.sourceAndJob(ev, src, j)
			return ev, s, nj, true
		}
		ev, decoded := c.decodeQueued(ctx, row)
		if !decoded {
			continue
		}
		// A run of one person's messages is one turn — except from the web chat, whose
		// composer queues: there each message was sent to be answered on its own, and a held
		// one dropped here would never reach the record at all.
		for row.SourceKind != SourceChat && !ev.Held {
			more, ok, err := c.store.PopQueuedTurnFrom(ctx, sess.ID, row.Login, row.SourceKind)
			if err != nil || !ok {
				break
			}
			if later, decoded := c.decodeQueued(ctx, more); decoded {
				ev = later
			}
		}
		c.queueChanged(ctx, sess)
		s, nj := c.sourceAndJob(ev, src, j)
		if !c.stillAdmitted(ctx, s, &ev) {
			continue
		}
		return ev, s, nj, true
	}
}

func (c *Conductor) decodeQueued(ctx context.Context, row store.QueuedTurn) (InboundEvent, bool) {
	var ev InboundEvent
	if err := json.Unmarshal(row.Event, &ev); err != nil {
		c.logger.WarnContext(ctx, "a queued turn does not parse; dropping it",
			"session_id", row.SessionID, "queue_id", row.ID, "error", err)
		return InboundEvent{}, false
	}
	if ev.SourceKind == "" {
		ev.SourceKind = row.SourceKind
	}
	return ev, true
}

// sourceAndJob is the source a queued message answers through, and the job its turn runs.
// A conversation's job is the assistant whichever surface asked, or the playbook a Linear
// prompt named for itself (runDirect). Anything else runs the playbook accept chose for it
// (InboundEvent.Playbook), and a message queued without one keeps the job the session was
// already running.
func (c *Conductor) sourceAndJob(ev InboundEvent, src Source, j job) (Source, job) {
	if ev.SourceKind != "" && (src == nil || src.Kind() != ev.SourceKind) {
		if s := c.sourceOf(ev.SourceKind); s != nil {
			src = s
		}
	}
	profile := c.profiles.Current()
	pb, chosen := profile.Playbooks[ev.Playbook]
	chosen = chosen && ev.Playbook != ""
	if src != nil && c.conversational(src, ev) {
		if chosen {
			return src, directJob(pb)
		}
		return src, assistantJob(profile.Assistant())
	}
	if chosen {
		return src, playbookJob(pb)
	}
	return src, j
}

// stillAdmitted works out again, as the turn starts, whether its person may still start it
// (decision 37). Somebody who disconnected GitHub, or whose Slack account no longer proves
// them, while their turn waited is refused now rather than run as who they were.
func (c *Conductor) stillAdmitted(ctx context.Context, src Source, ev *InboundEvent) bool {
	if src == nil || ev.SpeakerProvider == "" || ev.Automatic {
		return true
	}
	was := strings.TrimSpace(ev.Login)
	// Fresh, not from the cache: this is the check that the person who asked is still a
	// Podium account, and a remembered answer from before they left would defeat it.
	c.idents.forget(ev.SpeakerProvider, ev.SpeakerID)
	c.resolveSpeaker(ctx, src, ev)
	if was == "" || strings.TrimSpace(ev.Login) != "" {
		return true
	}
	// They were somebody when they asked and are nobody now. Even a review command is
	// refused rather than re-routed: asked again, it runs as the App like anybody's.
	c.logger.InfoContext(ctx, "a queued turn's person is no longer a Podium account; not running it",
		"source", src.Kind(), "source_key", ev.SourceKey)
	if err := src.React(ctx, ev.Ref, ReactionRefused); err != nil {
		c.logger.WarnContext(ctx, "reacting to a refused queued turn failed", "ref", ev.Ref, "error", err)
	}
	return false
}

// letGo releases a session this conductor holds, and takes it back if a turn was queued
// while it was letting go. It reports nothing: whoever ends up serving the session serves it.
func (c *Conductor) letGo(ctx context.Context, sessionID, key string) {
	c.releaseSession(ctx, sessionID, key)
	c.recheckQueue(ctx, sessionID)
}

// Queue retries after a kick that found nothing to pop: queueRetryBase doubling per attempt,
// queueRetryMax attempts in a row. After that the session waits for the next message, or for
// adoptQueues, rather than for a loop that may never succeed.
const (
	queueRetryBase = 250 * time.Millisecond
	queueRetryMax  = 6
)

// retryQueue looks at a session's queue again later, when it still counts rows. It never
// recurses: the next kick runs on a goroutine of its own after the backoff.
func (c *Conductor) retryQueue(ctx context.Context, sessionID string) {
	if ctx.Err() != nil {
		return
	}
	n, err := c.store.CountQueuedTurns(ctx, sessionID)
	if err != nil || n == 0 {
		return
	}
	c.mu.Lock()
	st := c.state(sessionID)
	st.emptyPops++
	attempt := st.emptyPops
	c.mu.Unlock()
	if attempt > queueRetryMax {
		c.logger.WarnContext(ctx, "a session's queue counts turns that cannot be taken; leaving it for the next message",
			"session_id", sessionID, "queued", n, "attempts", attempt-1)
		c.mu.Lock()
		st.emptyPops = 0
		c.mu.Unlock()
		return
	}
	wait := queueRetryBase << (attempt - 1)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.recheckQueue(ctx, sessionID)
	}()
}

// releaseSession is letGo without the look at the queue afterwards.
func (c *Conductor) releaseSession(ctx context.Context, sessionID, key string) {
	c.mu.Lock()
	st := c.state(sessionID)
	st.running = false
	st.warmTaskID = ""
	st.warmHost = nil
	c.mu.Unlock()
	if handed := c.release(ctx, key); handed != nil {
		// An older conductor left a message on the lease (work_leases.pending). It is a turn
		// like any other: queued, and served by whoever serves the session.
		c.requeueHanded(ctx, sessionID, *handed)
		c.release(ctx, key)
	}
}

// recheckQueue is the look after letting go. A queued row with nobody serving it is this
// conductor's to serve, if it can take the lease again.
func (c *Conductor) recheckQueue(ctx context.Context, sessionID string) {
	if ctx.Err() != nil {
		return
	}
	n, err := c.store.CountQueuedTurns(ctx, sessionID)
	if err != nil || n == 0 {
		return
	}
	sess, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		return
	}
	c.serveQueued(ctx, sess)
}

// serveQueued starts serving a session's queue from nothing in memory: after letting go, and
// for a queue a dead conductor left behind.
func (c *Conductor) serveQueued(ctx context.Context, sess store.Session) {
	src := c.sourceOf(sess.SourceKind)
	j := job{}
	if c.host != nil && src != nil && aConversation(src.Kind()) {
		j = assistantJob(c.profiles.Current().Assistant())
	} else if pb, ok := c.profiles.Current().Playbooks[sess.Playbook]; ok {
		j = playbookJob(pb)
	}
	c.kick(ctx, src, sess, j)
}

// requeueHanded puts a message an older conductor handed over on the lease into the queue.
func (c *Conductor) requeueHanded(ctx context.Context, sessionID string, ev InboundEvent) {
	sess, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		c.logger.WarnContext(ctx, "a handed-over message's session is gone; dropping it",
			"session_id", sessionID, "error", err)
		return
	}
	c.enqueue(ctx, c.sourceOf(ev.SourceKind), sess, ev)
}

// adoptQueues serves every queue a dead conductor left behind.
func (c *Conductor) adoptQueues(ctx context.Context) {
	ids, err := c.store.OrphanedQueues(ctx, c.replicaID)
	if err != nil {
		c.logger.WarnContext(ctx, "looking for orphaned turn queues failed", "error", err)
		return
	}
	for _, id := range ids {
		sess, err := c.store.GetSession(ctx, id)
		if err != nil {
			continue
		}
		c.logger.InfoContext(ctx, "serving a turn queue nobody was serving", "session_id", id)
		c.serveQueued(ctx, sess)
	}
}

// queueChanged tells whoever is watching the session's chat that its queue moved.
func (c *Conductor) queueChanged(ctx context.Context, sess store.Session) {
	w, ok := c.mirror.(QueueWatcher)
	if !ok || sess.SourceKey == "" {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, sess.SourceKey)
	if err != nil {
		return
	}
	w.QueueChanged(chat.ID)
}

// injectIntoOwner gives ev to another conductor's warm task for its session, when that
// conductor serves the session and the task is answering the same person on the same
// surface. It reports whether it did; false means queue it.
func (c *Conductor) injectIntoOwner(ctx context.Context, src Source, sessionID string, ev *InboundEvent) bool {
	if !c.leasing() {
		return false
	}
	warm, ok, err := c.store.OwnerWarm(ctx, store.SessionLease(sessionID), c.replicaID)
	if err != nil || !ok || warm.TaskID == "" {
		return false
	}
	login, kind := c.askerOf(src, *ev)
	if warm.Login != login || warm.Kind != kind {
		return false
	}
	c.recordHeld(ctx, src, ev)
	if err := c.podium.InjectTask(ctx, warm.TaskID, ev.Text); err != nil {
		c.logger.WarnContext(ctx, "injecting into another conductor's warm task failed; queueing the turn",
			"session_id", sessionID, "task_id", warm.TaskID, "error", err)
		return false
	}
	c.logger.InfoContext(ctx, "injected a message into another conductor's warm task",
		"session_id", sessionID, "task_id", warm.TaskID)
	return true
}

// CancelChatTurn stops the turn answering a chat right now, wherever it runs: a host turn is
// a child of whichever conductor started it, so the request is also announced to the others
// (store.TurnCancelChannel), and a task-backed turn's task is cancelled through the control
// plane, which does not care who asks. The turns queued behind it are not touched — the next
// one starts as this one ends.
func (c *Conductor) CancelChatTurn(ctx context.Context, chatID string) (bool, error) {
	turn, ok, err := c.store.RunningChatTurn(ctx, chatID)
	if err != nil || !ok {
		return false, err
	}
	// stopHostTurnSoon, not CancelHostTurn: the turn reads as running here a moment before it
	// registers its stop, and a Stop pressed in that gap must still stop it.
	stopped, deferred := c.stopHostTurnSoon(turn.TriggerRef)
	if deferred && !c.leasing() && turn.TaskID == "" {
		// One conductor, and a host turn that has not registered yet: it can only be ours,
		// and registerHost will stop it.
		stopped = true
	}
	if !stopped && c.leasing() {
		if err := c.store.Notify(ctx, store.TurnCancelChannel, turn.TriggerRef); err != nil {
			c.logger.WarnContext(ctx, "asking the other conductors to stop a turn failed",
				"turn_id", turn.ID, "error", err)
		} else {
			stopped = true
		}
	}
	if turn.TaskID != "" {
		if err := c.podium.CancelTask(ctx, turn.TaskID, "stopped from the chat"); err != nil {
			c.logger.WarnContext(ctx, "cancelling a chat turn's task failed",
				"turn_id", turn.ID, "task_id", turn.TaskID, "error", err)
		} else {
			stopped = true
		}
	}
	return stopped, nil
}

// How long CancelSessionTurn waits for a turn that is starting to become one it can stop,
// and how often it looks. A turn's row is written, and then its task created, within a second
// or two of the turn starting; a Stop pressed in that gap must still stop it.
const (
	stopStartingWait = 15 * time.Second
	stopStartingPoll = 200 * time.Millisecond
)

// CancelSessionTurn stops the turn answering one conversation, named by its source key, and
// drops every turn queued behind it. It reports whether anything was stopped. It is how a
// source whose surface has a Stop of its own — a Linear agent session — stops the work: the
// source holds no conductor and is handed this.
//
// It differs from CancelChatTurn in the queue. Stopping a web chat's turn lets the next queued
// message start; a Stop on a Linear session ends the session's work, and Linear forbids an
// agent to act in a session after it was stopped, so a follow-up queued before the Stop never
// runs.
//
// The turn is stopped as every other turn is: a task through the control plane, which does
// not care which conductor follows it, and a host turn by cancelling it here or asking the
// conductor that runs it. The run following the turn then records it cancelled and posts what
// it posts for any cancelled turn; a source that answers a stop itself refuses those posts.
// The run may be on another conductor, whose source never heard the Stop, so the stop is
// announced to every conductor's sources first (StopHearer), and the cancellation it posts
// carries the control plane's cancel reason (Outbound.CancelReason), which outlives a restart.
// A turn parked on a question is released first, so a reply that arrives as it ends is not
// injected into a task that is going away.
func (c *Conductor) CancelSessionTurn(ctx context.Context, sourceKey, reason string) (bool, error) {
	sess, err := c.store.GetSessionByKey(ctx, sourceKey)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dropped := c.dropQueuedTurns(ctx, sess)
	turn, ok, err := c.stoppableTurn(ctx, sess)
	if err != nil || !ok {
		return dropped > 0, err
	}
	if turn.TaskID == "" {
		if c.CancelHostTurn(turn.TriggerRef) {
			return true, nil
		}
		// No task after the wait, and not a host turn here: a host turn on another
		// conductor, or a turn that is taking far longer than it should to get its task.
		if !c.leasing() {
			return dropped > 0, fmt.Errorf("conductor: turn %s has no task to cancel", turn.ID)
		}
		c.announceStop(ctx, sourceKey, "")
		if err := c.store.Notify(ctx, store.TurnCancelChannel, turn.TriggerRef); err != nil {
			return dropped > 0, fmt.Errorf("conductor: asking the other conductors to stop turn %s: %w", turn.ID, err)
		}
		return true, nil
	}
	c.clearAwaiting(sess.ID, turn.TaskID)
	c.clearWarm(sess.ID, turn.TaskID)
	c.announceStop(ctx, sourceKey, turn.TaskID)
	if err := c.podium.CancelTask(ctx, turn.TaskID, reason); err != nil {
		// FailedPrecondition is a task that is already over: stopped by a Stop before this one,
		// or finishing as it was pressed. Either way nothing of it is running.
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			return dropped > 0, fmt.Errorf("conductor: cancelling turn %s: %w", turn.ID, err)
		}
	}
	c.logger.InfoContext(ctx, "stopped a turn at its conversation's request",
		"session_id", sess.ID, "turn_id", turn.ID, "task_id", turn.TaskID, "dropped_queued", dropped)
	return true, nil
}

// stoppableTurn is the session's running turn, once it is one CancelSessionTurn can stop: it
// has its task, or it is a host turn running here. A turn without either yet is starting, and
// is waited for, as is a session this conductor is serving whose turn row is not written yet.
// ok is false when nothing is running. After stopStartingWait the turn is returned as it is.
func (c *Conductor) stoppableTurn(ctx context.Context, sess store.Session) (store.Turn, bool, error) {
	deadline := time.Now().Add(stopStartingWait)
	for {
		turns, err := c.store.ListTurns(ctx, sess.ID, 1)
		if err != nil {
			return store.Turn{}, false, err
		}
		var turn store.Turn
		running := len(turns) > 0 && turns[0].Status == store.TurnRunning
		if running {
			turn = turns[0]
		}
		c.mu.Lock()
		serving := c.state(sess.ID).running
		_, hostHere := c.hostRuns[turn.TriggerRef]
		c.mu.Unlock()
		switch {
		case running && (turn.TaskID != "" || hostHere):
			return turn, true, nil
		case !running && !serving:
			return store.Turn{}, false, nil
		case time.Now().After(deadline):
			return turn, running, nil
		}
		select {
		case <-ctx.Done():
			return store.Turn{}, false, ctx.Err()
		case <-time.After(stopStartingPoll):
		}
	}
}

// dropQueuedTurns removes every turn waiting in a session, durable and in-process alike, and
// reports how many there were.
func (c *Conductor) dropQueuedTurns(ctx context.Context, sess store.Session) int {
	n := 0
	for {
		_, ok, err := c.store.PopQueuedTurn(ctx, sess.ID)
		if err != nil {
			c.logger.WarnContext(ctx, "dropping a stopped session's queued turns failed",
				"session_id", sess.ID, "error", err)
			break
		}
		if !ok {
			break
		}
		n++
	}
	c.mu.Lock()
	st := c.state(sess.ID)
	n += len(st.local)
	st.local = nil
	c.mu.Unlock()
	if n > 0 {
		c.queueChanged(ctx, sess)
	}
	return n
}

// StopHearer is the optional half of a Source whose surface answers a stop itself: a Linear
// agent session, which is closed with "Stopped." by the source that heard the Stop. A turn of
// that conversation may be running on another conductor, whose source heard nothing, and goes
// on relaying what the cancelled task says until it dies — after "Stopped.", which Linear
// shows as the session working again. Every conductor's source is told here, this one's too,
// since the source that heard the Stop may not know which task was running: it treats the task
// as one it heard stopped. taskID is the task being cancelled, or empty for a host turn.
type StopHearer interface {
	StoppedElsewhere(sourceKey, taskID string)
}

// stopNotice is a SessionStopChannel payload.
type stopNotice struct {
	// From is the conductor that stopped the turn, which told its own sources directly.
	From   string `json:"from"`
	Key    string `json:"key"`
	TaskID string `json:"task_id,omitempty"`
}

// announceStop tells this conductor's sources, and then the other conductors, that a
// conversation's turn is being stopped. It is said before the task is cancelled, so it is
// heard before the task says anything more. A notice that cannot be sent is logged: the task
// is still cancelled, and the cancellation the run posts is still recognised by its cancel
// reason.
func (c *Conductor) announceStop(ctx context.Context, sourceKey, taskID string) {
	c.tellStop(sourceKey, taskID)
	if !c.leasing() {
		return
	}
	payload, err := json.Marshal(stopNotice{From: c.replicaID, Key: sourceKey, TaskID: taskID})
	if err == nil {
		err = c.store.Notify(ctx, store.SessionStopChannel, string(payload))
	}
	if err != nil {
		c.logger.WarnContext(ctx, "telling the other conductors a turn was stopped failed",
			"source_key", sourceKey, "task_id", taskID, "error", err)
	}
}

// listenStops hands another conductor's stop to this conductor's sources that hear one.
func (c *Conductor) listenStops(ctx context.Context) {
	if !c.leasing() {
		return
	}
	_ = c.store.Listen(ctx, store.SessionStopChannel, nil, c.heardStop, func(err error) {
		c.logger.WarnContext(ctx, "the session-stop listener lost its connection; reconnecting", "error", err)
	})
}

// heardStop is a stop another conductor announced. Its own announcement comes back to it too,
// and is not passed on again: announceStop already told its sources.
func (c *Conductor) heardStop(payload string) {
	var n stopNotice
	if json.Unmarshal([]byte(payload), &n) != nil || n.Key == "" || n.From == c.replicaID {
		return
	}
	c.tellStop(n.Key, n.TaskID)
}

// tellStop tells every source of this conductor that hears a stop about one.
func (c *Conductor) tellStop(sourceKey, taskID string) {
	for _, src := range c.sourceList() {
		if h, ok := src.(StopHearer); ok {
			h.StoppedElsewhere(sourceKey, taskID)
		}
	}
}

// listenCancels stops this conductor's host turns that another conductor was asked to stop.
func (c *Conductor) listenCancels(ctx context.Context) {
	if !c.leasing() {
		return
	}
	_ = c.store.Listen(ctx, store.TurnCancelChannel, nil, func(ref string) {
		// stopHostTurnSoon: the turn may be ours and not registered yet. A ref that is not
		// ours is remembered for earlyStopTTL and then forgotten.
		if stopped, _ := c.stopHostTurnSoon(ref); stopped {
			c.logger.InfoContext(ctx, "stopped a host turn another conductor was asked to stop", "ref", ref)
		}
	}, func(err error) {
		c.logger.WarnContext(ctx, "the turn-cancel listener lost its connection; reconnecting", "error", err)
	})
}
