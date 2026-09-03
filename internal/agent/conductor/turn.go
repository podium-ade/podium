package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// KindDev is the test-only in-process source's kind. The conductor honours a source's
// requested task environment for this kind and no other.
const KindDev = "dev"

// turnRun is one turn being followed. Everything in it is touched from exactly one
// goroutine — the one running run — so none of it is locked.
type turnRun struct {
	c     *Conductor
	src   Source
	sess  store.Session
	skill profiles.Skill
	turn  store.Turn
	// ref is the source's reference to the message that started the turn.
	ref string
	// author, instruction and url come off the inbound event and exist for the
	// end-of-turn retain (retain.go). A resumed turn has none of them: they are not
	// persisted, and only the answer is.
	author      string
	instruction string
	url         string
	// placeholder is the "working…" message progress edits replace. It is empty for a
	// resumed turn, whose progress is posted as new messages instead.
	placeholder string
	startedAt   time.Time

	lastSeq uint64
	// finals is every final text this run relayed, in order. The runtime splits an answer
	// over 32 KiB into several consecutive finals; joined with a blank line they are the
	// whole answer again.
	finals []string
	// attachments are artifact names the finals asked for, in order, deduplicated.
	attachments []string
	// heldProgress is a progress text waiting for the throttle to let it through.
	heldProgress string
	haveHeld     bool
	lastEdit     time.Time
	// errText is the last non-retryable error event. It goes to the log, never to a human.
	errText string
}

// run relays the task's messages and then records how the turn ended.
func (r *turnRun) run(ctx context.Context) {
	f := &follower{
		tasks:       r.c.podium.Tasks,
		taskID:      r.turn.TaskID,
		lastSeq:     r.lastSeq,
		onEvent:     r.onEvent,
		onReconnect: r.c.metrics.FollowReconnects.Inc,
	}
	followErr := f.follow(ctx)
	r.flushProgress(ctx)
	if ctx.Err() != nil {
		// A shutdown mid-turn. The turn stays running in the database and the recovery
		// pass picks it up on the next start; the relayed table means it will not repeat
		// anything it already said.
		r.c.logger.InfoContext(ctx, "stopping while a turn is in flight; it will be resumed",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID)
		return
	}
	if followErr != nil {
		r.c.logger.WarnContext(ctx, "following the turn's task ended early",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "error", followErr)
	}

	task, err := getTaskWithRetry(ctx, r.c.podium.Tasks, r.turn.TaskID, terminalStatusBudget)
	if err != nil {
		r.c.logger.ErrorContext(ctx, "reading the turn's task back failed",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "error", err)
		r.c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"I lost track of this one. Task `%s`.", r.turn.TaskID)})
		r.finish(ctx, store.TurnFailed, nil, nil)
		return
	}

	summary := r.settle(ctx, task)
	result := classify(task, r.skill.Timeout.Std())
	if result.Post != "" {
		// The raw failure_reason never reaches a human: it can carry a name, a path or a
		// stack, and none of that is an answer. It goes to the log with the task id.
		r.c.logger.WarnContext(ctx, "a turn did not succeed",
			"turn_id", r.turn.ID, "task_id", task.GetId(), "status", task.GetStatus().String(),
			"exit_code", task.GetExitCode(), "failure_reason", task.GetFailureReason(),
			"error_event", r.errText)
		r.c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, Text: result.Post})
	}
	r.finish(ctx, result.Status, summary.numTurns(), summary.cost())
}

