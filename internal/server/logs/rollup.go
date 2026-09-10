package logs

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/podium-ade/podium/internal/server/artifacts"
	"github.com/podium-ade/podium/internal/server/store"
)

// RollupConfig is the log roll-up schedule.
//
// Grace is the part that matters. A task's chunks are what store.TaskStreamOffsets and
// store.MaxTaskSeq are computed from, and those are what a restarting node is told to
// resume against, so deleting them is not a housekeeping detail. Roll-up only ever touches
// terminal tasks — which reconciliation never tells a node to adopt — and the grace period
// is the second belt: a task that finished minutes ago still has its hot rows if anything
// is still reading them.
type RollupConfig struct {
	// Interval is how often terminal tasks are swept for roll-up.
	Interval time.Duration
	// Settle is how long after a task finishes before its logs are rolled up. It keeps
	// the roll-up off the heels of the last event batch.
	Settle time.Duration
	// PruneInterval is how often rolled-up chunks are pruned.
	PruneInterval time.Duration
	// Grace is how long a task's chunks survive after its logs reached the object store.
	Grace time.Duration
}

// DefaultRollupConfig is the design's schedule: roll up a minute after a task settles,
// prune hourly, keep the hot rows for a day.
func DefaultRollupConfig() RollupConfig {
	return RollupConfig{
		Interval:      time.Minute,
		Settle:        30 * time.Second,
		PruneInterval: time.Hour,
		Grace:         24 * time.Hour,
	}
}

// Environment overrides for the roll-up schedule. They exist so the prune horizon can be
// driven to a second in a test without waiting a day, and so an operator with a small
// database can shorten it.
const (
	RollupIntervalEnv = "PODIUM_LOG_ROLLUP_INTERVAL"
	RollupSettleEnv   = "PODIUM_LOG_ROLLUP_SETTLE"
	PruneIntervalEnv  = "PODIUM_LOG_PRUNE_INTERVAL"
	ChunkGraceEnv     = "PODIUM_LOG_CHUNK_GRACE"
)

// RollupConfigFromEnv applies the four duration overrides to the defaults. An unparseable
// value is ignored: a typo must not silently delete a day of log retention.
func RollupConfigFromEnv() RollupConfig {
	cfg := DefaultRollupConfig()
	for _, o := range []struct {
		env string
		dst *time.Duration
	}{
		{RollupIntervalEnv, &cfg.Interval},
		{RollupSettleEnv, &cfg.Settle},
		{PruneIntervalEnv, &cfg.PruneInterval},
		{ChunkGraceEnv, &cfg.Grace},
	} {
		if v := os.Getenv(o.env); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				*o.dst = d
			}
		}
	}
	return cfg
}

// rollupBatch is how many tasks one sweep rolls up.
const rollupBatch = 20

// SetArchive wires in the object store and the roll-up schedule. It is a setter for the
// same reason SetSlots is: the server builds the pieces in an order neither can be a
// constructor argument of.
func (s *Service) SetArchive(a *artifacts.Service, cfg RollupConfig) {
	s.archive = a
	s.rollup = cfg
}

// RunRollUp sweeps terminal tasks into the object store and prunes the hot rows of ones
// that have been there long enough. It returns when ctx is cancelled.
func (s *Service) RunRollUp(ctx context.Context) error {
	if s.archive == nil || !s.archive.Enabled() {
		s.logger.InfoContext(ctx, "log roll-up is off: no object store is configured")
		return nil
	}
	s.logger.InfoContext(ctx, "log roll-up running",
		"interval", s.rollup.Interval, "settle", s.rollup.Settle,
		"prune_interval", s.rollup.PruneInterval, "grace", s.rollup.Grace)

	roll := time.NewTicker(s.rollup.Interval)
	defer roll.Stop()
	prune := time.NewTicker(s.rollup.PruneInterval)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-roll.C:
			if n, err := s.RollUpOnce(ctx); err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "log roll-up sweep failed", "error", err)
			} else if n > 0 {
				s.logger.InfoContext(ctx, "rolled up task logs", "tasks", n)
			}
		case <-prune.C:
			if n, err := s.PruneOnce(ctx); err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "log chunk prune failed", "error", err)
			} else if n > 0 {
				s.logger.InfoContext(ctx, "pruned rolled-up log chunks", "rows", n)
			}
		}
	}
}

// RollUpOnce rolls up one batch of terminal tasks and reports how many it did.
func (s *Service) RollUpOnce(ctx context.Context) (int, error) {
	if s.archive == nil || !s.archive.Enabled() {
		return 0, nil
	}
	ids, err := s.store.TasksPendingLogRollUp(ctx, time.Now().UTC().Add(-s.rollup.Settle), rollupBatch)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, taskID := range ids {
		if ctx.Err() != nil {
			return done, nil
		}
		if err := s.rollUpTask(ctx, taskID); err != nil {
			s.logger.ErrorContext(ctx, "rolling up a task's logs failed", "task_id", taskID, "error", err)
			continue
		}
		done++
	}
	return done, nil
}

