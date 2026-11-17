package conductor

// The watchdog notices a task that has gone quiet and asks the person in its conversation
// what to do about it: wait, restart or end. It never decides on its own. A task nobody
// answers about keeps running until its own timeout, which is what it would have done anyway.
//
// Quiet is two different things, and the question says which:
//
//   - quiet: the runtime's heartbeat still arrives every minute but the task has reported
//     no work for WatchdogQuietAfter. The harness reports a tool only once it has finished,
//     so this is very often a long test run and not a fault. Restarting interrupts the
//     harness and has it carry on in the same session (main.ts's steer), so nothing it
//     knew is lost.
//   - wedged: the heartbeat itself has stopped. The runtime's event loop is not running
//     (a task once spent seven quiet minutes like this and then ran out of heap), so an
//     inject would never be read. Restarting cancels the task and starts a new one from
//     the same instruction.
//
// The watch is in memory, like the follow it rides on. A conductor that restarts or adopts
// a task starts watching it afresh from the events it replays.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// MsgHeartbeat is the runtime's liveness message (agent/runtime/src/heartbeat.ts). It is
// never relayed, never recorded as progress and never shown.
const MsgHeartbeat = "heartbeat"

// DefaultWatchdogWedgedAfter is how long without a heartbeat a task counts as wedged. The
// runtime sends one a minute, so three missed is not a slow runner.
const DefaultWatchdogWedgedAfter = 3 * time.Minute

// watchdogTick is how often the watchdog looks.
const watchdogTick = time.Minute

// watchdogChoice is the person's answer to the watchdog's question.
type watchdogChoice string

const (
	watchdogWait    watchdogChoice = "wait"
	watchdogRestart watchdogChoice = "restart"
	watchdogEnd     watchdogChoice = "end"
)

// parseWatchdogChoice reads a reply as an answer to the watchdog, or reports that it is not
// one. Only the whole message counts: "restart the build with -race" is an instruction for
// the task, not a choice.
func parseWatchdogChoice(text string) (watchdogChoice, bool) {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.Trim(s, " \t\r\n.!*`'\"")
	switch s {
	case "wait", "keep waiting", "leave it":
		return watchdogWait, true
	case "restart", "retry":
		return watchdogRestart, true
	case "end", "stop", "cancel", "kill":
		return watchdogEnd, true
	}
	return "", false
}

// watch is one followed task, seen by the watchdog.
type watch struct {
	taskID    string
	sessionID string
	playbook  string
	// sink is where the question is posted. Only its conversation is used (src, ref,
	// speaker), which never changes after the run starts.
	sink *sink
	// restart starts the task's work again in a new task after the old one was cancelled.
	// Nil for a run that cannot be started again from what the conductor kept.
	restart func(ctx context.Context) error

	mu            sync.Mutex
	started       bool
	lastActivity  time.Time
	lastHeartbeat time.Time
	lastStep      string
	// answered is set by the task's answer and cleared by anything it says after it: a task
	// that has answered is only waiting for a follow-up, and quiet because it should be.
	answered bool
	// askedAt is when the question went out, zero when none is outstanding. waitFrom is
	// when the person last said wait: quiet is measured from it, so the question comes back
	// only after another full interval of nothing.
	askedAt     time.Time
	askedWedged bool
	waitFrom    time.Time
}

// observe takes one event of the task. Everything is timed by the event, not by when it was
// read, so a follow that resumes and reads a backlog does not mistake old news for new.
func (w *watch) observe(e *podiumv1.TaskEvent) (resumed bool) {
	at := time.Now()
	if ts := e.GetTs(); ts != nil && ts.AsTime().Before(at) {
		at = ts.AsTime()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch e.GetKind() {
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED:
		w.started = true
		w.lastActivity = at
	case podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE:
		msg := e.GetMessage()
		if msg.GetType() == MsgHeartbeat {
			w.started = true
			w.lastHeartbeat = at
			return false
		}
		w.started = true
		w.lastActivity = at
		w.answered = msg.GetType() == OutFinal
		if msg.GetType() == MsgActivity {
			var a struct {
				Title string `json:"title"`
				Tool  string `json:"tool"`
			}
			if json.Unmarshal([]byte(msg.GetText()), &a) == nil {
				w.lastStep = strings.TrimSpace(cmp.Or(a.Title, a.Tool))
			}
		}
		// The task spoke after the question went out: it is moving again, and the question
		// no longer needs an answer.
		if !w.askedAt.IsZero() && at.After(w.askedAt) {
			w.askedAt = time.Time{}
			return true
		}
	default:
	}
	return false
}

// verdict is what the watchdog makes of a watch now: whether to ask, and whether the task
// is wedged rather than only quiet.
func (w *watch) verdict(now time.Time, quietAfter, wedgedAfter time.Duration) (ask, wedged bool, quiet time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started || w.answered || !w.askedAt.IsZero() {
		return false, false, 0
	}
	// Somebody said wait: whatever kind of quiet it is, they hear about it again only after
	// another full interval.
	if !w.waitFrom.IsZero() && now.Sub(w.waitFrom) <= quietAfter {
		return false, false, 0
	}
	// A runtime from before heartbeats never sends one; it can only ever be quiet.
	wedged = !w.lastHeartbeat.IsZero() && now.Sub(w.lastHeartbeat) > wedgedAfter
	if wedged || now.Sub(w.lastActivity) > quietAfter {
		return true, wedged, now.Sub(w.lastActivity)
	}
	return false, false, 0
}

// startWatch registers a followed task with the watchdog and returns the call that stops
// watching it. With the watchdog off it registers nothing.
func (c *Conductor) startWatch(w *watch) func() {
	if c.watchdogQuietAfter <= 0 || w.taskID == "" {
		return func() {}
	}
	c.mu.Lock()
	if c.watches == nil {
		c.watches = map[string]*watch{}
	}
	c.watches[w.taskID] = w
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.watches[w.taskID] == w {
			delete(c.watches, w.taskID)
		}
		if st := c.sessions[w.sessionID]; st != nil && st.watchdogTaskID == w.taskID {
			st.watchdogTaskID = ""
		}
		c.mu.Unlock()
	}
}

