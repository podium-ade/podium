package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// MaxSlackChannelDescriptionRunes caps an operator note so it cannot eat the turn brief.
const MaxSlackChannelDescriptionRunes = 500

// SlackChannel is one Slack channel this conductor has seen, and the note a turn of it is
// briefed with.
type SlackChannel struct {
	ID          string
	Name        string
	Description string
	UpdatedAt   time.Time
}

// ListSlackChannels returns every known channel, named ones first (empty names sort last
// only by id when names match).
func (s *Store) ListSlackChannels(ctx context.Context) ([]SlackChannel, error) {
	rows, err := s.q.ListSlackChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list slack channels: %w", err)
	}
	out := make([]SlackChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, slackChannelFromRow(r))
	}
	return out, nil
}

// SlackChannel reads one. ErrNotFound means this conductor has never seen it.
func (s *Store) SlackChannel(ctx context.Context, id string) (SlackChannel, error) {
	if id == "" {
		return SlackChannel{}, fmt.Errorf("%w: slack channel", ErrNotFound)
	}
	row, err := s.q.GetSlackChannel(ctx, id)
	if noRows(err) {
		return SlackChannel{}, fmt.Errorf("%w: slack channel %s", ErrNotFound, id)
	}
	if err != nil {
		return SlackChannel{}, fmt.Errorf("get slack channel %s: %w", id, err)
	}
	return slackChannelFromRow(row), nil
}

// UpsertSlackChannelName records the name Slack last gave this channel. An empty name on
// an existing row is ignored so a failed lookup cannot blank a name we already had.
func (s *Store) UpsertSlackChannelName(ctx context.Context, id, name string) error {
	if id == "" {
		return fmt.Errorf("upsert slack channel: an id is required")
	}
	if err := s.q.UpsertSlackChannelName(ctx, db.UpsertSlackChannelNameParams{
		ID:        id,
		Name:      name,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("upsert slack channel %s: %w", id, err)
	}
	return nil
}

// SetSlackChannelDescription stores the operator note a turn of this channel is briefed
// with. Empty is valid. ErrNotFound means the channel is not in the catalogue yet.
func (s *Store) SetSlackChannelDescription(ctx context.Context, id, description string) (SlackChannel, error) {
	if id == "" {
		return SlackChannel{}, fmt.Errorf("set slack channel description: an id is required")
	}
	description = strings.TrimSpace(description)
	if n := utf8.RuneCountInString(description); n > MaxSlackChannelDescriptionRunes {
		return SlackChannel{}, fmt.Errorf("%w: %d runes is more than the %d-rune limit",
			ErrInvalidSlackChannelDescription, n, MaxSlackChannelDescriptionRunes)
	}
	n, err := s.q.SetSlackChannelDescription(ctx, db.SetSlackChannelDescriptionParams{
		ID:          id,
		Description: description,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return SlackChannel{}, fmt.Errorf("set slack channel %s description: %w", id, err)
	}
	if n == 0 {
		return SlackChannel{}, fmt.Errorf("%w: slack channel %s", ErrNotFound, id)
	}
	return s.SlackChannel(ctx, id)
}

// SetChatChannel records the Slack channel name a mirrored chat should show. Empty is
// valid and means "not resolved yet".
func (s *Store) SetChatChannel(ctx context.Context, id, channel string) error {
	if id == "" {
		return fmt.Errorf("set chat channel: an id is required")
	}
	if err := s.q.SetChatChannel(ctx, db.SetChatChannelParams{ID: id, Channel: channel}); err != nil {
		return fmt.Errorf("set channel of chat %s: %w", id, err)
	}
	return nil
}

// SlackChannelID is the C…/G…/D… inside a slack:<channel>:<thread> source key.
func SlackChannelID(sourceKey string) (string, bool) {
	kind, rest, ok := strings.Cut(sourceKey, ":")
	if !ok || kind != "slack" {
		return "", false
	}
	id, _, ok := strings.Cut(rest, ":")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func slackChannelFromRow(r db.SlackChannel) SlackChannel {
	return SlackChannel{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		UpdatedAt:   r.UpdatedAt.UTC(),
	}
}
