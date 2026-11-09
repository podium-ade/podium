package conductor

// Routines (docs/agent.md, "Routines"): a recurring job an agent does in one chat of its own.
//
// The scheduler (internal/agent/routines) fires a run by handing the routine's chat an event
// with Routine set. From there it is a turn like any other in that chat — the same session,
// the same record, the same queue — with three differences:
//
//   - its brief carries a routine block: the routine's name and schedule, the owner's
//     standing notes, the state the agent kept across runs, and how many runs since the last
//     one that did something found nothing to do. The instruction is the routine's own, and
//     it is never written into the record: a routine that runs every fifteen minutes would
//     otherwise repeat it a hundred times a day.
//   - it has a routine_finish tool, through which the agent says whether the run did
//     something, keeps its state, and proposes a standing note.
//   - what it says is held (RoutineHolder) until it ends, then written in under a divider.
//     A run that reported idle is kept quietly: its thinking, tool calls and answer are in the
//     chat for anyone reading back, but as rows that are not history — they do not mark the
//     chat unread and are never briefed to a later run. A run that did something is kept as
//     an ordinary answer.
//
// A person's own message in a routine chat is an ordinary turn of the chat. It gets the
// routine block too, so "why did you skip #42?" is answered with the state in view, and it
// may update the state and propose notes, but it is never held and never idle.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// RoutineRef is the scheduled run an event is. The scheduler sets it; nothing a person sends
// carries one.
type RoutineRef struct {
	RoutineID string `json:",omitempty"`
	RunID     string `json:",omitempty"`
}

// RoutineHolder is a source that can hold what a run says until the run ends. The web chat
// implements it.
type RoutineHolder interface {
	// HoldsRun reports whether a run's output is being held for this conversation.
	HoldsRun(ref string) bool
	// ReleaseRun ends the hold, writing what was held into the record under a divider saying
	// divider. quiet is a run that found nothing to do: what it said is kept as rows that are
	// not history, and answer — its final words, which are not posted as an answer — with them.
	ReleaseRun(ctx context.Context, ref string, quiet bool, divider, answer string)
}

// BriefRoutine is the routine a turn belongs to, as its brief carries it.
type BriefRoutine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Schedule is the schedule in words ("weekdays at 09:00").
	Schedule string `json:"schedule"`
	// Scheduled is true for a run the schedule started, false for a person's own message in
	// the routine's chat.
	Scheduled bool `json:"scheduled"`
	// Notes are the owner's standing instructions. They are as binding as the instruction.
	Notes string `json:"notes,omitempty"`
	// State is what the agent kept across runs, a JSON object.
	State json.RawMessage `json:"state"`
	// IdleRuns is how many runs since the last one that did something found nothing to do.
	IdleRuns int `json:"idle_runs,omitempty"`
	// InstructionChangedAt is set when the instruction was edited after the last run, so the
	// agent knows the runs above it followed an older one.
	InstructionChangedAt string `json:"instruction_changed_at,omitempty"`
	// System is a system routine: nobody owns it, and it reads GitHub as the App, in any
	// repository the App is installed on. Which ones it works on, its instruction says.
	System bool `json:"system,omitempty"`
}

// Outcomes routine_finish takes.
const (
	RoutineFinishIdle = "idle"
	RoutineFinishDone = "done"
)

// ErrInvalidRoutineFinish is a routine_finish whose arguments are wrong. The message says which.
var ErrInvalidRoutineFinish = errors.New("routine_finish: invalid arguments")

// ErrNotARoutine is routine_finish from a turn that is not in a routine's chat.
var ErrNotARoutine = errors.New("this conversation is not a routine, so there is nothing to finish; answer normally")

// RoutineFinishRequest is what routine_finish asks for.
type RoutineFinishRequest struct {
	// Status is RoutineFinishIdle or RoutineFinishDone.
	Status string
	// Summary is one line about the run, for the routine's run list.
	Summary string
	// State replaces the routine's state when set. Nil leaves it as it was.
	State json.RawMessage
	// ProposeNote is a standing instruction the agent suggests adding to the notes.
	ProposeNote string
}

