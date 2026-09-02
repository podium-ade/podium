// Package logs owns the write side of a task's event history: it turns a node's event batch
// into rows, applies the status transitions those events imply, and fans the result out to live
// subscribers. Everything a client ever sees about a running task comes through here.
package logs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// pollInterval bounds how long a subscriber waits before re-reading the store without a
// wake-up. Wake-ups do the real work; this is insurance against a dropped notification.
const pollInterval = time.Second

// pageSize is how many rows of each table a subscriber reads per round trip.
const pageSize = 256

// Slots gives a node back the slot it was holding for a task. The node session registry
// implements it.
type Slots interface {
	Release(taskID string)
}

// Service ingests node event batches and serves StreamTaskEvents.
type Service struct {
	store  *store.Store
	logger *slog.Logger
	slots  Slots

	mu         sync.Mutex
	watchers   map[string]map[chan struct{}]struct{}
	cancelling map[string]string
}

// New returns a Service. Call Run once to follow Postgres notifications.
func New(st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:      st,
		logger:     logger,
		watchers:   make(map[string]map[chan struct{}]struct{}),
		cancelling: make(map[string]string),
	}
}

// SetSlots wires in the node session registry. It is a setter rather than a constructor
// argument because the registry is built around this service: nodes.NewService takes the
// Ingestor, so neither can be constructed before the other.
func (s *Service) SetSlots(sl Slots) { s.slots = sl }

// Run follows the task-event notification channel until ctx is cancelled, waking the
// subscribers of every task that gains rows. One subscription serves the whole process.
func (s *Service) Run(ctx context.Context) error {
	notifications, err := s.store.SubscribeTaskEvents(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to task events: %w", err)
	}
	for taskID := range notifications {
		s.wake(taskID)
	}
	return nil
}

// MarkCancelling records that a cancel was requested for taskID, so the terminal event turns
// the task into cancelled rather than succeeded or failed. The mark is in memory on purpose:
// so is the session registry, and step 12 owns durable cancellation.
func (s *Service) MarkCancelling(taskID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelling[taskID] = reason
}

// Ingest stores one node event batch and returns the highest seq it contained, which is what
// the caller acks. A node numbers all of a task's events from one seq space, so the batch is
// split across task_events and task_log_chunks but written in a single transaction — the ack
// therefore covers the whole batch, not either table's high-water mark.
//
// Re-ingesting an acked batch is a no-op: the rows conflict on (task_id, seq) and the status
// transitions they imply are already applied and get rejected harmlessly.
func (s *Service) Ingest(ctx context.Context, taskID string, batch []*podiumv1.TaskEvent) (uint64, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	var (
		events []store.Event
		chunks []store.LogChunk
		maxSeq uint64
	)
	for _, e := range batch {
		if e.GetSeq() > maxSeq {
			maxSeq = e.GetSeq()
		}
		ts := eventTime(e)
		if e.GetKind() == podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG {
			lc := e.GetLog()
			chunks = append(chunks, store.LogChunk{
				Seq:     e.GetSeq(),
				Stream:  streamNames[lc.GetStream()],
				Sidecar: lc.GetSidecarName(),
				TS:      ts,
				Bytes:   lc.GetBytes(),
			})
			continue
		}
		payload, err := payloadJSON(e)
		if err != nil {
			return 0, err
		}
		events = append(events, store.Event{
			Seq:     e.GetSeq(),
			Kind:    KindString(e.GetKind()),
			TS:      ts,
			Payload: payload,
		})
	}

	if err := s.store.AppendBatch(ctx, taskID, events, chunks); err != nil {
		return 0, err
	}
	for _, e := range batch {
		s.applyStatus(ctx, taskID, e)
	}

	s.wake(taskID)
	if err := s.store.NotifyTaskEvents(ctx, taskID); err != nil {
		s.logger.WarnContext(ctx, "notify task events failed", "task_id", taskID, "error", err)
	}
	return maxSeq, nil
}

// applyStatus moves the task along its state machine for the kinds that imply a transition. A
// rejected transition is expected — a replayed batch re-applies transitions that already
// happened — so it is logged and never fails the ingest.
func (s *Service) applyStatus(ctx context.Context, taskID string, e *podiumv1.TaskEvent) {
	ts := eventTime(e)
	var (
		from  []store.Status
		to    store.Status
		patch store.Patch
	)
	switch e.GetKind() {
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING:
		from, to = []store.Status{store.StatusScheduled}, store.StatusProvisioning
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED:
		from, to = []store.Status{store.StatusProvisioning}, store.StatusRunning
		patch.StartedAt = &ts
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED:
		fin := e.GetFinished()
		code := fin.GetExitCode()
		to = store.StatusSucceeded
		if code != 0 {
			to = store.StatusFailed
		}
		patch.FinishedAt = &ts
		patch.ExitCode = &code
		if u := fin.GetUsage(); u != nil {
			patch.Usage = &store.Usage{
				CPUSeconds:   u.GetCpuSeconds(),
				PeakMemoryMB: u.GetPeakMemoryMb(),
				WallMS:       u.GetWallMs(),
			}
		}
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR:
		if e.GetError().GetRetryable() {
			return
		}
		to = store.StatusFailed
		msg := e.GetError().GetMessage()
		patch.FailureReason = &msg
		patch.FinishedAt = &ts
	default:
		return
	}

	if to.Terminal() {
		if reason, ok := s.takeCancelling(taskID); ok {
			to = store.StatusCancelled
			patch.FailureReason = &reason
			from = nil
		}
		// The slot the scheduler booked on assign is only ever given back here. It happens
		// before the transition on purpose: a replayed batch has its transition rejected as
		// already applied, and a slot must not be stranded by that.
		if s.slots != nil {
			s.slots.Release(taskID)
		}
	}

	if _, err := s.store.TransitionTask(ctx, taskID, from, to, patch); err != nil {
		level := slog.LevelError
		if errors.Is(err, store.ErrInvalidTransition) {
			level = slog.LevelDebug
		}
		s.logger.Log(ctx, level, "task transition from event rejected",
			"task_id", taskID, "kind", KindString(e.GetKind()), "to", to, "error", err)
	}
}

