package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// The delivery outbox: every answer the bot owes a surface other than the web chat. See
// 0033_chat_outbox.sql for why a row exists before its first attempt and why delivery is at
// least once.

// Outbox statuses.
const (
	OutboxPending = "pending"
	OutboxSent    = "sent"
	OutboxFailed  = "failed"
)

// OutboxPayload is what a retry hands the source again. It is the conductor's Outbound, flat,
// so this package does not import the conductor's types.
type OutboxPayload struct {
	Type           string `json:"type"`
	Text           string `json:"text"`
	TaskID         string `json:"task_id,omitempty"`
	Playbook       string `json:"playbook,omitempty"`
	SourcePlaybook bool   `json:"source_playbook,omitempty"`
}

// OutboxItem is one delivery: one bot message, to one surface.
type OutboxItem struct {
	ID          string
	ChatID      string
	MessageSeq  uint64
	SurfaceKind string
	SurfaceRef  string
	Payload     OutboxPayload
	Status      string
	Attempts    int
	NextAt      time.Time
	LastError   string
	ExternalID  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func outboxFromRow(r db.ChatOutbox) (OutboxItem, error) {
	var p OutboxPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return OutboxItem{}, fmt.Errorf("outbox %s: decode payload: %w", r.ID, err)
	}
	it := OutboxItem{
		ID:          r.ID,
		ChatID:      r.ChatID,
		MessageSeq:  uint64(r.MessageSeq), //nolint:gosec // seqs are positive
		SurfaceKind: r.SurfaceKind,
		SurfaceRef:  r.SurfaceRef,
		Payload:     p,
		Status:      r.Status,
		Attempts:    int(r.Attempts),
		NextAt:      r.NextAt,
		LastError:   r.LastError,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.ExternalID != nil {
		it.ExternalID = *r.ExternalID
	}
	return it, nil
}

// EnqueueOutbox writes a pending delivery due at nextAt. The inline first attempt passes a
// grace period here, so the row only becomes anybody else's work if this caller dies before
// it reports how the attempt went.
func (s *Store) EnqueueOutbox(
	ctx context.Context, chatID string, seq uint64, kind, ref string, p OutboxPayload, nextAt time.Time,
) (OutboxItem, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return OutboxItem{}, fmt.Errorf("enqueue outbox: encode payload: %w", err)
	}
	row, err := s.q.InsertOutbox(ctx, db.InsertOutboxParams{
		ID:          ids.New("obx"),
		ChatID:      chatID,
		MessageSeq:  int64(seq), //nolint:gosec // seqs are small
		SurfaceKind: kind,
		SurfaceRef:  ref,
		Payload:     payload,
		NextAt:      nextAt.UTC(),
		Now:         time.Now().UTC(),
	})
	if err != nil {
		return OutboxItem{}, fmt.Errorf("enqueue outbox for chat %s seq %d: %w", chatID, seq, err)
	}
	return outboxFromRow(row)
}

// BeginOutboxAttempt counts the inline attempt on a row this caller just enqueued. False
// means the row is no longer pending — it cannot be, short of somebody deleting the chat
// between the two statements — and the caller does not attempt.
func (s *Store) BeginOutboxAttempt(ctx context.Context, id string) (bool, error) {
	n, err := s.q.BeginOutboxAttempt(ctx, db.BeginOutboxAttemptParams{ID: id, Now: time.Now().UTC()})
	if err != nil {
		return false, fmt.Errorf("begin outbox attempt %s: %w", id, err)
	}
	return n == 1, nil
}

