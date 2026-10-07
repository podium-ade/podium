package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// Preview is one row of the previews table: a task's environment kept up on its node after
// its command exited.
type Preview struct {
	TaskID  string
	NodeID  string
	Via     string
	Address string
	URLs    map[string]string
	TTL     time.Duration
	// ExpiresAt is nil while the command still runs: the ttl counts from its exit.
	ExpiresAt     *time.Time
	ReleasedAt    *time.Time
	ReleaseReason string
	CreatedAt     time.Time
}

// Live reports whether the preview has not been released.
func (p Preview) Live() bool { return p.ReleasedAt == nil }

// NewPreview is what a node reports when it has published a task's ports.
type NewPreview struct {
	TaskID  string
	NodeID  string
	Via     string
	Address string
	URLs    map[string]string
	TTL     time.Duration
}

// UpsertPreview records where a task's ports were published, replacing whatever an earlier
// attempt of the same task left.
func (s *Store) UpsertPreview(ctx context.Context, in NewPreview) (Preview, error) {
	urls, err := json.Marshal(in.URLs)
	if err != nil {
		return Preview{}, fmt.Errorf("preview urls for %s: %w", in.TaskID, err)
	}
	row, err := s.q.UpsertPreview(ctx, db.UpsertPreviewParams{
		TaskID:  in.TaskID,
		NodeID:  in.NodeID,
		Via:     in.Via,
		Address: in.Address,
		Urls:    urls,
		TtlMs:   in.TTL.Milliseconds(),
	})
	if err != nil {
		return Preview{}, fmt.Errorf("upsert preview for %s: %w", in.TaskID, err)
	}
	return previewOf(row), nil
}

// GetPreview reads a task's preview. A task that never had one is ErrNotFound.
func (s *Store) GetPreview(ctx context.Context, taskID string) (Preview, error) {
	row, err := s.q.GetPreview(ctx, taskID)
	if err != nil {
		if noRows(err) {
			return Preview{}, fmt.Errorf("preview of %s: %w", taskID, ErrNotFound)
		}
		return Preview{}, fmt.Errorf("get preview of %s: %w", taskID, err)
	}
	return previewOf(row), nil
}

// ListPreviews reads the previews of the given tasks, keyed by task. Tasks without one are
// simply absent.
func (s *Store) ListPreviews(ctx context.Context, taskIDs []string) (map[string]Preview, error) {
	out := make(map[string]Preview, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.ListPreviews(ctx, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("list previews: %w", err)
	}
	for _, r := range rows {
		out[r.TaskID] = previewOf(r)
	}
	return out, nil
}

// HoldPreview starts a live preview's ttl at the moment its command exited. It reports
// false for a task with no live preview, or one whose ttl is already running.
func (s *Store) HoldPreview(ctx context.Context, taskID string, exitedAt time.Time) (Preview, bool, error) {
	row, err := s.q.HoldPreview(ctx, db.HoldPreviewParams{TaskID: taskID, ExitedAt: exitedAt})
	if err != nil {
		if noRows(err) {
			return Preview{}, false, nil
		}
		return Preview{}, false, fmt.Errorf("hold preview of %s: %w", taskID, err)
	}
	return previewOf(row), true, nil
}

// ReleasePreview marks a live preview gone and says why. It reports false when there was
// nothing live to release, which callers treat as success: releasing is idempotent.
func (s *Store) ReleasePreview(ctx context.Context, taskID, reason string) (Preview, bool, error) {
	row, err := s.q.ReleasePreview(ctx, db.ReleasePreviewParams{TaskID: taskID, ReleaseReason: reason})
	if err != nil {
		if noRows(err) {
			return Preview{}, false, nil
		}
		return Preview{}, false, fmt.Errorf("release preview of %s: %w", taskID, err)
	}
	return previewOf(row), true, nil
}

// OrphanedPreviews is every live preview whose task is over without having been held.
func (s *Store) OrphanedPreviews(ctx context.Context) ([]Preview, error) {
	rows, err := s.q.OrphanedPreviews(ctx)
	if err != nil {
		return nil, fmt.Errorf("orphaned previews: %w", err)
	}
	out := make([]Preview, 0, len(rows))
	for _, r := range rows {
		out = append(out, previewOf(r))
	}
	return out, nil
}

// ExpiredPreviews is every live preview whose ttl ran out by now, oldest first.
func (s *Store) ExpiredPreviews(ctx context.Context, now time.Time) ([]Preview, error) {
	rows, err := s.q.ExpiredPreviews(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("expired previews: %w", err)
	}
	out := make([]Preview, 0, len(rows))
	for _, r := range rows {
		out = append(out, previewOf(r))
	}
	return out, nil
}

func previewOf(r db.Preview) Preview {
	p := Preview{
		TaskID:        r.TaskID,
		NodeID:        r.NodeID,
		Via:           r.Via,
		Address:       r.Address,
		TTL:           time.Duration(r.TtlMs) * time.Millisecond,
		ExpiresAt:     r.ExpiresAt,
		ReleasedAt:    r.ReleasedAt,
		ReleaseReason: r.ReleaseReason,
		CreatedAt:     r.CreatedAt,
	}
	_ = json.Unmarshal(r.Urls, &p.URLs)
	return p
}
