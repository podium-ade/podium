package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// KindDev is the test-only in-process source's kind. The conductor honours a source's
// requested task environment for this kind and no other.
const KindDev = "dev"

// MsgAccounting is the message type the runtime reports turn.json's contents with. `type`
// is an open string on the wire (docs/runner-events.md) so this needs no proto change. It
// is not a thing to say: the conductor reads it and posts nothing.
const MsgAccounting = "accounting"

// MsgQuestion is the message type an interactive turn emits when it is asking a human
// something and waiting. The conductor posts it as a durable message and injects the
// next inbound into the same task instead of starting a new turn.
const MsgQuestion = "question"

// Where a turn's accounting came from. It goes in the log, because "the store was off and
// the message did not arrive either" is a different problem from "the runtime said nothing".
const (
	acctFromMessage  = "message"
	acctFromArtifact = "artifact"
)

// turnRun is one turn being followed. Everything in it is touched from exactly one
// goroutine — the one running run — so none of it is locked.
type turnRun struct {
	// sink is where everything this turn says goes. It is embedded rather than held so
	// that r.src, r.ref, r.finals and the rest read the same as they did when they were
	// fields of this struct — and so a delegated task, which is not a turn, can relay
	// through exactly the same code (delegate.go).
	*sink

	sess store.Session
	job  job
	turn store.Turn
	// author, instruction and url come off the inbound event and exist for the
	// end-of-turn retain (retain.go). A resumed turn has none of them: they are not
	// persisted, and only the answer is.
	author      string
	instruction string
	url         string
	startedAt   time.Time

	lastSeq uint64
	// acct is what the runtime said this turn cost, nil until it says so. acctFrom names
	// the route it arrived by.
	acct     *accounting
	acctFrom string
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
		// Giving up on the stream is giving up on the task. One that keeps running past
		// here is a task nobody is listening to: it holds a node slot and a privileged dind
		// daemon, spends money for minutes more, and can finish by opening a pull request
		// that was never announced to the person who asked for it.
		r.cancelAbandoned(ctx, followErr)
	}

	task, err := getTaskWithRetry(ctx, r.c.podium.Tasks, r.turn.TaskID, terminalStatusBudget)
	if err != nil {
		r.c.logger.ErrorContext(ctx, "reading the turn's task back failed",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "error", err)
		r.c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, TaskID: r.turn.TaskID, Text: fmt.Sprintf(
			"I lost track of this one. Task `%s`.", r.turn.TaskID)})
		r.finish(ctx, store.TurnFailed)
		return
	}

	r.settle(ctx, task)
	result := classify(task, r.job.playbook.Timeout.Std())
	if result.Post != "" {
		// The raw failure_reason never reaches a human: it can carry a name, a path or a
		// stack, and none of that is an answer. It goes to the log with the task id.
		r.c.logger.WarnContext(ctx, "a turn did not succeed",
			"turn_id", r.turn.ID, "task_id", task.GetId(), "status", task.GetStatus().String(),
			"exit_code", task.GetExitCode(), "failure_reason", task.GetFailureReason(),
			"error_event", r.errText)
		r.c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, TaskID: r.turn.TaskID, Text: result.Post})
	}
	r.finish(ctx, result.Status)
}

// noteInteractive parks or unparks this session for a human reply. A question is a
// wait; anything else the task says means it is working again.
func (r *turnRun) noteInteractive(ctx context.Context, msgType string) {
	if msgType == MsgQuestion || msgType == OutQuestion {
		r.c.setAwaiting(r.sess.ID, r.turn.TaskID)
		if err := r.src.React(ctx, r.ref, ReactionAwaiting); err != nil {
			r.c.logger.WarnContext(ctx, "marking the turn as awaiting a reply failed",
				"turn_id", r.turn.ID, "error", err)
		}
		return
	}
	if r.c.clearAwaiting(r.sess.ID, r.turn.TaskID) {
		if err := r.src.React(ctx, r.ref, ReactionWorking); err != nil {
			r.c.logger.WarnContext(ctx, "marking the turn as working again failed",
				"turn_id", r.turn.ID, "error", err)
		}
	}
}

// cancelAbandoned stops a task the conductor can no longer follow. It is the same
// CancelTask `podium task cancel` calls, so the node gets the same SIGTERM and the same 30s
// grace; nothing here waits for it.
func (r *turnRun) cancelAbandoned(ctx context.Context, cause error) {
	_, err := r.c.podium.Tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: r.turn.TaskID,
		Reason: fmt.Sprintf("the conductor stopped following this task: %v", cause),
	}))
	// FailedPrecondition is the control plane saying the task is already terminal, which is
	// what a stream that broke as its task ended looks like: there is nothing to cancel and
	// nothing to report. Anything else left a task running with nobody listening, and it
	// only ever costs a log line — the turn has already failed, for its own reason.
	if err != nil && connect.CodeOf(err) != connect.CodeFailedPrecondition {
		r.c.logger.ErrorContext(ctx, "cancelling the task the conductor stopped following failed",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "error", err)
	}
}

