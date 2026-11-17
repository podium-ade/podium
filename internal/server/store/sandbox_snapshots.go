package store

import (
	"context"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// SandboxSnapshot is a session's workspace kept by a sandbox provider as an image of its
// own. ImageID is the provider's ID for it.
type SandboxSnapshot struct {
	SessionID string
	Provider  string
	ImageID   string
	NodeID    string
	TaskID    string
	UpdatedAt time.Time
	// InUse is set by ListSandboxSnapshots: a task of the session has not finished.
	InUse bool
}

// RetiredSandboxSnapshot is a provider image waiting to be deleted.
type RetiredSandboxSnapshot struct {
	ImageID   string
	Provider  string
	Attempts  int
	LastError string
	RetiredAt time.Time
}

// SandboxSnapshotRetention is when the sweep deletes a provider image. Zero turns a rule off.
type SandboxSnapshotRetention struct {
	MaxAge    time.Duration
	MaxCount  int64
	UpdatedBy string
	UpdatedAt time.Time
}

func sandboxSnapshotFromRow(r db.SandboxSnapshot) SandboxSnapshot {
	return SandboxSnapshot{
		SessionID: r.SessionID, Provider: r.Provider, ImageID: r.ImageID,
		NodeID: r.NodeID, TaskID: r.TaskID, UpdatedAt: r.UpdatedAt,
	}
}

// PutSandboxSnapshot records a session's image. The image it replaces, if any and not the
// same one, is retired in the same transaction, so it is deleted even if the caller dies
// right after.
func (s *Store) PutSandboxSnapshot(ctx context.Context, in SandboxSnapshot) error {
	if in.SessionID == "" || in.Provider == "" || in.ImageID == "" {
		return fmt.Errorf("put sandbox snapshot: session_id, provider and image_id are required")
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		prev, err := q.LockSandboxSnapshot(ctx, in.SessionID)
		if err != nil && !noRows(err) {
			return fmt.Errorf("put sandbox snapshot %s: %w", in.SessionID, err)
		}
		if _, err := q.UpsertSandboxSnapshot(ctx, db.UpsertSandboxSnapshotParams{
			SessionID: in.SessionID, Provider: in.Provider, ImageID: in.ImageID,
			NodeID: in.NodeID, TaskID: in.TaskID,
		}); err != nil {
			return fmt.Errorf("put sandbox snapshot %s: %w", in.SessionID, err)
		}
		if prev.ImageID != "" && prev.ImageID != in.ImageID {
			if err := q.RetireSandboxSnapshot(ctx, db.RetireSandboxSnapshotParams{
				ImageID: prev.ImageID, Provider: prev.Provider,
			}); err != nil {
				return fmt.Errorf("put sandbox snapshot %s: retire %s: %w", in.SessionID, prev.ImageID, err)
			}
		}
		return nil
	})
}

// GetSandboxSnapshot reads a session's image; ErrNotFound when it has none.
func (s *Store) GetSandboxSnapshot(ctx context.Context, sessionID string) (SandboxSnapshot, error) {
	row, err := s.q.GetSandboxSnapshot(ctx, sessionID)
	if noRows(err) {
		return SandboxSnapshot{}, fmt.Errorf("%w: sandbox snapshot %s", ErrNotFound, sessionID)
	}
	if err != nil {
		return SandboxSnapshot{}, fmt.Errorf("get sandbox snapshot %s: %w", sessionID, err)
	}
	return sandboxSnapshotFromRow(row), nil
}

// ListSandboxSnapshots returns every session's image, newest first.
func (s *Store) ListSandboxSnapshots(ctx context.Context) ([]SandboxSnapshot, error) {
	rows, err := s.q.ListSandboxSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sandbox snapshots: %w", err)
	}
	out := make([]SandboxSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, SandboxSnapshot{
			SessionID: r.SessionID, Provider: r.Provider, ImageID: r.ImageID,
			NodeID: r.NodeID, TaskID: r.TaskID, UpdatedAt: r.UpdatedAt, InUse: r.InUse,
		})
	}
	return out, nil
}

