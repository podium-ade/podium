package slack

// A turn's thinking steps: one reply in the thread holding a plan block — Slack's own
// task-card UI — that is edited as the turn works. It replaces the "⏳ …" progress message,
// which said only the latest thing.
//
// It is CONDENSED. A turn can make hundreds of tool calls, and a thread is a conversation,
// not a log; the Podium chat keeps every step whole. So:
//
//   - consecutive tool calls of one kind are one card ("Reading app.go (+6 more)"), the
//     recent targets inside it;
//   - a progress line — the model's own words between tool calls — is a card of its own;
//   - a thought is never a card: the latest one is the plan's title while the turn works,
//     which is what "thinking" looks like without a line per thought;
//   - only the newest maxShownSteps cards are shown, everything before them folded into one
//     "N earlier steps" card, and the title counts them all.
//
// It is an ordinary message edited with chat.update, not one of Slack's streamed messages:
// a stream is finalised by Slack ten minutes after it starts, and a turn runs for longer
// than that. Slack accepts the same plan block on chat.postMessage and chat.update.
//
// A steps message ends when the turn says something durable — an answer, a failure, a
// question — so it always sits directly above what it led to. Steps after that (a turn
// carrying on once a question is answered) start a new one below. The turn's outcome
// (React) ends it too, and forgets it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

const (
	// maxShownSteps is the most cards one steps message shows, the "earlier steps" card
	// included. Slack's own cap is 50 tasks a plan; this is about what a reader takes in.
	maxShownSteps = 12
	// maxStepTitleRunes keeps a card's title, and the plan's, to one line. Slack caps a
	// streamed task's title at 256 characters and documents nothing for the block.
	maxStepTitleRunes = 200
	// maxStepDetailRunes is the most of a progress line kept under its title.
	maxStepDetailRunes = 2000
	// maxStepTargets is how many of a merged card's targets it lists, newest last.
	maxStepTargets = 8
	// stepsInterval is the least time between two edits of one steps message. A turn can
	// make several tool calls a second, and every write shares the one-per-second limiter
	// with the answer itself.
	stepsInterval = 2 * time.Second
	// stepsTTL is how long a steps message nobody ended is remembered. A turn always ends
	// with an outcome (React); this is only for a conductor that never sent one.
	stepsTTL = 24 * time.Hour
	// stepsFlushTimeout bounds one deferred edit, which has no caller's context to inherit.
	stepsFlushTimeout = 30 * time.Second
)

// stepsBlockID is the plan's block_id, the same on every post and edit (see plan).
const stepsBlockID = "podium-steps"

// stepsWorking is the plan's title while the turn works and has not thought out loud yet.
const stepsWorking = "Working on it"

// turnSteps is one steps message. Its mutex is held across the Slack calls that post and
// edit it, so two edits never race and an older plan never lands over a newer one.
type turnSteps struct {
	mu      sync.Mutex
	channel string
	thread  string
	// ts is the message, empty until it has been posted.
	ts      string
	started time.Time
	// cards are the newest cards; earlier is how many steps were folded out of them, and
	// earlierFailed how many of those failed.
	cards         []card
	earlier       int
	earlierFailed int
	// total is every step this message has shown, folded or not.
	total int
	next  int
	// thought is the latest thing the turn thought, the title while it works.
	thought string
	// final is the title once the message has ended, empty until then.
	final string
	// ended is a steps message that has said its last: the next step starts a new one.
	ended bool
	// gone is a message Slack will no longer edit (deleted by somebody, or refused). Its
	// steps are dropped rather than retried forever.
	gone     bool
	dirty    bool
	lastEdit time.Time
	timer    *time.Timer
}

// card is one task card: a run of tool calls of one kind, or one progress line.
type card struct {
	id string
	// verb is the tool calls' verb, empty for a progress line.
	verb string
	// targets are the most recent maxStepTargets targets, newest last. count is all of them.
	targets []string
	count   int
	// title and details are a progress line's.
	title   string
	details string
	failed  bool
}