// PruneOnce deletes the hot rows of tasks rolled up longer ago than the grace period.
func (s *Service) PruneOnce(ctx context.Context) (int64, error) {
	if s.archive == nil || !s.archive.Enabled() {
		return 0, nil
	}
	return s.store.PruneLogChunks(ctx, time.Now().UTC().Add(-s.rollup.Grace))
}

// rollUpTask concatenates one task's log chunks per stream, compresses each with zstd,
// stores it as an artifact of kind log, and records the high-water marks the chunks
// carried. The marks are the whole point of doing it in this order: once they are on the
// task row, deleting the chunks cannot make reconciliation answer a smaller number.
func (s *Service) rollUpTask(ctx context.Context, taskID string) error {
	streams, high, offsets, err := s.readStreams(ctx, taskID)
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(streams) {
		if err := s.putStream(ctx, taskID, name, streams[name]); err != nil {
			return err
		}
	}
	return s.store.MarkLogsRolledUp(ctx, taskID, store.LogRollUp{
		HighSeq:      high,
		StdoutOffset: offsets.Stdout,
		StderrOffset: offsets.Stderr,
	})
}

// putStream compresses one stream's spool file and stores it.
func (s *Service) putStream(ctx context.Context, taskID, name string, spool *os.File) error {
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()
	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("size of %s spool for task %s: %w", name, taskID, err)
	}
	if size == 0 {
		return nil
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind %s spool for task %s: %w", name, taskID, err)
	}
	_, err = s.archive.Put(ctx, artifacts.Upload{
		TaskID:      taskID,
		Kind:        store.ArtifactKindLog,
		Name:        name,
		ContentType: artifacts.LogContentType,
		Size:        size,
		Body:        spool,
	})
	return err
}

// readStreams pages through a task's log chunks and spools each stream, zstd-compressed,
// to a temporary file. Compressing on the way through is what keeps a chatty task's whole
// log from having to fit in memory; a spool file is what lets the object store be handed a
// known length instead of a multipart upload.
func (s *Service) readStreams(ctx context.Context, taskID string) (map[string]*os.File, uint64, store.StreamOffsets, error) {
	spools := make(map[string]*os.File)
	writers := make(map[string]*zstd.Encoder)
	var offsets store.StreamOffsets
	var high uint64

	fail := func(err error) (map[string]*os.File, uint64, store.StreamOffsets, error) {
		for _, w := range writers {
			_ = w.Close()
		}
		for _, f := range spools {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
		return nil, 0, store.StreamOffsets{}, err
	}

	next := uint64(0)
	for {
		chunks, err := s.store.ListLogChunks(ctx, taskID, next, pageSize)
		if err != nil {
			return fail(err)
		}
		if len(chunks) == 0 {
			break
		}
		for _, c := range chunks {
			name := streamArtifactName(c)
			w, ok := writers[name]
			if !ok {
				f, err := os.CreateTemp("", "podium-log-*.zst")
				if err != nil {
					return fail(fmt.Errorf("spool %s of task %s: %w", name, taskID, err))
				}
				enc, err := zstd.NewWriter(f)
				if err != nil {
					_ = f.Close()
					_ = os.Remove(f.Name())
					return fail(fmt.Errorf("compress %s of task %s: %w", name, taskID, err))
				}
				spools[name], writers[name] = f, enc
				w = enc
			}
			if _, err := w.Write(c.Bytes); err != nil {
				return fail(fmt.Errorf("write %s of task %s: %w", name, taskID, err))
			}
			if c.Seq > high {
				high = c.Seq
			}
			if c.Sidecar == "" {
				switch c.Stream {
				case store.StreamStdout:
					offsets.Stdout = max(offsets.Stdout, c.SourceOffset)
				case store.StreamStderr:
					offsets.Stderr = max(offsets.Stderr, c.SourceOffset)
				}
			}
		}
		next = chunks[len(chunks)-1].Seq + 1
	}

	for name, w := range writers {
		if err := w.Close(); err != nil {
			return fail(fmt.Errorf("finish %s of task %s: %w", name, taskID, err))
		}
	}
	// The event table's high-water mark counts too: the marks stand in for everything the
	// chunks used to prove, and a synthetic note must never reuse a spent sequence number.
	eventsHigh, err := s.store.MaxTaskSeq(ctx, taskID)
	if err != nil {
		return fail(err)
	}
	return spools, max(high, eventsHigh), offsets, nil
}

// streamArtifactName is the artifact name — and therefore the object key's stem — of one
// stream: stdout, stderr, or sidecar-<name>.
func streamArtifactName(c store.LogChunk) string {
	if c.Sidecar != "" {
		return "sidecar-" + c.Sidecar
	}
	if c.Stream == "" {
		return store.StreamStdout
	}
	return c.Stream
}

func sortedKeys(m map[string]*os.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