// DropSandboxSnapshot removes a session's row if it still points at imageID and retires
// the image, in one transaction. retire false forgets an image the provider no longer has.
// It reports whether the row was removed.
func (s *Store) DropSandboxSnapshot(ctx context.Context, row SandboxSnapshot, retire bool) (bool, error) {
	var dropped bool
	err := s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteSandboxSnapshot(ctx, db.DeleteSandboxSnapshotParams{SessionID: row.SessionID, ImageID: row.ImageID})
		if err != nil {
			return fmt.Errorf("delete sandbox snapshot %s: %w", row.SessionID, err)
		}
		dropped = n > 0
		if !dropped || !retire {
			return nil
		}
		if err := q.RetireSandboxSnapshot(ctx, db.RetireSandboxSnapshotParams{ImageID: row.ImageID, Provider: row.Provider}); err != nil {
			return fmt.Errorf("retire sandbox snapshot %s: %w", row.ImageID, err)
		}
		return nil
	})
	return dropped, err
}

// ListRetiredSandboxSnapshots is every image waiting to be deleted, oldest first.
func (s *Store) ListRetiredSandboxSnapshots(ctx context.Context) ([]RetiredSandboxSnapshot, error) {
	rows, err := s.q.ListRetiredSandboxSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("list retired sandbox snapshots: %w", err)
	}
	out := make([]RetiredSandboxSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, RetiredSandboxSnapshot{
			ImageID: r.ImageID, Provider: r.Provider, Attempts: int(r.Attempts),
			LastError: r.LastError, RetiredAt: r.RetiredAt,
		})
	}
	return out, nil
}

// ForgetRetiredSandboxSnapshot drops an image the provider has deleted.
func (s *Store) ForgetRetiredSandboxSnapshot(ctx context.Context, imageID string) error {
	if err := s.q.DeleteRetiredSandboxSnapshot(ctx, imageID); err != nil {
		return fmt.Errorf("forget retired sandbox snapshot %s: %w", imageID, err)
	}
	return nil
}

// FailRetiredSandboxSnapshot records a failed delete, to be tried again.
func (s *Store) FailRetiredSandboxSnapshot(ctx context.Context, imageID, msg string) error {
	if err := s.q.FailRetiredSandboxSnapshot(ctx, db.FailRetiredSandboxSnapshotParams{ImageID: imageID, LastError: msg}); err != nil {
		return fmt.Errorf("fail retired sandbox snapshot %s: %w", imageID, err)
	}
	return nil
}

// GetSandboxSnapshotRetention reads the policy; the zero value keeps everything.
func (s *Store) GetSandboxSnapshotRetention(ctx context.Context) (SandboxSnapshotRetention, error) {
	row, err := s.q.GetSandboxSnapshotRetention(ctx)
	if noRows(err) {
		return SandboxSnapshotRetention{}, nil
	}
	if err != nil {
		return SandboxSnapshotRetention{}, fmt.Errorf("get sandbox snapshot retention: %w", err)
	}
	return sandboxRetentionFromRow(row), nil
}

// SetSandboxSnapshotRetention replaces the policy.
func (s *Store) SetSandboxSnapshotRetention(ctx context.Context, in SandboxSnapshotRetention) (SandboxSnapshotRetention, error) {
	if in.MaxAge < 0 || in.MaxCount < 0 {
		return SandboxSnapshotRetention{}, fmt.Errorf("set sandbox snapshot retention: limits cannot be negative")
	}
	row, err := s.q.SetSandboxSnapshotRetention(ctx, db.SetSandboxSnapshotRetentionParams{
		MaxAgeSeconds: int64(in.MaxAge / time.Second),
		MaxCount:      in.MaxCount,
		UpdatedBy:     in.UpdatedBy,
	})
	if err != nil {
		return SandboxSnapshotRetention{}, fmt.Errorf("set sandbox snapshot retention: %w", err)
	}
	return sandboxRetentionFromRow(row), nil
}

func sandboxRetentionFromRow(r db.SandboxSnapshotRetention) SandboxSnapshotRetention {
	return SandboxSnapshotRetention{
		MaxAge:    time.Duration(r.MaxAgeSeconds) * time.Second,
		MaxCount:  r.MaxCount,
		UpdatedBy: r.UpdatedBy,
		UpdatedAt: r.UpdatedAt,
	}
}