// RoutineFinishTool is routine_finish as an MCP tool. The runtime's own server
// (agent/runtime/src/mcp.ts) offers it in the same words.
var RoutineFinishTool = chatTool{
	Name: "routine_finish",
	Description: "End this routine run, once, as your last step. status \"idle\" means there was nothing to do: " +
		"what you write stays in the routine's chat quietly, is not shown to later runs, and the person is not " +
		"notified, so use it whenever the run found nothing " +
		"worth telling them. status \"done\" means you did or found something: your answer is kept in the " +
		"routine's chat. summary is one line for the run list. state, when given, REPLACES the routine's " +
		"state — the exact facts the next run needs (what you already handled, by id) — so pass the whole " +
		"object, not a change. propose_note suggests a standing instruction for the owner to accept.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{RoutineFinishIdle, RoutineFinishDone},
				"description": "idle: nothing to do, nothing is kept. done: you did or found something worth keeping."},
			"summary": map[string]any{"type": "string", "description": "One line about this run, e.g. \"3 new PRs, 1 failing\"."},
			"state": map[string]any{"type": "object", "description": "The routine's whole state after this run. " +
				"Optional; leave it out to keep the state as it is. At most 16 KB."},
			"propose_note": map[string]any{"type": "string", "description": "A standing instruction the person gave " +
				"or that you think should hold for every run. Optional; the owner accepts or dismisses it."},
		},
		"required":             []string{"status", "summary"},
		"additionalProperties": false,
	},
}

// RoutineFinishArgs reads routine_finish's arguments.
func RoutineFinishArgs(args map[string]any) (RoutineFinishRequest, error) {
	var req RoutineFinishRequest
	req.Status, _ = args["status"].(string)
	req.Summary, _ = args["summary"].(string)
	req.ProposeNote, _ = args["propose_note"].(string)
	switch v := args["state"].(type) {
	case nil:
	case map[string]any:
		raw, err := json.Marshal(v)
		if err != nil {
			return req, fmt.Errorf("%w: state could not be encoded: %v", ErrInvalidRoutineFinish, err)
		}
		req.State = raw
	case string:
		if strings.TrimSpace(v) != "" {
			req.State = json.RawMessage(v)
		}
	default:
		return req, fmt.Errorf("%w: state must be a JSON object", ErrInvalidRoutineFinish)
	}
	return req, nil
}

// routineTurn is what this conductor knows about a scheduled run's turn while it runs.
type routineTurn struct {
	ref    RoutineRef
	chatID string
	// idle is set when the run reported it had nothing to do.
	idle bool
}

// noteRoutineTurn records that turnID is a scheduled run, on both rows and in memory.
func (c *Conductor) noteRoutineTurn(ctx context.Context, turnID string, ev InboundEvent) {
	if ev.Routine == nil || ev.Routine.RunID == "" {
		return
	}
	if err := c.store.LinkRoutineRunTurn(ctx, ev.Routine.RunID, turnID); err != nil {
		c.logger.WarnContext(ctx, "linking a routine run to its turn failed",
			"run_id", ev.Routine.RunID, "turn_id", turnID, "error", err)
	}
	c.mu.Lock()
	c.routineTurns[turnID] = &routineTurn{ref: *ev.Routine, chatID: ev.Ref}
	c.mu.Unlock()
}

// forgetRoutineTurn drops a finished turn's in-memory record.
func (c *Conductor) forgetRoutineTurn(turnID string) {
	c.mu.Lock()
	delete(c.routineTurns, turnID)
	c.mu.Unlock()
}

// routineIdle reports whether the scheduled run answering ref reported idle.
func (c *Conductor) routineIdle(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rt := range c.routineTurns {
		if rt.chatID == ref && rt.idle {
			return true
		}
	}
	return false
}

// releaseRoutine ends a held run's hold on ref. answer is the run's final words, "" when it
// ended without any. It reports whether the run was idle: its answer then went in quietly
// with the rest, which is what tells post to say nothing more.
func (c *Conductor) releaseRoutine(ctx context.Context, src Source, ref, answer string) (idle bool) {
	h, ok := src.(RoutineHolder)
	if !ok || !h.HoldsRun(ref) {
		return false
	}
	idle = c.routineIdle(ref)
	divider := "Routine run · " + time.Now().UTC().Format("Mon 2 Jan 15:04 MST")
	if idle {
		divider += " · nothing to do"
		h.ReleaseRun(ctx, ref, true, divider, answer)
		return true
	}
	h.ReleaseRun(ctx, ref, false, divider, "")
	return false
}