// PostActivity implements conductor.ActivityPoster: a tool call joins or starts a card, and
// a thought becomes the plan's title.
func (s *Source) PostActivity(ctx context.Context, ref string, out conductor.Outbound) error {
	desc, ok := conductor.DescribeActivity(out.Text)
	if !ok {
		return nil
	}
	return s.withSteps(ctx, ref, func(st *turnSteps) {
		if desc.Thought {
			st.thought = conductor.Clip(desc.Target, maxStepTitleRunes)
			return
		}
		st.addTool(desc)
	})
}

// addProgress puts a progress line into the steps message. It reports false for a progress
// post that is not a turn's progress line: the notices the conductor posts as progress ("no
// artifact named …") carry no ⏳ and are said as messages, because somebody has to read them.
func (s *Source) addProgress(ctx context.Context, ref, text string) (bool, error) {
	body, ok := strings.CutPrefix(text, conductor.ProgressPrefix)
	body = strings.TrimSpace(body)
	if !ok || body == "" {
		return false, nil
	}
	c := card{title: conductor.Clip(conductor.FirstLine(body), maxStepTitleRunes)}
	if body != c.title {
		c.details = conductor.Clip(body, maxStepDetailRunes)
	}
	return true, s.withSteps(ctx, ref, func(st *turnSteps) { st.add(c) })
}

// withSteps changes the turn's steps message and shows the change, posting the message once
// it has a card, and editing it no oftener than Source.stepsEvery.
func (s *Source) withSteps(ctx context.Context, ref string, change func(*turnSteps)) error {
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return err
	}
	st := s.stepsFor(ref, channel, thread)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.gone {
		return nil
	}
	change(st)
	if len(st.cards) == 0 {
		// Only a thought so far: a plan with no tasks is nothing to post.
		return nil
	}
	st.dirty = true
	if wait := s.stepsEvery - time.Since(st.lastEdit); wait > 0 && st.ts != "" {
		if st.timer == nil {
			st.timer = time.AfterFunc(wait, func() { s.flushLater(st) })
		}
		return nil
	}
	return s.flushSteps(ctx, st)
}

// stepsFor is the turn's open steps message, a new one when it has none or its last one has
// ended.
func (s *Source) stepsFor(ref, channel, thread string) *turnSteps {
	s.stepsMu.Lock()
	defer s.stepsMu.Unlock()
	if st, ok := s.steps[ref]; ok {
		st.mu.Lock()
		ended := st.ended
		st.mu.Unlock()
		if !ended {
			return st
		}
	}
	now := time.Now()
	for key, old := range s.steps {
		if now.Sub(old.started) > stepsTTL {
			old.mu.Lock()
			old.stop()
			old.mu.Unlock()
			delete(s.steps, key)
		}
	}
	st := &turnSteps{channel: channel, thread: thread, started: now}
	if s.steps == nil {
		s.steps = map[string]*turnSteps{}
	}
	s.steps[ref] = st
	return st
}

// endSteps gives the turn's open steps message its last title and says it now, before
// whatever the turn says next is posted under it. forget drops it altogether: the turn is
// over.
func (s *Source) endSteps(ctx context.Context, ref string, failed, forget bool) {
	s.stepsMu.Lock()
	st, ok := s.steps[ref]
	if ok && forget {
		delete(s.steps, ref)
	}
	s.stepsMu.Unlock()
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if forget {
		defer st.stop()
	}
	if st.ended || st.gone {
		return
	}
	st.ended = true
	if st.ts == "" {
		// Nothing was ever posted (a turn that only thought): there is nothing to finish.
		return
	}
	verb := "Worked for "
	if failed {
		verb = "Stopped after "
	}
	st.final = verb + duration(time.Since(st.started)) + " · " + plural(st.total, "step")
	st.dirty = true
	if err := s.flushSteps(ctx, st); err != nil {
		s.logger.WarnContext(ctx, "finishing the steps message failed", "ref", ref, "error", err)
	}
}

// flushLater is the deferred edit Source.stepsEvery held back.
func (s *Source) flushLater(st *turnSteps) {
	ctx, cancel := context.WithTimeout(context.Background(), stepsFlushTimeout)
	defer cancel()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.timer = nil
	if err := s.flushSteps(ctx, st); err != nil {
		s.logger.WarnContext(ctx, "updating the steps message failed", "channel", st.channel, "error", err)
	}
}