// ClaimOutbox takes up to limit due deliveries for this caller and holds each for lease. Two
// conductors calling this together get disjoint rows.
func (s *Store) ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]OutboxItem, error) {
	rows, err := s.q.ClaimOutbox(ctx, db.ClaimOutboxParams{
		LeaseSeconds: lease.Seconds(),
		PageLimit:    int32(limit), //nolint:gosec // a small page
	})
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	out := make([]OutboxItem, 0, len(rows))
	for _, r := range rows {
		it, err := outboxFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// ClaimOutboxFor claims one message's due deliveries, the way ClaimOutbox claims a page of
// everybody's.
func (s *Store) ClaimOutboxFor(ctx context.Context, chatID string, seq uint64, lease time.Duration) ([]OutboxItem, error) {
	rows, err := s.q.ClaimOutboxFor(ctx, db.ClaimOutboxForParams{
		LeaseSeconds: lease.Seconds(),
		ChatID:       chatID,
		MessageSeq:   int64(seq), //nolint:gosec // a chat's message numbers
	})
	if err != nil {
		return nil, fmt.Errorf("claim the deliveries of %s/%d: %w", chatID, seq, err)
	}
	out := make([]OutboxItem, 0, len(rows))
	for _, r := range rows {
		it, err := outboxFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// MarkOutboxSent records a delivery that landed, with the surface's id for it.
func (s *Store) MarkOutboxSent(ctx context.Context, id, externalID string) error {
	if err := s.q.MarkOutboxSent(ctx, db.MarkOutboxSentParams{
		ID: id, ExternalID: externalID, Now: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("mark outbox %s sent: %w", id, err)
	}
	return nil
}

// MarkOutboxRetry records a failed attempt: due again at nextAt, or failed for good.
func (s *Store) MarkOutboxRetry(ctx context.Context, id, lastError string, nextAt time.Time, giveUp bool) error {
	if err := s.q.MarkOutboxRetry(ctx, db.MarkOutboxRetryParams{
		ID: id, LastError: lastError, NextAt: nextAt.UTC(), GiveUp: giveUp, Now: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("mark outbox %s for retry: %w", id, err)
	}
	return nil
}

// RetryOutbox puts one message's failed deliveries back in the queue, due now. It returns how
// many it requeued; zero is a message with nothing failed, which the API reports as not found.
func (s *Store) RetryOutbox(ctx context.Context, chatID string, seq uint64) (int, error) {
	n, err := s.q.RetryOutbox(ctx, db.RetryOutboxParams{
		ChatID: chatID, MessageSeq: int64(seq), Now: time.Now().UTC(), //nolint:gosec // small
	})
	if err != nil {
		return 0, fmt.Errorf("retry outbox for chat %s seq %d: %w", chatID, seq, err)
	}
	return int(n), nil
}

// ListChatOutbox is every delivery one chat has, ordered by message.
func (s *Store) ListChatOutbox(ctx context.Context, chatID string) ([]OutboxItem, error) {
	rows, err := s.q.ListChatOutbox(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list outbox for chat %s: %w", chatID, err)
	}
	out := make([]OutboxItem, 0, len(rows))
	for _, r := range rows {
		it, err := outboxFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// GetOutbox reads one delivery, or ErrNotFound.
func (s *Store) GetOutbox(ctx context.Context, id string) (OutboxItem, error) {
	row, err := s.q.GetOutbox(ctx, id)
	if noRows(err) {
		return OutboxItem{}, fmt.Errorf("%w: outbox %s", ErrNotFound, id)
	}
	if err != nil {
		return OutboxItem{}, fmt.Errorf("get outbox %s: %w", id, err)
	}
	return outboxFromRow(row)
}

// SetChatMessageExternalID records where a bot message landed, if it has no id yet. False is
// a message that already had one, or an id another bot row in the chat already carries; both
// leave the message as it was.
func (s *Store) SetChatMessageExternalID(ctx context.Context, chatID string, seq uint64, externalID string) (bool, error) {
	if externalID == "" {
		return false, nil
	}
	n, err := s.q.SetChatMessageExternalID(ctx, db.SetChatMessageExternalIDParams{
		ChatID: chatID, Seq: int64(seq), ExternalID: &externalID, //nolint:gosec // small
	})
	if isUniqueViolation(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("set external id on chat %s seq %d: %w", chatID, seq, err)
	}
	return n == 1, nil
}