// observeWatch feeds one event to the task's watch, if it has one, and says so in the
// conversation when a task that was asked about starts moving again.
func (c *Conductor) observeWatch(ctx context.Context, w *watch, e *podiumv1.TaskEvent) {
	if w == nil || !w.observe(e) {
		return
	}
	c.mu.Lock()
	if st := c.sessions[w.sessionID]; st != nil && st.watchdogTaskID == w.taskID {
		st.watchdogTaskID = ""
	}
	c.mu.Unlock()
	c.post(ctx, w.sink.src, w.sink.ref, Outbound{Type: OutProgress, TaskID: w.taskID, Speaker: w.sink.speaker,
		Text: "It's moving again, so there is nothing to answer."})
	_ = w.sink.src.React(ctx, w.sink.ref, ReactionWorking)
}

// watchQuietTasks is the watchdog's loop.
func (c *Conductor) watchQuietTasks(ctx context.Context) {
	t := time.NewTicker(watchdogTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.checkQuietTasks(ctx, time.Now())
		}
	}
}

// checkQuietTasks is one look at every watched task, separate from the loop so a test can
// run one without a clock.
func (c *Conductor) checkQuietTasks(ctx context.Context, now time.Time) {
	c.mu.Lock()
	watches := make([]*watch, 0, len(c.watches))
	for _, w := range c.watches {
		// A task waiting on a person, or one that has answered and is up for a follow-up,
		// is quiet because it should be.
		if st := c.sessions[w.sessionID]; st != nil &&
			(st.awaitingTaskID == w.taskID || st.warmTaskID == w.taskID || st.watchdogTaskID != "") {
			continue
		}
		watches = append(watches, w)
	}
	c.mu.Unlock()

	for _, w := range watches {
		ask, wedged, quiet := w.verdict(now, c.watchdogQuietAfter, c.watchdogWedgedAfter)
		if !ask {
			continue
		}
		c.askAboutQuietTask(ctx, w, wedged, quiet, now)
	}
}

// askAboutQuietTask puts the question to the conversation and parks the session on it: the
// next reply that reads as a choice is the watchdog's, not the task's (accept).
func (c *Conductor) askAboutQuietTask(ctx context.Context, w *watch, wedged bool, quiet time.Duration, now time.Time) {
	c.mu.Lock()
	st := c.state(w.sessionID)
	if st.watchdogTaskID != "" {
		c.mu.Unlock()
		return
	}
	st.watchdogTaskID = w.taskID
	c.mu.Unlock()

	w.mu.Lock()
	w.askedAt = now
	w.askedWedged = wedged
	step := w.lastStep
	w.mu.Unlock()

	c.metrics.WatchdogQuestions.WithLabelValues(watchdogKind(wedged)).Inc()
	c.logger.InfoContext(ctx, "asking about a quiet task", "task_id", w.taskID, "session_id", w.sessionID,
		"wedged", wedged, "quiet", quiet.Round(time.Second))
	c.post(ctx, w.sink.src, w.sink.ref, Outbound{
		Type: OutQuestion, TaskID: w.taskID, Speaker: w.sink.speaker,
		Text: watchdogQuestion(w.playbook, step, wedged, quiet),
	})
	if err := w.sink.src.React(ctx, w.sink.ref, ReactionAwaiting); err != nil {
		c.logger.WarnContext(ctx, "marking a quiet task as awaiting an answer failed", "task_id", w.taskID, "error", err)
	}
}

func watchdogKind(wedged bool) string {
	if wedged {
		return "wedged"
	}
	return "quiet"
}