// finish records the turn, shows the outcome and counts it.
func (r *turnRun) finish(ctx context.Context, status string) {
	r.c.clearAwaiting(r.sess.ID, r.turn.TaskID)
	var numTurns *int
	var cost *float64
	if r.acct != nil {
		numTurns, cost = r.acct.NumTurns, r.acct.TotalCostUSD
		r.c.logger.DebugContext(ctx, "the turn's accounting arrived",
			"turn_id", r.turn.ID, "from", r.acctFrom)
	} else if status == store.TurnSucceeded {
		// The one case that used to be silent. A turn that ran and cost money but recorded
		// neither is a hole in the accounting, and an operator gets to see it.
		r.c.logger.WarnContext(ctx, "a turn succeeded but reported no accounting, so its "+
			"num_turns and cost_usd are unrecorded: neither the runtime's accounting message "+
			"nor its turn.json artifact arrived",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "playbook", r.job.name)
		r.c.metrics.TurnsWithoutAccounting.Inc()
	}
	// The turn is over, so its authority to mint a GitHub token is too. Mint already
	// refuses a capability whose work has finished; this removes the row as well.
	r.c.dropGitCapability(ctx, r.turn.ID)
	answer := r.answer()
	if err := r.c.store.FinishTurn(ctx, r.turn.ID, status, numTurns, cost, answer); err != nil {
		r.c.logger.ErrorContext(ctx, "recording how the turn ended failed",
			"turn_id", r.turn.ID, "status", status, "error", err)
	}
	r.linkPullRequests(ctx, answer)
	r.c.finish(ctx, r.src, r.ref, reactionFor(status))
	r.c.metrics.Turns.WithLabelValues(r.sess.SourceKind, r.job.name, status).Inc()
	r.c.metrics.TurnDuration.WithLabelValues(r.job.name).Observe(time.Since(r.startedAt).Seconds())
	r.c.logger.InfoContext(ctx, "turn finished", "turn_id", r.turn.ID, "task_id", r.turn.TaskID,
		"status", status, "playbook", r.job.name)
	// Last, deliberately: the answer is posted and the outcome is on the message, so a
	// memory outage costs a log line and nothing a human is waiting for.
	r.retain(ctx, status)
}

// linkPullRequests hands the source the pull requests this turn's answer named.
func (r *turnRun) linkPullRequests(ctx context.Context, answer string) {
	r.c.linkPullRequests(ctx, r.src, r.ref, answer, "turn_id", r.turn.ID)
}

// linkPullRequests hands the source the pull requests ONE answer named, whether that answer
// came from a turn or from a task the turn delegated.
//
// Both, and that is the whole reason this is not a method on turnRun any more. A conversation
// is answered by the assistant, which opens no pull requests — it has no repository and no
// shell — so every pull request this bot produces now comes out of a delegated task. Reading
// only the turn's own final_text meant the one place a link could appear was the one place it
// never did: the assistant announced "the task is running", the task answered with the URL,
// and the chat's pull-request bar stayed empty while the work sat in review.
//
// answer is the joined finals — byte for byte what FinishTurn or FinishDelegation just
// stored — and it is the right text to read for two reasons. It is the only thing that was
// said in full: progress lines are coalesced and superseded on the way out, so a link found
// in one would appear or not depending on how fast the runtime was talking. And it is what a
// human would have read: a pull request is announced in an answer, and one only muttered
// about on the way there is not the result.
//
// It never fails anything. The answer is already posted and the row is already recorded; a
// link that did not land costs a log line and a human can attach it by hand.
func (c *Conductor) linkPullRequests(ctx context.Context, src Source, ref, answer string, logArgs ...any) {
	linker, ok := src.(pullRequestLinker)
	if !ok {
		return
	}
	found := FindPullRequests(answer)
	if len(found) == 0 {
		return
	}
	if len(found) > maxPullRequestsPerTurn {
		c.logger.InfoContext(ctx, "more pull requests were named than one answer may link",
			append(logArgs, "found", len(found), "linked", maxPullRequestsPerTurn)...)
		found = found[:maxPullRequestsPerTurn]
	}
	if err := linker.LinkPullRequests(ctx, ref, found); err != nil {
		c.logger.WarnContext(ctx, "linking pull requests to the conversation failed",
			append(logArgs, "ref", ref, "error", err)...)
	}
}

