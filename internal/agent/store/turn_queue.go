package store

import (
	"context"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// QueuedTurn is one message waiting for its own turn in a session that was already answering
// something (0034). Event is the conductor's InboundEvent, encoded by the conductor: the store
// does not know its shape and does not need to.
type QueuedTurn struct {
	ID         int64
	SessionID  string
	SourceKind string
	// Login is who asked, and Author how they are shown. Empty Login is a turn nobody in
	// Podium is behind (a review command from somebody who has not linked GitHub).
	Login  string
	Author string
	// ChatID and MessageSeq are the message that asked, when it is in a chat's record.
	ChatID     string
	MessageSeq uint64
	Event      []byte
	CreatedAt  time.Time
}

func queuedTurnFromRow(r db.TurnQueue) QueuedTurn {
	return QueuedTurn{
		ID:         r.ID,
		SessionID:  r.SessionID,
		SourceKind: r.SourceKind,
		Login:      r.Login,
		Author:     r.Author,
		ChatID:     r.ChatID,
		MessageSeq: uint64(r.MessageSeq),
		Event:      r.Event,
		CreatedAt:  r.CreatedAt.UTC(),
	}
}

// EnqueueTurn puts one message at the back of its session's queue.
func (s *Store) EnqueueTurn(ctx context.Context, q QueuedTurn) (QueuedTurn, error) {
	row, err := s.q.EnqueueTurn(ctx, db.EnqueueTurnParams{
		SessionID:  q.SessionID,
		SourceKind: q.SourceKind,
		Login:      q.Login,
		Author:     q.Author,
		ChatID:     q.ChatID,
		MessageSeq: int64(q.MessageSeq),
		Event:      q.Event,
	})
	if err != nil {
		return QueuedTurn{}, fmt.Errorf("queue a turn for session %s: %w", q.SessionID, err)
	}
	return queuedTurnFromRow(row), nil
}

// PopQueuedTurn takes the oldest queued turn of a session. ok is false when there is none.
func (s *Store) PopQueuedTurn(ctx context.Context, sessionID string) (QueuedTurn, bool, error) {
	row, err := s.q.PopQueuedTurn(ctx, sessionID)
	if noRows(err) {
		return QueuedTurn{}, false, nil
	}
	if err != nil {
		return QueuedTurn{}, false, fmt.Errorf("take the next queued turn of session %s: %w", sessionID, err)
	}
	return queuedTurnFromRow(row), true, nil
}

// PopQueuedTurnFrom takes the oldest queued turn of a session only when login asked it on
// sourceKind. ok is false otherwise, and nothing is taken.
func (s *Store) PopQueuedTurnFrom(ctx context.Context, sessionID, login, sourceKind string) (QueuedTurn, bool, error) {
	row, err := s.q.PopQueuedTurnFrom(ctx, db.PopQueuedTurnFromParams{
		SessionID: sessionID, Login: login, SourceKind: sourceKind,
	})
	if noRows(err) {
		return QueuedTurn{}, false, nil
	}
	if err != nil {
		return QueuedTurn{}, false, fmt.Errorf("take the next queued turn of session %s: %w", sessionID, err)
	}
	return queuedTurnFromRow(row), true, nil
}

// CountQueuedTurns is how many turns wait in a session.
func (s *Store) CountQueuedTurns(ctx context.Context, sessionID string) (int, error) {
	n, err := s.q.CountQueuedTurns(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("count the queued turns of session %s: %w", sessionID, err)
	}
	return int(n), nil
}

// ListQueuedTurnsForChat is the turns waiting in the session a chat is the record of,
// oldest first.
func (s *Store) ListQueuedTurnsForChat(ctx context.Context, chatID string) ([]QueuedTurn, error) {
	rows, err := s.q.ListQueuedTurnsForChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list the queued turns of chat %s: %w", chatID, err)
	}
	out := make([]QueuedTurn, 0, len(rows))
	for _, r := range rows {
		out = append(out, queuedTurnFromRow(r))
	}
	return out, nil
}

