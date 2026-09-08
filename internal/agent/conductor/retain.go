package conductor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// retainTimeout bounds one retain. Hindsight hands extraction to its own worker and answers
// in milliseconds, so this only ever matters when it is unreachable — and it is deliberately
// inside the conductor's ShutdownTimeout, so a retain in flight at SIGTERM finishes rather
// than making the shutdown warn.
const retainTimeout = 10 * time.Second

// maxRetainedAnswer caps the answer inside a retained item. Extraction works on the gist and
// the whole transcript is already an artifact of the task; a megabyte of prose here would be
// a megabyte of LLM input for no more facts.
const maxRetainedAnswer = 8 << 10

// truncationNote is what replaces the tail of an answer that did not fit.
const truncationNote = "\n…(answer truncated)"

// Metric labels for podium_agent_memory_retain_total.
const (
	retainAccepted = "accepted"
	retainError    = "error"
	retainRedacted = "redacted"
)

// retain records one finished turn in the shared memory, if there is anything worth
// remembering and anywhere to put it.
//
// It runs after the turn is recorded, after the answer is posted and after the outcome is
// on the triggering message, so nothing a human waits for waits for this. A memory outage
// must never fail or delay a turn: every failure below is a log line and a counter.
//
// The exchange is retained, not the transcript. Hindsight extracts facts from prose, and a
// tool log would produce noise.
func (r *turnRun) retain(ctx context.Context, status string) {
	c := r.c
	if c.memories == nil {
		return
	}
	answer := strings.TrimSpace(r.answer())
	if !retainable(r.src.Kind(), status, answer) {
		return
	}

	item := memory.Item{
		Content:    retainContent(r.author, r.instruction, c.profiles.Current().DisplayName, answer),
		Context:    r.job.provenance(),
		Tags:       append([]string{"source:" + r.sess.SourceKind}, r.job.memoryTags()...),
		Metadata:   retainMetadata(r),
		DocumentID: r.turn.ID,
	}

	// Detached from the turn's context on purpose: a SIGTERM arriving between the answer
	// and this call should not lose the memory. The conductor's WaitGroup is what makes
	// the process wait for it.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		retainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), retainTimeout)
		defer cancel()

		err := c.memories.Retain(retainCtx, item)
		switch {
		case err == nil:
			c.metrics.MemoryRetains.WithLabelValues(retainAccepted).Inc()
			// "accepted", not "retained": the retain is asynchronous, so all this says
			// is that Hindsight took the work. Whether a fact came out of it is decided
			// later by its own worker and reported by watchExtractions — see
			// reconcile.go, and note that this line looked healthy throughout an
			// install where extraction had never once succeeded.
			//
			// The content is not logged, at any level: it is a task's words about a
			// human's words, and this log is not where either belongs.
			c.logger.InfoContext(retainCtx, "handed the turn to shared memory for extraction",
				"turn_id", r.turn.ID, "bytes", len(item.Content))
		case errors.Is(err, memory.ErrRedacted):
			c.metrics.MemoryRetains.WithLabelValues(retainRedacted).Inc()
			c.logger.WarnContext(retainCtx, "not retaining this turn: its answer carries a "+
				"redaction marker, which means a secret was in it",
				"turn_id", r.turn.ID, "task_id", r.turn.TaskID)
		default:
			c.metrics.MemoryRetains.WithLabelValues(retainError).Inc()
			// Never posted to the conversation. A human asked a question and got an
			// answer; the bot's filing system is not their problem.
			c.logger.WarnContext(retainCtx, "retaining the turn in shared memory failed",
				"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "error", err)
		}
	}()
}

// retainable is the whole of what is worth remembering.
//
// A turn that did not succeed has nothing to say: a failure, a loss, a cancellation and a
// max-turns stop all end with an apology or a half-answer, and neither is a fact. A turn
// with no answer has nothing at all.
//
// The dev source is excluded because it is test-only and it is the only thing in Podium
// that can put the runtime into dry run — so "dry-run turns retain nothing" and "a fake
// conversation cannot plant a memory" are the same exclusion. The source kind is also the
// only form of it that survives a restart: the dry-run env lives on a task spec the
// conductor does not read back when it resumes a turn.
func retainable(sourceKind, status, answer string) bool {
	return status == store.TurnSucceeded && answer != "" && sourceKind != KindDev
}

// retainContent is the retained prose. One item per turn: the question and the answer, in
// the words they were said in.
//
// A resumed turn has lost the question — the instruction is not persisted — and keeping the
// answer alone is better than losing the memory, so the question is optional here.
func retainContent(author, instruction, displayName, answer string) string {
	if len(answer) > maxRetainedAnswer {
		// On a rune boundary: half a character in shared memory is a character nobody can
		// read and an embedding nobody can match.
		cut := maxRetainedAnswer - len(truncationNote)
		for cut > 0 && !utf8.RuneStart(answer[cut]) {
			cut--
		}
		answer = answer[:cut] + truncationNote
	}
	if strings.TrimSpace(instruction) == "" {
		return fmt.Sprintf("%s answered: %s", displayName, answer)
	}
	return fmt.Sprintf("%s asked: %s\n\n%s answered: %s", author, instruction, displayName, answer)
}

// retainMetadata is the provenance every retained fact carries. It is what the Memory tab's
// chips read, and it is the only thing that lets a human trace a memory back to the
// conversation that produced it — which is the whole mitigation for a poisoned turn.
func retainMetadata(r *turnRun) map[string]string {
	meta := map[string]string{
		"session_id": r.sess.ID,
		"turn_id":    r.turn.ID,
		"source_ref": r.ref,
	}
	if r.turn.TaskID != "" {
		meta["task_id"] = r.turn.TaskID
	}
	if r.url != "" {
		meta["source_url"] = r.url
	}
	return meta
}
