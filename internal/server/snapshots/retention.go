package snapshots

import (
	"context"
	"errors"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

// SweepInterval is how often the retention policy is applied. A snapshot only changes
// when a task exits, so there is nothing to gain from sweeping more often.
const SweepInterval = 10 * time.Minute

// ErrReplaced is returned when a task uploaded a newer snapshot while it was being deleted.
var ErrReplaced = errors.New("snapshots: a newer snapshot was stored while deleting; nothing was deleted")

// Swept is what one sweep deleted.
type Swept struct {
	Removed    int
	FreedBytes int64
}

// List returns every session snapshot, newest first.
func (s *Service) List(ctx context.Context) ([]store.WorkspaceSnapshot, error) {
	return s.store.ListWorkspaceSnapshots(ctx)
}

// Retention returns the policy the sweep applies.
func (s *Service) Retention(ctx context.Context) (store.SnapshotRetention, error) {
	return s.store.GetSnapshotRetention(ctx)
}

// SetRetention replaces the policy and applies it straight away, so the page that saved it
// shows what it deleted rather than waiting for the next tick.
func (s *Service) SetRetention(ctx context.Context, actor string, in store.SnapshotRetention) (store.SnapshotRetention, Swept, error) {
	in.UpdatedBy = actor
	out, err := s.store.SetSnapshotRetention(ctx, in)
	if err != nil {
		return store.SnapshotRetention{}, Swept{}, err
	}
	s.audit(ctx, actor, "snapshot.retention.set", "", map[string]any{
		"max_age_seconds": int64(out.MaxAge / time.Second),
		"max_total_bytes": out.MaxTotalBytes,
	})
	swept, err := s.Sweep(ctx)
	return out, swept, err
}

// Delete removes one session's snapshot. The next task of that session starts from the
// repository base, or from an empty workspace.
func (s *Service) Delete(ctx context.Context, actor, sessionID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	row, err := s.store.GetWorkspaceSnapshot(ctx, sessionID)
	if err != nil {
		return err
	}
	dropped, err := s.drop(ctx, row)
	if err != nil {
		return err
	}
	if !dropped {
		return ErrReplaced
	}
	s.audit(ctx, actor, "snapshot.delete", sessionID, map[string]any{"size_bytes": row.SizeBytes})
	return nil
}

// Sweep deletes the snapshots the policy no longer keeps: older than MaxAge, and, walking
// newest first, every one past the point where the running total exceeds MaxTotalBytes. A
// snapshot whose session has an unfinished task is never deleted, but it still counts
// towards the total.
func (s *Service) Sweep(ctx context.Context) (Swept, error) {
	if !s.Enabled() {
		return Swept{}, nil
	}
	s.sweeping.Lock()
	defer s.sweeping.Unlock()

	policy, err := s.store.GetSnapshotRetention(ctx)
	if err != nil {
		return Swept{}, err
	}
	if policy.MaxAge <= 0 && policy.MaxTotalBytes <= 0 {
		return Swept{}, nil
	}
	rows, err := s.store.ListWorkspaceSnapshots(ctx)
	if err != nil {
		return Swept{}, err
	}

	var out Swept
	var total int64
	over := false
	cutoff := s.now().Add(-policy.MaxAge)
	for _, row := range rows {
		expired := policy.MaxAge > 0 && row.UpdatedAt.Before(cutoff)
		if !expired && !over && policy.MaxTotalBytes > 0 && total+row.SizeBytes > policy.MaxTotalBytes {
			over = true
		}
		if row.InUse || (!expired && !over) {
			total += row.SizeBytes
			continue
		}
		dropped, err := s.drop(ctx, row)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			s.logger.WarnContext(ctx, "deleting a workspace snapshot failed", "session_id", row.SessionID, "error", err)
			continue
		}
		if dropped {
			out.Removed++
			out.FreedBytes += row.SizeBytes
		}
	}
	return out, nil
}

// Run applies the policy every SweepInterval until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	tick := time.NewTicker(SweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			swept, err := s.Sweep(ctx)
			if err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "workspace snapshot sweep failed", "error", err)
			} else if swept.Removed > 0 {
				s.logger.InfoContext(ctx, "deleted workspace snapshots past retention",
					"snapshots", swept.Removed, "freed_bytes", swept.FreedBytes)
			}
		}
	}
}

// drop removes the row first, so nothing can restore from an object that is about to go.
// A false result means an upload replaced the row in between, and both are left alone.
func (s *Service) drop(ctx context.Context, row store.WorkspaceSnapshot) (bool, error) {
	ok, err := s.store.DeleteWorkspaceSnapshot(ctx, row.SessionID, row.ObjectKey)
	if err != nil || !ok {
		return false, err
	}
	s.remove(ctx, row.ObjectKey)
	return true, nil
}

func (s *Service) audit(ctx context.Context, actor, action, subject string, details map[string]any) {
	if err := s.store.Audit(ctx, actor, action, subject, details); err != nil {
		s.logger.WarnContext(ctx, "writing the audit log failed", "action", action, "error", err)
	}
}