// RemoveQueuedTurn takes back login's queued turn for one message. ErrNotFound means it is
// not queued (any more), or is not theirs; the two are one answer.
func (s *Store) RemoveQueuedTurn(ctx context.Context, chatID string, seq uint64, login string) error {
	n, err := s.q.RemoveQueuedTurn(ctx, db.RemoveQueuedTurnParams{
		ChatID: chatID, MessageSeq: int64(seq), Login: login,
	})
	if err != nil {
		return fmt.Errorf("remove the queued turn of %s/%d: %w", chatID, seq, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: no queued turn of yours for %s/%d", ErrNotFound, chatID, seq)
	}
	return nil
}

// RemoveQueuedTurnByID takes back login's queued turn by its row. ErrNotFound means it is not
// queued (any more), is not in this chat, or is not theirs; the three are one answer.
func (s *Store) RemoveQueuedTurnByID(ctx context.Context, chatID string, id int64, login string) error {
	n, err := s.q.RemoveQueuedTurnByID(ctx, db.RemoveQueuedTurnByIDParams{ID: id, ChatID: chatID, Login: login})
	if err != nil {
		return fmt.Errorf("remove queued turn %d of %s: %w", id, chatID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: no queued turn %d of yours in %s", ErrNotFound, id, chatID)
	}
	return nil
}

// OrphanedQueues is every session with turns waiting and no live conductor other than owner
// serving it.
func (s *Store) OrphanedQueues(ctx context.Context, owner string) ([]string, error) {
	ids, err := s.q.OrphanedQueues(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list orphaned turn queues: %w", err)
	}
	return ids, nil
}

// WarmTask is a session owner's task that has answered and is waiting for a follow-up, and
// whose turn it was.
type WarmTask struct {
	TaskID string
	Login  string
	Kind   string
}

// OwnerWarm is the warm task of the live conductor other than caller that serves key. ok is
// false when no other conductor serves it; TaskID is empty when it serves it with nothing
// warm.
func (s *Store) OwnerWarm(ctx context.Context, key, caller string) (WarmTask, bool, error) {
	row, err := s.q.OwnerWarm(ctx, db.OwnerWarmParams{Key: key, Caller: caller})
	if noRows(err) {
		return WarmTask{}, false, nil
	}
	if err != nil {
		return WarmTask{}, false, fmt.Errorf("read the owner of %s: %w", key, err)
	}
	return WarmTask{TaskID: row.WarmTaskID, Login: row.WarmLogin, Kind: row.WarmKind}, true, nil
}

// MarkChatMultiplayer makes a chat multiplayer for good. force is a share or a mirrored
// conversation; without it the chat must have a second Podium participant. changed is true
// only for the call that flipped it, which is the one that announces it.
func (s *Store) MarkChatMultiplayer(ctx context.Context, chatID string, force bool) (Chat, bool, error) {
	row, err := s.q.MarkChatMultiplayer(ctx, db.MarkChatMultiplayerParams{ID: chatID, Force: force})
	if noRows(err) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, fmt.Errorf("mark chat %s multiplayer: %w", chatID, err)
	}
	// Only the call that flipped it names the earlier messages, and a message sent after the
	// flip already carries its author, so this cannot race a send into a wrong name.
	if err := s.q.NameWebAuthors(ctx, chatID); err != nil {
		return Chat{}, false, fmt.Errorf("name the earlier messages of chat %s: %w", chatID, err)
	}
	return chatFromRow(row), true, nil
}

// QueuePosition is how many turns run before the queued turn id: those queued ahead of it in
// its session, and the one running there, if any.
func (s *Store) QueuePosition(ctx context.Context, sessionID string, id int64) (int, error) {
	n, err := s.q.QueuePosition(ctx, db.QueuePositionParams{SessionID: sessionID, ID: id})
	if err != nil {
		return 0, fmt.Errorf("read the queue position of turn %d in session %s: %w", id, sessionID, err)
	}
	return int(n), nil
}