// briefRoutine is the routine block for a turn in a routine's chat, or nil for any other turn.
func (c *Conductor) briefRoutine(ctx context.Context, src Source, ev InboundEvent) *BriefRoutine {
	if c.store == nil || src == nil || src.Kind() != SourceChat || ev.Ref == "" {
		return nil
	}
	r, err := c.store.RoutineByChat(ctx, ev.Ref)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.logger.WarnContext(ctx, "reading a chat's routine failed; briefing without it",
				"chat_id", ev.Ref, "error", err)
		}
		return nil
	}
	b := &BriefRoutine{
		ID:        r.ID,
		Name:      r.Name,
		Schedule:  describeSchedule(r.Schedule),
		Scheduled: ev.Routine != nil,
		Notes:     r.Notes,
		State:     r.State,
		IdleRuns:  r.IdleCount,
		System:    r.System(),
	}
	if len(b.State) == 0 {
		b.State = json.RawMessage("{}")
	}
	if r.LastRunAt != nil && r.InstructionUpdatedAt.After(*r.LastRunAt) {
		b.InstructionChangedAt = BriefTimestamp(r.InstructionUpdatedAt)
	}
	return b
}

// DescribeSchedule says a schedule in words. It is set by the agent package to the routines
// package's Describe, which this package cannot import without a cycle.
var DescribeSchedule func(expr string) string

func describeSchedule(expr string) string {
	if DescribeSchedule != nil {
		return DescribeSchedule(expr)
	}
	return expr
}

// routineMenu is the delegation menu for a scheduled run: nobody is watching a routine, so a
// playbook that waits on a person — a desktop someone watches, an interactive question — is
// left off. A person's own message in the chat gets the whole menu.
func (c *Conductor) routineMenu(menu []DelegablePlaybook, ev InboundEvent) []DelegablePlaybook {
	if ev.Routine == nil {
		return menu
	}
	playbooks := c.profiles.Current().Playbooks
	out := make([]DelegablePlaybook, 0, len(menu))
	for _, p := range menu {
		if p.Desktop || playbooks[p.Name].Interactive || playbooks[p.Name].Desktop {
			continue
		}
		out = append(out, p)
	}
	return out
}

// FinishRoutine is routine_finish for the turn token holds.
func (c *Conductor) FinishRoutine(ctx context.Context, token string, req RoutineFinishRequest) (string, error) {
	g, ok := c.grantFor(ctx, token)
	if !ok {
		return "", ErrNoTurn
	}
	if c.store == nil || g.src == nil || g.src.Kind() != SourceChat {
		return "", ErrNotARoutine
	}
	r, err := c.store.RoutineByChat(ctx, g.ref)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotARoutine
	}
	if err != nil {
		return "", err
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if status != RoutineFinishIdle && status != RoutineFinishDone {
		return "", fmt.Errorf("%w: status %q is not idle or done", ErrInvalidRoutineFinish, req.Status)
	}
	if strings.TrimSpace(req.Summary) == "" {
		return "", fmt.Errorf("%w: summary is required: one line about this run", ErrInvalidRoutineFinish)
	}
	if len(req.State) > 0 {
		clean, err := store.CleanRoutineState(req.State)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRoutineFinish, err)
		}
		if _, err := c.store.SetRoutineState(ctx, r.ID, clean); err != nil {
			return "", err
		}
	}
	if note := strings.TrimSpace(req.ProposeNote); note != "" {
		if err := c.store.ProposeRoutineNote(ctx, r.ID, note); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRoutineFinish, err)
		}
	}

	c.mu.Lock()
	rt := c.routineTurns[g.turnID]
	if rt != nil {
		rt.idle = status == RoutineFinishIdle
	}
	c.mu.Unlock()
	if rt == nil {
		// A person's own message in the routine chat: there is no run to report on, and the
		// answer is theirs to read whatever it says.
		return "Saved. This was a reply to the person rather than a scheduled run, so your answer is shown as usual.", nil
	}
	if err := c.store.ReportRoutineRun(ctx, rt.ref.RunID, status, req.Summary); err != nil {
		return "", err
	}
	if status == RoutineFinishIdle {
		return "Recorded as idle. This run stays in the chat quietly and nobody is notified; end your turn now " +
			"without further work.", nil
	}
	return "Recorded. End your turn with your answer: it is kept in the routine's chat.", nil
}

