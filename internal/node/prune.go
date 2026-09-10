package node

import (
	"context"
	"log/slog"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
)

// pruneInterval is how often the image cache is considered, on top of the check every
// heartbeat does when it sees the disk cross the watermark.
const pruneInterval = 5 * time.Minute

// maybePrune is called from the heartbeat, which already samples the disk. It does nothing
// at all unless the operator turned pruning on.
//
// Pruning is opt-in (`image_cache_prune: true`, or PODIUM_NODE_IMAGE_CACHE_PRUNE=1) and
// that default is deliberate. A node shares its Docker engine with whatever else runs on
// the machine, and on a developer's laptop that is their own work. An LRU sweep that
// deleted the wrong image would cost them a rebuild they may not be able to reproduce, so
// the feature does not run unless somebody said it should. Even then it only ever considers
// images Podium pulled itself — see docker.ErrNotPodiumImage.
func (n *Node) maybePrune(ctx context.Context, load hostLoad) {
	if load.DiskTotalBytes <= 0 {
		return
	}
	used := load.DiskUsedPct / 100
	above := used >= n.cfg.ImageCacheHighWatermark
	if n.diskAboveWatermark.Swap(above) != above {
		n.logger.LogAttrs(ctx, levelFor(above), "node disk crossed the image cache watermark",
			slog.Float64("used_fraction", used),
			slog.Float64("high_watermark", n.cfg.ImageCacheHighWatermark),
			slog.Bool("accepting_work", !above))
	}
	if !above || !n.cfg.ImageCachePrune {
		return
	}
	n.pruneOnce(ctx, load)
}

func levelFor(bad bool) slog.Level {
	if bad {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// runPruneLoop is the periodic half: every five minutes, whatever the heartbeat saw.
func (n *Node) runPruneLoop(ctx context.Context) {
	if !n.cfg.ImageCachePrune {
		return
	}
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.pruneOnce(ctx, sampleLoad(ctx, n.cfg.DataDir, n.logger))
		}
	}
}

// pruneOnce asks the executor to drop the least recently used images Podium pulled, never
// including one a task on this node is running right now.
func (n *Node) pruneOnce(ctx context.Context, load hostLoad) {
	if load.DiskTotalBytes <= 0 {
		return
	}
	removed, err := n.exec.PruneImages(ctx, docker.PruneRequest{
		DiskUsedFraction: load.DiskUsedPct / 100,
		HighWatermark:    n.cfg.ImageCacheHighWatermark,
		TotalBytes:       load.DiskTotalBytes,
		InUse:            n.imagesInUse(),
	})
	if err != nil {
		n.logger.WarnContext(ctx, "image cache prune made no progress", "error", err)
		return
	}
	if len(removed) > 0 {
		n.logger.InfoContext(ctx, "image cache pruned", "images", len(removed))
	}
}

// imagesInUse is every image a task on this node needs right now, task containers and
// sidecars alike. They are excluded from the candidates before the allow-list is even
// consulted.
func (n *Node) imagesInUse() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.taskImages))
	for _, refs := range n.taskImages {
		out = append(out, refs...)
	}
	return out
}

// diskFull reports whether the last disk sample was above the high watermark.
func (n *Node) diskFull() bool { return n.diskAboveWatermark.Load() }
