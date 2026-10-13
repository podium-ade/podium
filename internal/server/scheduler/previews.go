package scheduler

import (
	"context"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

// Reasons a preview is let go of without anybody asking.
const (
	ReasonPreviewExpired = "expired"
	ReasonPreviewEnded   = "task ended without holding"
)

// sweepPreviews ends every preview whose time is up, and every one whose task ended in a
// way that left nothing held on its node — cancelled, lost, failed before the command ran.
// The row is released first and the node told second: a node that is away hears it in its
// next HelloAck instead, so no Release is ever retried from here.
func (s *Service) sweepPreviews(ctx context.Context) {
	expired, err := s.store.ExpiredPreviews(ctx, time.Now().UTC())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "preview sweep could not list expired previews", "error", err)
		}
		return
	}
	for _, p := range expired {
		s.releasePreview(ctx, p, ReasonPreviewExpired)
	}
	orphaned, err := s.store.OrphanedPreviews(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "preview sweep could not list orphaned previews", "error", err)
		}
		return
	}
	for _, p := range orphaned {
		s.releasePreview(ctx, p, ReasonPreviewEnded)
	}
}

func (s *Service) releasePreview(ctx context.Context, p store.Preview, reason string) {
	if _, released, err := s.store.ReleasePreview(ctx, p.TaskID, reason); err != nil || !released {
		if err != nil {
			s.logger.ErrorContext(ctx, "releasing a preview failed", "task_id", p.TaskID, "error", err)
		}
		return
	}
	s.logger.InfoContext(ctx, "preview released", "task_id", p.TaskID, "node_id", p.NodeID, "reason", reason)
	if p.NodeID == "" {
		return
	}
	if err := s.nodes.ReleasePreview(ctx, p.NodeID, p.TaskID, reason); err != nil {
		s.logger.DebugContext(ctx, "the node will hear about the release when it reconnects",
			"task_id", p.TaskID, "node_id", p.NodeID, "error", err)
	}
}