// finish records the turn, shows the outcome and counts it.
func (r *turnRun) finish(ctx context.Context, status string, numTurns *int, cost *float64) {
	if err := r.c.store.FinishTurn(ctx, r.turn.ID, status, numTurns, cost, strings.Join(r.finals, "\n\n")); err != nil {
		r.c.logger.ErrorContext(ctx, "recording how the turn ended failed",
			"turn_id", r.turn.ID, "status", status, "error", err)
	}
	reaction := ReactionDone
	if status != store.TurnSucceeded {
		reaction = ReactionFailed
	}
	r.c.finish(ctx, r.src, r.ref, reaction)
	r.c.metrics.Turns.WithLabelValues(r.sess.SourceKind, r.skill.Name, status).Inc()
	r.c.metrics.TurnDuration.WithLabelValues(r.skill.Name).Observe(time.Since(r.startedAt).Seconds())
	r.c.logger.InfoContext(ctx, "turn finished", "turn_id", r.turn.ID, "task_id", r.turn.TaskID,
		"status", status, "skill", r.skill.Name)
	// Last, deliberately: the answer is posted and the outcome is on the message, so a
	// memory outage costs a log line and nothing a human is waiting for.
	r.retain(ctx, status)
}

// onEvent is the relay. Only MESSAGE events say anything; ERROR is remembered for the log;
// everything else, log chunks included, is ignored — the conductor is not a log consumer.
func (r *turnRun) onEvent(ctx context.Context, e *podiumv1.TaskEvent) {
	switch e.GetKind() {
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE:
		r.relay(ctx, e)
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR:
		if err := e.GetError(); err != nil && !err.GetRetryable() {
			r.errText = err.GetMessage()
		}
	default:
	}
}

// relay says one message, exactly once. MarkRelayed is the ledger: the insert either takes
// (task_id, seq) or finds it taken, and only the run that took it speaks.
func (r *turnRun) relay(ctx context.Context, e *podiumv1.TaskEvent) {
	first, err := r.c.store.MarkRelayed(ctx, r.turn.TaskID, e.GetSeq())
	if err != nil {
		// Saying nothing is recoverable — the next start resumes from the last seq that
		// *was* recorded — and saying something twice is not.
		r.c.logger.ErrorContext(ctx, "claiming a message for relay failed; not posting it",
			"task_id", r.turn.TaskID, "seq", e.GetSeq(), "error", err)
		return
	}
	if !first {
		return
	}
	msg := e.GetMessage()
	if msg == nil {
		return
	}
	r.c.metrics.RelayedMessages.WithLabelValues(msg.GetType()).Inc()

	if msg.GetType() != OutFinal {
		// Anything that is not a final is progress, whatever it called itself.
		r.heldProgress = msg.GetText()
		r.haveHeld = true
		r.maybeEdit(ctx)
		return
	}
	// A held progress edit is flushed before the answer lands, so the thread never shows
	// a stale "working on it" above the final.
	r.flushProgress(ctx)
	text := msg.GetText()
	r.finals = append(r.finals, text)
	for _, name := range msg.GetAttachments() {
		if !slices.Contains(r.attachments, name) {
			r.attachments = append(r.attachments, name)
		}
	}
	if text == "" {
		return
	}
	r.c.post(ctx, r.src, r.ref, Outbound{Type: OutFinal, Text: text})
}

// maybeEdit shows the held progress if the throttle allows it. There is no timer: the next
// progress or the final flushes whatever is held, and the runtime coalesces progress to at
// most one every five seconds anyway (step 16), so the throttle almost never engages.
func (r *turnRun) maybeEdit(ctx context.Context) {
	if time.Since(r.lastEdit) < progressThrottle {
		return
	}
	r.flushProgress(ctx)
}

// flushProgress edits the placeholder to the newest progress text, or posts it as a new
// message when there is no placeholder to edit (a resumed turn).
func (r *turnRun) flushProgress(ctx context.Context) {
	if !r.haveHeld {
		return
	}
	text := r.heldProgress
	r.haveHeld = false
	r.heldProgress = ""
	r.lastEdit = time.Now()
	if strings.TrimSpace(text) == "" {
		return
	}
	out := Outbound{Type: OutProgress, Text: progressPrefix + text}
	if r.placeholder == "" {
		r.placeholder = r.c.post(ctx, r.src, r.ref, out)
		return
	}
	if err := r.src.Edit(ctx, r.ref, r.placeholder, out); err != nil {
		r.c.logger.WarnContext(ctx, "editing the progress message failed",
			"ref", r.ref, "error", err)
	}
}

