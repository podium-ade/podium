package store

import (
	"context"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// SandboxReap is a sandbox left behind by a task the control plane gave up on.
type SandboxReap struct {
	Provider  string
	SandboxID string
	TaskID    string
	Reason    string
	CreatedAt time.Time
}

// SetTaskSandbox records the sandbox a node made for the attempt holding leaseID. It
// reports false when the task has moved on to another attempt.
func (s *Store) SetTaskSandbox(ctx context.Context, taskID, leaseID, provider, sandboxID string) (bool, error) {
	n, err := s.q.SetTaskSandbox(ctx, db.SetTaskSandboxParams{
		ID: taskID, LeaseID: &leaseID, SandboxProvider: provider, SandboxID: sandboxID,
	})
	if err != nil {
		return false, fmt.Errorf("record sandbox of task %s: %w", taskID, err)
	}
	return n > 0, nil
}

// ListSandboxReaps returns the provider's queued reaps, oldest first.
func (s *Store) ListSandboxReaps(ctx context.Context, provider string) ([]SandboxReap, error) {
	rows, err := s.q.ListSandboxReaps(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("list sandbox reaps of %s: %w", provider, err)
	}
	out := make([]SandboxReap, 0, len(rows))
	for _, r := range rows {
		out = append(out, SandboxReap{
			Provider: r.Provider, SandboxID: r.SandboxID, TaskID: r.TaskID, Reason: r.Reason, CreatedAt: r.CreatedAt.UTC(),
		})
	}
	return out, nil
}

// sandboxGrace is how long a finished task's sandbox counts as up when no node says it was
// deleted, so a node that went away mid-teardown does not hold a provider's cap forever.
const sandboxGrace = 2 * time.Minute

// SandboxesKept counts, by provider, the sandboxes still up with no task running in them:
// those of finished tasks no node has said are deleted, and queued deletions.
func (s *Store) SandboxesKept(ctx context.Context) (map[string]int32, error) {
	rows, err := s.q.SandboxesKept(ctx, int32(sandboxGrace/time.Second))
	if err != nil {
		return nil, fmt.Errorf("count kept sandboxes: %w", err)
	}
	out := make(map[string]int32, len(rows))
	for _, r := range rows {
		out[r.Provider] = r.Kept
	}
	return out, nil
}

// SandboxDeleted records that a node deleted a sandbox: its reap, if any, goes, and it no
// longer counts against its provider.
func (s *Store) SandboxDeleted(ctx context.Context, provider, sandboxID string) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := q.DeleteSandboxReap(ctx, db.DeleteSandboxReapParams{Provider: provider, SandboxID: sandboxID}); err != nil {
			return fmt.Errorf("delete sandbox reap %s/%s: %w", provider, sandboxID, err)
		}
		if _, err := q.MarkSandboxDeleted(ctx, db.MarkSandboxDeletedParams{Provider: provider, SandboxID: sandboxID}); err != nil {
			return fmt.Errorf("mark sandbox %s/%s deleted: %w", provider, sandboxID, err)
		}
		return nil
	})
}
