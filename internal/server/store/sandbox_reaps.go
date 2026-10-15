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

// QueueSandboxReap queues the sandbox the task last recorded for deletion and returns its
// provider, or "" when the task never had one or it is already queued.
func (s *Store) QueueSandboxReap(ctx context.Context, taskID, reason string) (string, error) {
	provider, err := s.q.QueueSandboxReap(ctx, db.QueueSandboxReapParams{TaskID: taskID, Reason: reason})
	if noRows(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("queue sandbox reap for task %s: %w", taskID, err)
	}
	return provider, nil
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

// DeleteSandboxReap drops a reap once a node has deleted the sandbox.
func (s *Store) DeleteSandboxReap(ctx context.Context, provider, sandboxID string) error {
	if _, err := s.q.DeleteSandboxReap(ctx, db.DeleteSandboxReapParams{Provider: provider, SandboxID: sandboxID}); err != nil {
		return fmt.Errorf("delete sandbox reap %s/%s: %w", provider, sandboxID, err)
	}
	return nil
}
