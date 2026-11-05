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
// One person's run of messages is still one turn. Consecutive rows from the same person on
// the same surface are taken together and the latest is the instruction, exactly as before;
// the earlier ones are in the transcript.
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
	"strings"

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

// setTurnAsker records whose turn a session is running, for sameAsker and for the lease's
// warm task. Called as each turn starts.
func (c *Conductor) setTurnAsker(sessionID string, src Source, ev InboundEvent) {
	login, kind := c.askerOf(src, ev)
	c.mu.Lock()
	st := c.state(sessionID)
	st.turnLogin, st.turnKind = login, kind
	c.mu.Unlock()
}

// enqueue writes ev at the back of its session's queue. A database that refuses the write
// does not lose the message: it is kept in this process and served after the queue.
func (c *Conductor) enqueue(ctx context.Context, src Source, sess store.Session, ev InboundEvent) {
	login, kind := c.askerOf(src, ev)
	ev.SourceKind = kind
	raw, err := json.Marshal(ev)
	if err == nil {
		_, err = c.store.EnqueueTurn(ctx, store.QueuedTurn{
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
	}
	c.queueChanged(ctx, sess)
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
		// Somebody else took it between our write and our lease, or it was removed.
		c.letGo(ctx, sess.ID, key)
		return
	}
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
		for {
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
// A conversation's job is the assistant whichever surface asked; anything else keeps the
// job the session was already running.
func (c *Conductor) sourceAndJob(ev InboundEvent, src Source, j job) (Source, job) {
	if ev.SourceKind != "" && (src == nil || src.Kind() != ev.SourceKind) {
		if s := c.sourceOf(ev.SourceKind); s != nil {
			src = s
		}
	}
	if src != nil && c.host != nil && aConversation(src.Kind()) {
		return src, assistantJob(c.profiles.Current().Assistant())
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
	c.recheckQueue(ctx, sessionID)
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
func (c *Conductor) injectIntoOwner(ctx context.Context, src Source, sessionID string, ev InboundEvent) bool {
	if !c.leasing() {
		return false
	}
	warm, ok, err := c.store.OwnerWarm(ctx, store.SessionLease(sessionID), c.replicaID)
	if err != nil || !ok || warm.TaskID == "" {
		return false
	}
	login, kind := c.askerOf(src, ev)
	if warm.Login != login || warm.Kind != kind {
		return false
	}
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
	stopped := c.CancelHostTurn(turn.TriggerRef)
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

// listenCancels stops this conductor's host turns that another conductor was asked to stop.
func (c *Conductor) listenCancels(ctx context.Context) {
	if !c.leasing() {
		return
	}
	_ = c.store.Listen(ctx, store.TurnCancelChannel, nil, func(ref string) {
		if c.CancelHostTurn(ref) {
			c.logger.InfoContext(ctx, "stopped a host turn another conductor was asked to stop", "ref", ref)
		}
	}, func(err error) {
		c.logger.WarnContext(ctx, "the turn-cancel listener lost its connection; reconnecting", "error", err)
	})
}
