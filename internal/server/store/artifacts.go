package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/internal/server/store/db"
)

// Artifact kinds, as stored in artifacts.kind.
const (
	// ArtifactKindFile is a file the task produced.
	ArtifactKindFile = "file"
	// ArtifactKindLog is a rolled-up log stream: the task's own stdout or stderr, or one
	// sidecar's, moved out of task_log_chunks and into the object store.
	ArtifactKindLog = "log"
)

// Artifact is one row of the artifacts table.
type Artifact struct {
	ID          string
	TaskID      string
	Kind        string
	Name        string
	ObjectKey   string
	SizeBytes   int64
	ContentType string
	SHA256      string
	CreatedAt   time.Time
}

// NewArtifact is the input to CreateArtifact. An empty ID is minted and an empty Kind
// defaults to file.
type NewArtifact struct {
	ID          string
	TaskID      string
	Kind        string
	Name        string
	ObjectKey   string
	SizeBytes   int64
	ContentType string
	SHA256      string
}

// LogRollUp is what a task's roll-up left behind: when it happened and the high-water
// marks that were true at the time. The marks are what keep MaxTaskSeq and
// TaskStreamOffsets monotonic once the chunks they were derived from are pruned.
type LogRollUp struct {
	At           *time.Time
	HighSeq      uint64
	StdoutOffset int64
	StderrOffset int64
}

// CreateArtifact records one stored object.
func (s *Store) CreateArtifact(ctx context.Context, in NewArtifact) (Artifact, error) {
	if in.ID == "" {
		in.ID = ids.NewArtifact()
	}
	if in.Kind == "" {
		in.Kind = ArtifactKindFile
	}
	row, err := s.q.CreateArtifact(ctx, db.CreateArtifactParams{
		ID:          in.ID,
		TaskID:      in.TaskID,
		Kind:        in.Kind,
		Name:        in.Name,
		ObjectKey:   in.ObjectKey,
		SizeBytes:   in.SizeBytes,
		ContentType: in.ContentType,
		Sha256:      in.SHA256,
	})
	if err != nil {
		return Artifact{}, fmt.Errorf("create artifact for task %s: %w", in.TaskID, err)
	}
	return artifactOf(row), nil
}

// ListArtifacts returns a task's artifacts oldest first.
func (s *Store) ListArtifacts(ctx context.Context, taskID string) ([]Artifact, error) {
	rows, err := s.q.ListArtifacts(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts of task %s: %w", taskID, err)
	}
	out := make([]Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, artifactOf(r))
	}
	return out, nil
}

// ListArtifactsOfKind returns a task's artifacts of one kind, by name.
func (s *Store) ListArtifactsOfKind(ctx context.Context, taskID, kind string) ([]Artifact, error) {
	rows, err := s.q.ListArtifactsByKind(ctx, db.ListArtifactsByKindParams{TaskID: taskID, Kind: kind})
	if err != nil {
		return nil, fmt.Errorf("list %s artifacts of task %s: %w", kind, taskID, err)
	}
	out := make([]Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, artifactOf(r))
	}
	return out, nil
}

// GetArtifact reads one artifact by ID. A missing row is ErrNotFound.
func (s *Store) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	row, err := s.q.GetArtifact(ctx, id)
	if err != nil {
		if noRows(err) {
			return Artifact{}, fmt.Errorf("artifact %s: %w", id, ErrNotFound)
		}
		return Artifact{}, fmt.Errorf("get artifact %s: %w", id, err)
	}
	return artifactOf(row), nil
}

// TasksPendingLogRollUp lists terminal tasks whose logs are still only in Postgres and
// that finished before finishedBefore.
func (s *Store) TasksPendingLogRollUp(ctx context.Context, finishedBefore time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	ids, err := s.q.TasksPendingLogRollUp(ctx, db.TasksPendingLogRollUpParams{
		FinishedBefore: finishedBefore.UTC(),
		PageLimit:      int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks pending log roll-up: %w", err)
	}
	return ids, nil
}

// MarkLogsRolledUp stamps a task as rolled up and records the high-water marks that were
// true at the time. The marks only ever move up: a second roll-up of the same task must
// never lower what reconciliation would answer.
func (s *Store) MarkLogsRolledUp(ctx context.Context, taskID string, up LogRollUp) error {
	if up.HighSeq > math.MaxInt64 {
		return fmt.Errorf("task %s: log high seq %d overflows bigint", taskID, up.HighSeq)
	}
	err := s.q.MarkLogsRolledUp(ctx, db.MarkLogsRolledUpParams{
		ID:           taskID,
		HighSeq:      int64(up.HighSeq),
		StdoutOffset: up.StdoutOffset,
		StderrOffset: up.StderrOffset,
	})
	if err != nil {
		return fmt.Errorf("mark task %s rolled up: %w", taskID, err)
	}
	return nil
}

// TaskLogRollUp reports whether a task's logs have been rolled up, and the marks recorded
// when they were.
func (s *Store) TaskLogRollUp(ctx context.Context, taskID string) (LogRollUp, error) {
	row, err := s.q.TaskLogRollUp(ctx, taskID)
	if err != nil {
		if noRows(err) {
			return LogRollUp{}, fmt.Errorf("task %s: %w", taskID, ErrNotFound)
		}
		return LogRollUp{}, fmt.Errorf("read log roll-up of task %s: %w", taskID, err)
	}
	return LogRollUp{
		At:           utcPtr(row.LogsRolledUpAt),
		HighSeq:      uint64(max(row.LogsHighSeq, 0)),
		StdoutOffset: row.LogsStdoutOffset,
		StderrOffset: row.LogsStderrOffset,
	}, nil
}

func artifactOf(r db.Artifact) Artifact {
	return Artifact{
		ID:          r.ID,
		TaskID:      r.TaskID,
		Kind:        r.Kind,
		Name:        r.Name,
		ObjectKey:   r.ObjectKey,
		SizeBytes:   r.SizeBytes,
		ContentType: r.ContentType,
		SHA256:      r.Sha256,
		CreatedAt:   r.CreatedAt.UTC(),
	}
}