// watchdogQuestion is the question, in the conversation's words.
func watchdogQuestion(playbook, step string, wedged bool, quiet time.Duration) string {
	name := "The task"
	if playbook != "" {
		name = fmt.Sprintf("The `%s` task", playbook)
	}
	mins := max(1, int(quiet.Round(time.Minute)/time.Minute))
	var b strings.Builder
	if wedged {
		fmt.Fprintf(&b, "%s has stopped responding: nothing from it, not even the heartbeat its runtime sends every minute, for %d minutes.", name, mins)
		if step != "" {
			fmt.Fprintf(&b, " The last thing it finished was `%s`.", oneLine(step))
		}
		b.WriteString("\n\nReply **wait** to leave it, **restart** to end it and start it again from its instruction, or **end** to stop it.")
		return b.String()
	}
	fmt.Fprintf(&b, "%s has reported nothing for %d minutes.", name, mins)
	if step != "" {
		fmt.Fprintf(&b, " The last thing it finished was `%s`.", oneLine(step))
	}
	b.WriteString(" Its runtime is still alive, so it is most likely inside a long step, such as a test run, that has not finished yet.")
	b.WriteString("\n\nReply **wait** to leave it, **restart** to interrupt it and have it carry on from where it was, or **end** to stop it.")
	return b.String()
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.ReplaceAll(s, "`", "'")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

// takeWatchdogReply reports whether ev answers a watchdog question this session is parked
// on, and if it does, unparks it and returns the task and the choice. The caller holds c.mu.
func (c *Conductor) takeWatchdogReply(st *sessionState, src Source, ev InboundEvent, same bool) (string, watchdogChoice, bool) {
	if st.watchdogTaskID == "" || (!same && !c.answersQuestion(st, src, ev)) {
		return "", "", false
	}
	choice, ok := parseWatchdogChoice(ev.Text)
	if !ok {
		return "", "", false
	}
	taskID := st.watchdogTaskID
	st.watchdogTaskID = ""
	return taskID, choice, true
}

// answerWatchdog carries out the person's choice for a task the watchdog asked about.
func (c *Conductor) answerWatchdog(ctx context.Context, taskID string, choice watchdogChoice, login string) {
	c.mu.Lock()
	w := c.watches[taskID]
	c.mu.Unlock()
	if w == nil {
		// It ended between the question and the answer; the run already said how.
		return
	}
	w.mu.Lock()
	wedged := w.askedWedged
	quiet := time.Since(w.lastActivity)
	w.askedAt = time.Time{}
	if choice == watchdogWait {
		w.waitFrom = time.Now()
	}
	w.mu.Unlock()

	say := func(text string) {
		c.post(ctx, w.sink.src, w.sink.ref, Outbound{Type: OutProgress, TaskID: taskID, Speaker: w.sink.speaker, Text: text})
	}
	_ = w.sink.src.React(ctx, w.sink.ref, ReactionWorking)
	c.metrics.WatchdogAnswers.WithLabelValues(string(choice)).Inc()
	c.logger.InfoContext(ctx, "a person answered the watchdog", "task_id", taskID, "choice", string(choice),
		"wedged", wedged, "login", login)

	switch choice {
	case watchdogWait:
		say(fmt.Sprintf("Leaving it running. I'll ask again if it is still quiet in %s.", c.watchdogQuietAfter))

	case watchdogEnd:
		if err := c.podium.CancelTask(ctx, taskID, "ended after it went quiet"); err != nil {
			c.logger.WarnContext(ctx, "ending a quiet task failed", "task_id", taskID, "error", err)
			say("I couldn't end it: " + err.Error())
			return
		}
		say("Ending it.")

	case watchdogRestart:
		if !wedged {
			nudge := fmt.Sprintf("You had reported nothing for %d minutes, and the person asked me to "+
				"restart you. Whatever you were waiting on has been interrupted. Check where things "+
				"stand, then carry on with the work.", max(1, int(quiet.Round(time.Minute)/time.Minute)))
			err := c.podium.InjectTask(ctx, taskID, nudge)
			if err == nil {
				say("Interrupting it; it will carry on from where it was.")
				return
			}
			c.logger.WarnContext(ctx, "interrupting a quiet task failed; ending it and starting again",
				"task_id", taskID, "error", err)
		}
		if w.restart == nil {
			if err := c.podium.CancelTask(ctx, taskID, "ended after it stopped responding"); err != nil {
				say("I couldn't end it: " + err.Error())
				return
			}
			say("Its runtime isn't responding, so it can't be interrupted, and this kind of task can't be started again on its own. I've ended it: send your message again to start over.")
			return
		}
		if err := c.podium.CancelTask(ctx, taskID, "restarted after it stopped responding"); err != nil {
			say("I couldn't end it to restart it: " + err.Error())
			return
		}
		if err := w.restart(ctx); err != nil {
			c.logger.WarnContext(ctx, "restarting a wedged task failed", "task_id", taskID, "error", err)
			say("I ended it, but starting it again failed: " + err.Error())
			return
		}
		say("Ended it and started it again from its instruction, in a new task.")
	}
}

// restartedInstruction is a restarted task's instruction: the same work, with a word about
// the workspace a wedged predecessor may have left half done.
func restartedInstruction(instruction string) string {
	return "An earlier task with this instruction stopped responding and was ended. The workspace may " +
		"hold its unfinished work: look at what is there before you start, and keep what is good.\n\n" + instruction
}