// MaxChatsStartedPerRoutineDay bounds start_chat for a scheduled run: a routine's runs open at
// most this many chats in any 24 hours, whatever each run's own cap allows.
const MaxChatsStartedPerRoutineDay = 3

// ErrRoutineChatCap is a routine that has already opened its chats for the day.
var ErrRoutineChatCap = fmt.Errorf("this routine has already opened %d chats in the last 24 hours, which is as many as "+
	"a routine may; say what you would have opened it for in your answer instead", MaxChatsStartedPerRoutineDay)

// checkRoutineChatCap refuses start_chat from a scheduled run whose routine is at its cap.
func (c *Conductor) checkRoutineChatCap(ctx context.Context, turnID string) error {
	c.mu.Lock()
	rt := c.routineTurns[turnID]
	c.mu.Unlock()
	if rt == nil {
		return nil
	}
	n, err := c.store.CountChatsStartedByRoutineSince(ctx, rt.ref.RoutineID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if n >= MaxChatsStartedPerRoutineDay {
		return ErrRoutineChatCap
	}
	return nil
}

// inRoutineChat reports whether the turn token holds is in a routine's chat, which is when
// routine_finish is offered.
func (c *Conductor) inRoutineChat(ctx context.Context, token string) bool {
	g, ok := c.grantFor(ctx, token)
	if !ok || c.store == nil || g.src == nil || g.src.Kind() != SourceChat {
		return false
	}
	_, err := c.store.RoutineByChat(ctx, g.ref)
	return err == nil
}

// RoutineCreator opens a routine for a person, checked as their own request would be. The API
// service implements it; it is handed over by SetRoutineCreator because it is built after the
// conductor.
type RoutineCreator interface {
	CreateRoutineFor(ctx context.Context, owner string, d store.RoutineDraft) (store.Routine, error)
}

// SetRoutineCreator hands the conductor what create_routine creates routines with. Until it is
// called, create_routine is not offered.
func (c *Conductor) SetRoutineCreator(rc RoutineCreator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routineCreator = rc
}

func (c *Conductor) creator() RoutineCreator {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.routineCreator
}

// ErrInvalidCreateRoutine is a create_routine whose arguments are wrong. The message says which.
var ErrInvalidCreateRoutine = errors.New("create_routine: invalid arguments")

// ErrNoRoutineHere is create_routine from a turn that may not create one: not a web chat, a
// routine's own chat, or a turn no person started.
var ErrNoRoutineHere = errors.New("a routine can only be set up from a person's own web chat; tell them to " +
	"create it on the Routines page instead")

// CreateRoutineRequest is what create_routine asks for.
type CreateRoutineRequest struct {
	Name        string
	Instruction string
	Schedule    string
	Timezone    string
	Notes       string
}

// CreateRoutineTool is create_routine as an MCP tool. The runtime's own server
// (agent/runtime/src/mcp.ts) offers it in the same words.
var CreateRoutineTool = chatTool{
	Name: "create_routine",
	Description: "Set up a routine for the person you are talking to: a job an assistant does again on a " +
		"schedule, with nobody present, in a chat of its own where its answers land. Use it only when they " +
		"ask for something recurring (\"every morning\", \"each Monday\", \"keep an eye on\") and you have " +
		"agreed what it does and when; ask first if either is unclear. instruction is the complete brief for " +
		"ONE run, written to the assistant in the second person: what to look at, what to do, what to " +
		"report, and to report nothing when there is nothing new — it will not see this conversation. " +
		"schedule is a five-field cron expression in timezone, never more often than every 15 minutes. " +
		"The routine is theirs; they can pause, edit or delete it on the Routines page.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "2-5 words, e.g. \"Failing PR digest\"."},
			"instruction": map[string]any{"type": "string", "description": "The complete brief for one run, " +
				"self-contained: the run does not see this conversation."},
			"schedule": map[string]any{"type": "string", "description": "Five-field cron, e.g. \"0 9 * * 1-5\" " +
				"for weekdays at 09:00. At most every 15 minutes."},
			"timezone": map[string]any{"type": "string", "description": "IANA timezone the schedule is read in, " +
				"e.g. \"America/Toronto\". Ask the person when the time of day matters and you do not know it. " +
				"Empty is UTC."},
			"notes": map[string]any{"type": "string", "description": "Standing instructions the person gave, " +
				"one per line starting \"- \". Optional."},
		},
		"required":             []string{"name", "instruction", "schedule"},
		"additionalProperties": false,
	},
}