// takeCancelling consumes a pending cancel intent.
func (s *Service) takeCancelling(taskID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason, ok := s.cancelling[taskID]
	delete(s.cancelling, taskID)
	return reason, ok
}

// wake nudges every subscriber of taskID. The channel is a coalescing one-slot signal, never
// the event itself, so a slow subscriber can never block ingest — it just re-reads the store.
func (s *Service) wake(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.watchers[taskID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Service) watch(taskID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.watchers[taskID] == nil {
		s.watchers[taskID] = make(map[chan struct{}]struct{})
	}
	s.watchers[taskID][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.watchers[taskID], ch)
		if len(s.watchers[taskID]) == 0 {
			delete(s.watchers, taskID)
		}
	}
}

// Subscribe replays a task's stored events from fromSeq (exclusive, so 0 replays everything)
// and then follows live ones. The returned channel is closed when the task is terminal and
// every event has been delivered, or when ctx is cancelled.
func (s *Service) Subscribe(ctx context.Context, taskID string, fromSeq uint64) (<-chan *podiumv1.TaskEvent, error) {
	if _, err := s.store.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	wake, unwatch := s.watch(taskID)
	out := make(chan *podiumv1.TaskEvent, pageSize)

	go func() {
		defer close(out)
		defer unwatch()

		next := fromSeq + 1
		for {
			drained, err := s.drain(ctx, taskID, &next, out)
			if err != nil {
				if ctx.Err() == nil {
					s.logger.ErrorContext(ctx, "task event replay failed", "task_id", taskID, "error", err)
				}
				return
			}
			if !drained {
				return
			}
			done, err := s.terminal(ctx, taskID)
			if err != nil {
				return
			}
			if done {
				// The status is written after the rows commit, so one more pass is
				// guaranteed to see everything the task will ever emit.
				if _, err := s.drain(ctx, taskID, &next, out); err != nil && ctx.Err() == nil {
					s.logger.ErrorContext(ctx, "task event replay failed", "task_id", taskID, "error", err)
				}
				return
			}
			select {
			case <-wake:
			case <-time.After(pollInterval):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// drain sends every event from *next onwards that is currently stored, advancing *next. It
// returns false when ctx ended mid-send.
func (s *Service) drain(ctx context.Context, taskID string, next *uint64, out chan<- *podiumv1.TaskEvent) (bool, error) {
	for {
		batch, err := s.readFrom(ctx, taskID, *next)
		if err != nil {
			return false, err
		}
		if len(batch) == 0 {
			return true, nil
		}
		for _, e := range batch {
			select {
			case out <- e:
			case <-ctx.Done():
				return false, nil
			}
		}
		*next = batch[len(batch)-1].GetSeq() + 1
	}
}

// readFrom merges one page of task_events and one page of task_log_chunks into a single
// seq-ordered run. The two tables share the node's one seq space, so the merge is total; when
// either page is full the run is cut at the lower of the two last seqs, because rows beyond it
// may still be unread in the other table.
func (s *Service) readFrom(ctx context.Context, taskID string, fromSeq uint64) ([]*podiumv1.TaskEvent, error) {
	eventRows, err := s.store.ListEvents(ctx, taskID, fromSeq, pageSize)
	if err != nil {
		return nil, err
	}
	chunkRows, err := s.store.ListLogChunks(ctx, taskID, fromSeq, pageSize)
	if err != nil {
		return nil, err
	}

	cutoff := ^uint64(0)
	if len(eventRows) == pageSize {
		cutoff = min(cutoff, eventRows[len(eventRows)-1].Seq)
	}
	if len(chunkRows) == pageSize {
		cutoff = min(cutoff, chunkRows[len(chunkRows)-1].Seq)
	}

	out := make([]*podiumv1.TaskEvent, 0, len(eventRows)+len(chunkRows))
	i, j := 0, 0
	for i < len(eventRows) || j < len(chunkRows) {
		takeEvent := j >= len(chunkRows) || (i < len(eventRows) && eventRows[i].Seq < chunkRows[j].Seq)
		var (
			e   *podiumv1.TaskEvent
			seq uint64
		)
		if takeEvent {
			seq = eventRows[i].Seq
			if e, err = eventToProto(taskID, eventRows[i]); err != nil {
				return nil, err
			}
			i++
		} else {
			seq = chunkRows[j].Seq
			e = chunkToProto(taskID, chunkRows[j])
			j++
		}
		if seq > cutoff {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// terminal reports whether the task has reached an end state.
func (s *Service) terminal(ctx context.Context, taskID string) (bool, error) {
	t, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	return t.Status.Terminal(), nil
}