// onEvent is the relay. Only MESSAGE events say anything; ERROR is remembered for the log;
// everything else, log chunks included, is ignored — the conductor is not a log consumer.
func (r *turnRun) onEvent(ctx context.Context, e *podiumv1.TaskEvent) {
	switch e.GetKind() {
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE:
		if msg := e.GetMessage(); msg.GetType() == MsgAccounting {
			// Deliberately not through the relayed ledger: accounting is never said out
			// loud, so exactly-once does not apply to it, and leaving its seq unclaimed is
			// what lets a resumed turn — which follows from the last seq it *relayed* —
			// receive it again.
			r.readAccounting(ctx, acctFromMessage, []byte(msg.GetText()))
			return
		}
		r.relay(ctx, e)
		if msg := e.GetMessage(); msg != nil {
			r.noteInteractive(ctx, msg.GetType())
		}
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR:
		if err := e.GetError(); err != nil && !err.GetRetryable() {
			r.errText = err.GetMessage()
		}
	default:
	}
}

// accounting is the runtime's turn.json, as much of it as the turns table keeps. It reaches
// the conductor either as a message or as the artifact, and both carry the same document.
type accounting struct {
	NumTurns     *int     `json:"num_turns"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
}

// readAccounting records what the runtime said the turn cost. A document that does not
// parse is a bug in the runtime, not a failed turn, so it is logged and dropped.
func (r *turnRun) readAccounting(ctx context.Context, from string, raw []byte) {
	var a accounting
	if err := json.Unmarshal(raw, &a); err != nil {
		r.c.logger.WarnContext(ctx, "the turn's accounting does not parse",
			"turn_id", r.turn.ID, "task_id", r.turn.TaskID, "from", from, "error", err)
		return
	}
	r.acct = &a
	r.acctFrom = from
}

// settle resolves the final's attachments against what the task actually stored, uploads
// them, and falls back to turn.json for the accounting when the message did not arrive. It
// runs once the task is terminal, because an artifact named in a message may still have
// been uploading when the message arrived.
func (r *turnRun) settle(ctx context.Context, task *podiumv1.Task) {
	// The artifact is only worth fetching when it is the last route left.
	wantSummary := r.acct == nil && task.GetStatus() == podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED
	_, canTitle := r.src.(autoTitler)
	wantTitle := canTitle && task.GetStatus() == podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED
	if len(r.attachments) == 0 && !wantSummary && !wantTitle {
		return
	}
	list, err := r.c.podium.ListArtifacts(ctx, r.turn.TaskID)
	if err != nil {
		r.c.logger.WarnContext(ctx, "listing the turn's artifacts failed",
			"task_id", r.turn.TaskID, "error", err)
		return
	}
	byName := make(map[string]*podiumv1.Artifact, len(list))
	for _, a := range list {
		byName[a.GetName()] = a
	}
	if art, ok := byName[turnSummaryArtifact]; wantSummary && ok {
		r.readSummary(ctx, art)
	}
	if art, ok := byName[chatTitleArtifact]; wantTitle && ok {
		r.applyChatTitle(ctx, art)
	}
	r.attachAll(ctx, byName, chatTitleArtifact)
}

// applyChatTitle reads the model-written name of a web chat. A title the caller supplied
// at create is left alone by the source; a missing or empty file is not an error.
func (r *turnRun) applyChatTitle(ctx context.Context, art *podiumv1.Artifact) {
	namer, ok := r.src.(autoTitler)
	if !ok {
		return
	}
	body, _, err := r.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the chat title failed",
			"artifact_id", art.GetId(), "error", err)
		return
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, 4<<10))
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the chat title failed",
			"artifact_id", art.GetId(), "error", err)
		return
	}
	if err := namer.SetAutoTitle(ctx, r.ref, string(raw)); err != nil {
		r.c.logger.WarnContext(ctx, "naming the chat from the turn failed",
			"ref", r.ref, "error", err)
	}
}

// readSummary pulls num_turns and total_cost_usd out of the turn.json artifact. It is small
// by construction, so a bounded read is honest rather than defensive.
func (r *turnRun) readSummary(ctx context.Context, art *podiumv1.Artifact) {
	body, _, err := r.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the turn summary failed",
			"artifact_id", art.GetId(), "error", err)
		return
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(body, 64<<10))
	if err != nil {
		r.c.logger.WarnContext(ctx, "reading the turn summary failed", "artifact_id", art.GetId(), "error", err)
		return
	}
	r.readAccounting(ctx, acctFromArtifact, raw)
}

// reactionFor is the mark a finished turn leaves on the message that started it: ✅ when it
// worked and ❌ when it did not, for every way it can not work — failed, lost, CANCELLED and
// timed out alike.
//
// Cancelled is ❌ on purpose and not a third state. A turn somebody stopped produced no
// answer, which is the only thing the mark is telling a reader; that they stopped it
// themselves is something they already know, and the words posted alongside say so anyway
// ("This was cancelled. Task ...").
func reactionFor(status string) Reaction {
	if status == store.TurnSucceeded {
		return ReactionDone
	}
	return ReactionFailed
}