// CreateRoutineArgs reads create_routine's arguments.
func CreateRoutineArgs(args map[string]any) (CreateRoutineRequest, error) {
	var req CreateRoutineRequest
	for key, dst := range map[string]*string{
		"name": &req.Name, "instruction": &req.Instruction, "schedule": &req.Schedule,
		"timezone": &req.Timezone, "notes": &req.Notes,
	} {
		switch v := args[key].(type) {
		case nil:
		case string:
			*dst = v
		default:
			return req, fmt.Errorf("%w: %s must be a string", ErrInvalidCreateRoutine, key)
		}
	}
	return req, nil
}

// mayCreateRoutine reports whether the turn token holds may call create_routine: a turn a
// person started in a web chat that is not itself a routine's.
func (c *Conductor) mayCreateRoutine(ctx context.Context, token string) (turnGrant, bool) {
	g, ok := c.grantFor(ctx, token)
	if !ok || c.store == nil || c.creator() == nil || g.src == nil || g.src.Kind() != SourceChat || g.login == "" {
		return g, false
	}
	if _, err := c.store.RoutineByChat(ctx, g.ref); err == nil {
		return g, false
	}
	return g, true
}

// CreateRoutine is create_routine for the turn token holds. The routine is the person's who
// started the turn, with this chat's assistant when that assistant is theirs.
func (c *Conductor) CreateRoutine(ctx context.Context, token string, req CreateRoutineRequest) (string, error) {
	if _, ok := c.grantFor(ctx, token); !ok {
		return "", ErrNoTurn
	}
	g, ok := c.mayCreateRoutine(ctx, token)
	if !ok {
		return "", ErrNoRoutineHere
	}
	switch {
	case strings.TrimSpace(req.Name) == "":
		return "", fmt.Errorf("%w: name is required", ErrInvalidCreateRoutine)
	case strings.TrimSpace(req.Instruction) == "":
		return "", fmt.Errorf("%w: instruction is required: the complete brief for one run", ErrInvalidCreateRoutine)
	case strings.TrimSpace(req.Schedule) == "":
		return "", fmt.Errorf("%w: schedule is required: a five-field cron expression", ErrInvalidCreateRoutine)
	}
	draft := store.RoutineDraft{
		Name: strings.TrimSpace(req.Name), Instruction: strings.TrimSpace(req.Instruction),
		Schedule: strings.TrimSpace(req.Schedule), Timezone: strings.TrimSpace(req.Timezone),
		Notes: strings.TrimSpace(req.Notes),
	}
	if chat, err := c.store.GetChat(ctx, g.ref); err == nil && chat.PersonalityID != "" {
		if p, err := c.store.GetPersonality(ctx, chat.PersonalityID); err == nil && p.Login == g.login {
			draft.PersonalityID = p.ID
		}
	}
	r, err := c.creator().CreateRoutineFor(ctx, g.login, draft)
	if err != nil {
		// What the person asked for cannot be set up as asked; the reason is for the model to
		// fix or to tell them.
		return "", fmt.Errorf("%w: %v", ErrInvalidCreateRoutine, err)
	}
	pub, _ := c.mirror.(chatPublisher)
	next := "paused"
	if r.Enabled && !r.NextRunAt.IsZero() {
		next = r.NextRunAt.In(routineLocation(r.Timezone)).Format("Mon 2 Jan 15:04 MST")
	}
	return fmt.Sprintf("Created the routine %q.\nruns: %s (%s)\nfirst run: %s\nurl: %s\n\n"+
		"Tell the person it is set up, when it first runs, and that its answers land in its own chat "+
		"([open it](%s)), where they can also pause, edit or delete it — or on the Routines page.",
		r.Name, describeSchedule(r.Schedule), r.Timezone, next, chatPath(pub, r.ChatID), chatPath(pub, r.ChatID)), nil
}

func routineLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}
