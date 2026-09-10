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

	"google.golang.org/protobuf/encoding/protojson"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/artifacts"
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

// Retries hands a task back to whatever owns placement, after an error that ended the
// node's work on it without ending the task. The scheduler implements it: it owns the
// attempt budget, the lease and the queue, so the decision between another attempt and a
// terminal failure is not this package's to make.
type Retries interface {
	// Retry takes the task off the node it was on: queued again if max_attempts allows,
	// terminal with reason if it does not. Either way the task stops being the node's.
	Retry(ctx context.Context, taskID, reason string)
}

// Service ingests node event batches and serves StreamTaskEvents.
type Service struct {
	store   *store.Store
	logger  *slog.Logger
	slots   Slots
	retries Retries
	// archive is the object store rolled-up logs live in, nil when none is configured.
	archive *artifacts.Service
	rollup  RollupConfig

	mu        sync.Mutex
	watchers  map[string]map[chan struct{}]struct{}
	oomKilled map[string]struct{}
}

// New returns a Service. Call Run once to follow Postgres notifications.
func New(st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:     st,
		logger:    logger,
		rollup:    DefaultRollupConfig(),
		watchers:  make(map[string]map[chan struct{}]struct{}),
		oomKilled: make(map[string]struct{}),
	}
}

// SetSlots wires in the node session registry. It is a setter rather than a constructor
// argument because the registry is built around this service: nodes.NewService takes the
// Ingestor, so neither can be constructed before the other.
func (s *Service) SetSlots(sl Slots) { s.slots = sl }

// SetRetries wires in the scheduler, for the same reason SetSlots is a setter: the
// scheduler is built on the node registry, which is built on this service.
func (s *Service) SetRetries(r Retries) { s.retries = r }

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
				Seq:          e.GetSeq(),
				Stream:       streamNames[lc.GetStream()],
				Sidecar:      lc.GetSidecarName(),
				TS:           ts,
				Bytes:        lc.GetBytes(),
				SourceOffset: lc.GetSourceOffset(),
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
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED:
		// The exit itself implies no transition — finished carries the outcome — but it
		// is the only place the kernel's verdict is reported, so remember it for the
		// transition that follows.
		if e.GetExited().GetOomKilled() {
			s.markOOM(taskID)
		}
		return
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
		fail := e.GetError()
		if !fail.GetAbortsRun() {
			// The run survived it — an artifact that could not be stored, a log buffer
			// that overflowed. The task still owes an exit code, so its status is not
			// this event's to change.
			return
		}
		msg := fail.GetMessage()
		if fail.GetRetryable() && s.retries != nil {
			// The node has stopped, but another one might not. Whether that is worth
			// doing is the scheduler's call, and it answers with either a new attempt
			// or a terminal status — never with nothing.
			s.retries.Retry(ctx, taskID, msg)
			return
		}
		// Not retryable, or nobody to retry with. Either way the task ends here rather
		// than sitting in a status no node is working on.
		to = store.StatusFailed
		patch.FailureReason = &msg
		patch.FinishedAt = &ts
	default:
		return
	}

	if to.Terminal() {
		if s.takeOOM(taskID) && to == store.StatusFailed {
			// A memory limit kills a task with a plain non-zero exit code, which tells an
			// operator nothing. The container's exit state does, so say so.
			//
			// A task that was also cancelled or timed out loses this reason to the
			// durable intent, which TransitionTask applies: "cancelled by user" is a
			// better answer than "oom" for a task somebody stopped.
			reason := FailureReasonOOM
			patch.FailureReason = &reason
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

// FailureReasonOOM is the tasks.failure_reason a task gets when its container was killed
// for exceeding its memory limit.
const FailureReasonOOM = "oom"

// Note appends one synthetic event to a task's history: something the control plane
// observed rather than the node reported. Losing a node is the case that matters — a task
// that simply stops, with nothing in its log to say why, is the single most confusing
// thing an operator can be shown.
//
// It is stored as a non-retryable error event, which is how the CLI and the UI already
// render "this is why it ended", but it deliberately does *not* run the status transition
// such an event would imply when a node sends one: the caller owns the transition, and for
// a lost node the right end state is lost, not failed.
//
// The sequence number is one past everything stored. That is safe precisely because the
// node whose events would have collided is gone; a node that comes back is handed the new
// high-water mark in its HelloAck and numbers above it.
func (s *Service) Note(ctx context.Context, taskID, message string) error {
	high, err := s.store.MaxTaskSeq(ctx, taskID)
	if err != nil {
		return err
	}
	payload, err := protojson.Marshal(&podiumv1.Error{Message: message})
	if err != nil {
		return fmt.Errorf("marshal note for task %s: %w", taskID, err)
	}
	if err := s.store.AppendBatch(ctx, taskID, []store.Event{{
		Seq:     high + 1,
		Kind:    KindError,
		TS:      time.Now().UTC(),
		Payload: payload,
	}}, nil); err != nil {
		return err
	}
	s.wake(taskID)
	if err := s.store.NotifyTaskEvents(ctx, taskID); err != nil {
		s.logger.WarnContext(ctx, "notify task events failed", "task_id", taskID, "error", err)
	}
	return nil
}

// markOOM records that a task's container was OOM-killed.
func (s *Service) markOOM(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oomKilled[taskID] = struct{}{}
}

// takeOOM consumes the OOM mark, if any.
func (s *Service) takeOOM(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.oomKilled[taskID]
	delete(s.oomKilled, taskID)
	return ok
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
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// A task old enough to have had its hot rows pruned is served from the object store
	// instead. It is terminal by construction — roll-up never touches anything else — so
	// there is nothing to follow and the stream ends as soon as the replay does.
	if task.Status.Terminal() {
		if _, gone, aerr := s.archived(ctx, taskID); aerr != nil {
			return nil, aerr
		} else if gone {
			out := make(chan *podiumv1.TaskEvent, pageSize)
			go func() {
				defer close(out)
				if err := s.replayArchived(ctx, taskID, fromSeq, out); err != nil && ctx.Err() == nil {
					s.logger.ErrorContext(ctx, "replaying a rolled-up task log failed",
						"task_id", taskID, "error", err)
				}
			}()
			return out, nil
		}
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
