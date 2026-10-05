package store

import (
	"context"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// WorkspaceSnapshot is one stored workspace tar. SessionID is set for a conversation.
// Repo is set for a repository base. The other is empty.
type WorkspaceSnapshot struct {
	SessionID string
	Repo      string
	ObjectKey string
	SizeBytes int64
	SHA256    string
	NodeID    string
	TaskID    string
	UpdatedAt time.Time
}

// PutWorkspaceSnapshot records a session's latest snapshot and returns the object key it
// replaced. The previous key is empty when this is the first snapshot. The caller deletes
// that object only after this returns, so a failed write leaves the old one in place.
func (s *Store) PutWorkspaceSnapshot(ctx context.Context, in WorkspaceSnapshot) (previousKey string, err error) {
	if in.SessionID == "" || in.ObjectKey == "" {
		return "", fmt.Errorf("put workspace snapshot: session_id and object_key are required")
	}
	prev, err := s.q.GetWorkspaceSnapshot(ctx, in.SessionID)
	if err != nil && !noRows(err) {
		return "", fmt.Errorf("put workspace snapshot %s: %w", in.SessionID, err)
	}
	if err == nil {
		previousKey = prev.ObjectKey
	}
	if _, err := s.q.UpsertWorkspaceSnapshot(ctx, db.UpsertWorkspaceSnapshotParams{
		SessionID: in.SessionID,
		ObjectKey: in.ObjectKey,
		SizeBytes: in.SizeBytes,
		Sha256:    in.SHA256,
		NodeID:    in.NodeID,
		TaskID:    in.TaskID,
	}); err != nil {
		return "", fmt.Errorf("put workspace snapshot %s: %w", in.SessionID, err)
	}
	if previousKey == in.ObjectKey {
		previousKey = ""
	}
	return previousKey, nil
}

// GetWorkspaceSnapshot reads a session's latest snapshot.
func (s *Store) GetWorkspaceSnapshot(ctx context.Context, sessionID string) (WorkspaceSnapshot, error) {
	row, err := s.q.GetWorkspaceSnapshot(ctx, sessionID)
	if noRows(err) {
		return WorkspaceSnapshot{}, fmt.Errorf("%w: workspace snapshot %s", ErrNotFound, sessionID)
	}
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("get workspace snapshot %s: %w", sessionID, err)
	}
	return WorkspaceSnapshot{
		SessionID: row.SessionID,
		ObjectKey: row.ObjectKey,
		SizeBytes: row.SizeBytes,
		SHA256:    row.Sha256,
		NodeID:    row.NodeID,
		TaskID:    row.TaskID,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// PutWorkspaceBase records a repository's base snapshot and returns the object key it replaced.
func (s *Store) PutWorkspaceBase(ctx context.Context, in WorkspaceSnapshot) (previousKey string, err error) {
	if in.Repo == "" || in.ObjectKey == "" {
		return "", fmt.Errorf("put workspace base: repo and object_key are required")
	}
	prev, err := s.q.GetWorkspaceBase(ctx, in.Repo)
	if err != nil && !noRows(err) {
		return "", fmt.Errorf("put workspace base %s: %w", in.Repo, err)
	}
	if err == nil {
		previousKey = prev.ObjectKey
	}
	if _, err := s.q.UpsertWorkspaceBase(ctx, db.UpsertWorkspaceBaseParams{
		Repo:      in.Repo,
		ObjectKey: in.ObjectKey,
		SizeBytes: in.SizeBytes,
		Sha256:    in.SHA256,
	}); err != nil {
		return "", fmt.Errorf("put workspace base %s: %w", in.Repo, err)
	}
	if previousKey == in.ObjectKey {
		previousKey = ""
	}
	return previousKey, nil
}

// GetWorkspaceBase reads a repository's base snapshot.
func (s *Store) GetWorkspaceBase(ctx context.Context, repo string) (WorkspaceSnapshot, error) {
	row, err := s.q.GetWorkspaceBase(ctx, repo)
	if noRows(err) {
		return WorkspaceSnapshot{}, fmt.Errorf("%w: workspace base %s", ErrNotFound, repo)
	}
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("get workspace base %s: %w", repo, err)
	}
	return WorkspaceSnapshot{
		Repo:      row.Repo,
		ObjectKey: row.ObjectKey,
		SizeBytes: row.SizeBytes,
		SHA256:    row.Sha256,
		UpdatedAt: row.UpdatedAt,
	}, nil
}