// flushSteps posts or edits the message to show the steps so far. st.mu is held.
func (s *Source) flushSteps(ctx context.Context, st *turnSteps) error {
	if !st.dirty || st.gone {
		return nil
	}
	st.dirty = false
	st.lastEdit = time.Now()
	st.stop()
	title := st.title()
	opts := []slack.MsgOption{
		// The fallback text is what a notification and a screen reader say, and what a
		// transcript read reads: the ⏳ is what marks it as scaffolding there (isNoise).
		slack.MsgOptionText(conductor.ProgressPrefix+title, false),
		slack.MsgOptionBlocks(st.plan(title)),
	}
	if st.ts == "" {
		ts, err := s.write(ctx, func() (string, error) {
			_, ts, err := s.api.PostMessageContext(ctx, st.channel, append(opts, slack.MsgOptionTS(st.thread))...)
			return ts, err
		})
		if err != nil {
			return fmt.Errorf("slack: post the steps to %s/%s: %w", st.channel, st.thread, err)
		}
		st.ts = ts
		s.posted(st.channel, st.thread)
		return nil
	}
	_, err := s.write(ctx, func() (string, error) {
		_, ts, _, err := s.api.UpdateMessageContext(ctx, st.channel, st.ts, opts...)
		return ts, err
	})
	if err != nil {
		if isGone(err) {
			st.gone = true
		}
		return fmt.Errorf("slack: edit the steps %s in %s: %w", st.ts, st.channel, err)
	}
	return nil
}

// isGone reports whether Slack will never take an edit of this message again.
func isGone(err error) bool {
	var resp slack.SlackErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	switch resp.Err {
	case "message_not_found", "cant_update_message", "edit_window_closed":
		return true
	}
	return false
}

// addTool puts one tool call on the newest card when that card is a run of the same verb,
// and on a new card otherwise. A failure is a card of its own, so its error mark points at
// the call that failed and not at a run of calls that worked.
func (st *turnSteps) addTool(desc conductor.ActivityStep) {
	if n := len(st.cards); n > 0 && !desc.Failed {
		last := &st.cards[n-1]
		if last.verb == desc.Verb && !last.failed {
			last.push(desc.Target)
			st.total++
			return
		}
	}
	c := card{verb: desc.Verb, failed: desc.Failed}
	c.push(desc.Target)
	st.add(c)
}

// add puts one new card at the end, folding the oldest into the earlier count when there
// are more than a message shows.
func (st *turnSteps) add(c card) {
	st.next++
	c.id = fmt.Sprintf("s%d", st.next)
	if c.count == 0 {
		c.count = 1
	}
	st.total += c.count
	st.cards = append(st.cards, c)
	shown := maxShownSteps
	if st.earlier > 0 || len(st.cards) > maxShownSteps {
		// One place goes to the "earlier steps" card.
		shown--
	}
	for len(st.cards) > shown {
		st.earlier += st.cards[0].count
		if st.cards[0].failed {
			st.earlierFailed++
		}
		st.cards = st.cards[1:]
	}
}

// push adds one target to a run of tool calls.
func (c *card) push(target string) {
	c.count++
	if target == "" {
		return
	}
	c.targets = append(c.targets, target)
	if len(c.targets) > maxStepTargets {
		c.targets = c.targets[len(c.targets)-maxStepTargets:]
	}
}

// heading is the card's title: a progress line's own, or "Reading app.go (+6 more)" — the
// newest target, and how many calls the card stands for besides it.
func (c card) heading() string {
	if c.verb == "" {
		return c.title
	}
	line := c.verb
	if n := len(c.targets); n > 0 {
		line += " " + c.targets[n-1]
	}
	if c.count > 1 {
		if len(c.targets) > 0 {
			line += fmt.Sprintf(" (+%d more)", c.count-1)
		} else {
			line += fmt.Sprintf(" ×%d", c.count)
		}
	}
	return conductor.Clip(line, maxStepTitleRunes)
}