// turnSummary is the runtime's turn.json, as much of it as the turns table keeps.
type turnSummary struct {
	NumTurns     *int     `json:"num_turns"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	found        bool
}

func (s turnSummary) numTurns() *int {
	if !s.found {
		return nil
	}
	return s.NumTurns
}

func (s turnSummary) cost() *float64 {
	if !s.found {
		return nil
	}
	return s.TotalCostUSD
}

// settle resolves the final's attachments against what the task actually stored, uploads
// them, and reads the runtime's own accounting. It runs once the task is terminal, because
// an artifact named in a message may still have been uploading when the message arrived.
func (r *turnRun) settle(ctx context.Context, task *podiumv1.Task) turnSummary {
	var summary turnSummary
	if len(r.attachments) == 0 && task.GetStatus() != podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED {
		// Nothing to attach and nothing worth accounting for.
		return summary
	}
	list, err := r.c.podium.ListArtifacts(ctx, r.turn.TaskID)
	if err != nil {
		r.c.logger.WarnContext(ctx, "listing the turn's artifacts failed",
			"task_id", r.turn.TaskID, "error", err)
		return summary
	}
	byName := make(map[string]*podiumv1.Artifact, len(list))
	for _, a := range list {
		byName[a.GetName()] = a
	}
	if art, ok := byName[turnSummaryArtifact]; ok {
		summary = r.readSummary(ctx, art)
	}
	for _, name := range r.attachments {
		art, ok := byName[name]
		if !ok {
			// Normal, not an error: a name that matches nothing is how the runtime says
			// "I mentioned a file I did not produce".
			r.c.post(ctx, r.src, r.ref, Outbound{Type: OutProgress, Text: fmt.Sprintf(
				"no artifact named `%s` was produced", name)})
			continue
		}
		r.attach(ctx, art)
	}
	return summary
}

// attach streams one artifact into the conversation. Nothing is buffered: an artifact may
// be hundreds of megabytes, and one over the relay limit is refused with a line in the
// thread rather than proxied.
func (r *turnRun) attach(ctx context.Context, art *podiumv1.Artifact) {
	if art.GetSizeBytes() > podium.MaxAttachmentBytes {
		r.c.post(ctx, r.src, r.ref, Outbound{Type: OutProgress, Text: fmt.Sprintf(
			"`%s` is %d MB, which is more than I will relay. It is on the task: `podium artifact get %s`.",
			art.GetName(), art.GetSizeBytes()>>20, art.GetId())})
		return
	}
	body, contentType, err := r.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		if errors.Is(err, podium.ErrTooLarge) {
			r.c.post(ctx, r.src, r.ref, Outbound{Type: OutProgress, Text: fmt.Sprintf(
				"`%s` is too large for me to relay. It is on the task: `podium artifact get %s`.",
				art.GetName(), art.GetId())})
			return
		}
		r.c.logger.WarnContext(ctx, "downloading an attachment failed",
			"artifact_id", art.GetId(), "name", art.GetName(), "error", err)
		return
	}
	defer func() { _ = body.Close() }()
	if err := r.src.Attach(ctx, r.ref, Attachment{
		Name:        art.GetName(),
		ContentType: contentType,
		Size:        art.GetSizeBytes(),
		Body:        body,
	}); err != nil {
		r.c.logger.WarnContext(ctx, "attaching a file to the conversation failed",
			"artifact_id", art.GetId(), "name", art.GetName(), "error", err)
	}
}

// readSummary pulls num_turns and total_cost_usd out of turn.json. It is small by
// construction, so a bounded read is honest rather than defensive.
func (r *turnRun) readSummary(ctx context.Context, art *podiumv1.Artifact) turnSummary {
	body, _, err := r.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the turn summary failed",
			"artifact_id", art.GetId(), "error", err)
		return turnSummary{}
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(body, 64<<10))
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the turn summary failed", "artifact_id", art.GetId(), "error", err)
		return turnSummary{}
	}
	var summary turnSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		r.c.logger.WarnContext(ctx, "the turn summary does not parse",
			"artifact_id", art.GetId(), "error", err)
		return turnSummary{}
	}
	summary.found = true
	return summary
}