// detail is what opens under the card: a progress line's full text, or a run's recent
// targets, one a line.
func (c card) detail() string {
	if c.verb == "" {
		return c.details
	}
	if c.count < 2 || len(c.targets) < 2 {
		return ""
	}
	return strings.Join(c.targets, "\n")
}

// title is the plan's title: what the turn came to once it has ended, the latest thought
// while it works.
func (st *turnSteps) title() string {
	switch {
	case st.final != "":
		return st.final
	case st.thought != "":
		return st.thought
	default:
		return stepsWorking
	}
}

// plan is the message's one block.
func (st *turnSteps) plan(title string) slack.Block {
	tasks := make([]*slack.TaskCardBlock, 0, len(st.cards)+1)
	if st.earlier > 0 {
		// A failure folded out of sight is still said: in the title, not as an error mark
		// on a card that mostly stands for calls that worked.
		earlier := plural(st.earlier, "earlier step")
		if st.earlierFailed > 0 {
			earlier += fmt.Sprintf(" · %d failed", st.earlierFailed)
		}
		tasks = append(tasks, slack.NewTaskCardBlock("earlier", earlier).WithStatus(slack.TaskCardStatusComplete))
	}
	for _, c := range st.cards {
		status := slack.TaskCardStatusComplete
		if c.failed {
			status = slack.TaskCardStatusError
		}
		task := slack.NewTaskCardBlock(c.id, c.heading()).WithStatus(status)
		if d := c.detail(); d != "" {
			// rich_text is literal: nothing in it is read as markup or a mention, which is
			// what a task's own words need.
			task.WithDetails(slack.NewRichTextBlock("", slack.NewRichTextSection(
				slack.NewRichTextSectionTextElement(d, nil))))
		}
		tasks = append(tasks, task)
	}
	// One block_id for every edit: Slack keeps a plan somebody expanded open across an edit
	// only when its block_id is the same, and a new one (or none, which Slack fills in)
	// collapses it under them every time a step lands. Tried in a real client.
	return slack.NewPlanBlock(title, slack.PlanBlockOptionBlockID(stepsBlockID)).WithTasks(tasks...)
}

// stop cancels a deferred edit. st.mu is held.
func (st *turnSteps) stop() {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

// plural is "1 step", "143 steps".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// duration is how long a turn took, said the way a person would: "42s", "3m 5s", "1h 12m".
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// thinking sets or clears the thread's "is thinking…" status: Slack's own line under the
// thread, not a message, so it adds nothing to the conversation. Slack clears it by itself
// when the bot next posts there, so it covers the wait before the first step or the answer,
// and it is only cleared here for a turn that ended without posting anything. It is best
// effort: a refusal turns it off for good rather than costing every turn a call.
func (s *Source) thinking(ctx context.Context, ref string, on bool) {
	if s.statusOff.Load() {
		return
	}
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return
	}
	key := channel + "/" + thread
	s.stepsMu.Lock()
	showing := s.thinkingIn[key]
	if on {
		if s.thinkingIn == nil {
			s.thinkingIn = map[string]bool{}
		}
		s.thinkingIn[key] = true
	} else {
		delete(s.thinkingIn, key)
	}
	s.stepsMu.Unlock()
	if !on && !showing {
		return
	}
	status := ""
	if on {
		status = "is thinking…"
	}
	_, err = s.write(ctx, func() (string, error) {
		return "", s.api.SetAssistantThreadsStatusContext(ctx, slack.AssistantThreadsSetStatusParameters{
			ChannelID: channel, ThreadTS: thread, Status: status,
		})
	})
	var resp slack.SlackErrorResponse
	if errors.As(err, &resp) {
		s.statusOff.Store(true)
		s.logger.InfoContext(ctx, "slack refused the thinking status; not setting it again", "error", resp.Err)
	}
}

// posted notes that the bot said something in a thread, which clears its status on Slack's
// side.
func (s *Source) posted(channel, thread string) {
	s.stepsMu.Lock()
	delete(s.thinkingIn, channel+"/"+thread)
	s.stepsMu.Unlock()
}
