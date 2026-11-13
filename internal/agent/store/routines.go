package store

// Routines (0043): a recurring job an agent does, in one chat of its own. The schedule's
// arithmetic lives in internal/agent/routines; this file stores what it decides.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// ErrInvalidRoutine is a routine the store will not write. The handler maps it to
// InvalidArgument.
var ErrInvalidRoutine = errors.New("invalid routine")

// Routine scopes. Personal is the only one a person can create in this phase.
const (
	RoutineScopePersonal = "personal"
	RoutineScopeSystem   = "system"
)

// What starts a routine's runs: its schedule, or Linear events (routine_events.go).
const (
	RoutineKindSchedule = "schedule"
	RoutineKindLinear   = "linear"
)

// Where a routine's runs happen (0047). Every run of a routine on a schedule is in its one
// chat; a routine on Linear events may instead run in a chat per subject — the issue, project,
// cycle or label its events are about — each keeping that subject's history.
const (
	RoutineChatModeRoutine = "routine"
	RoutineChatModeSubject = "subject"
)

// Run triggers.
const (
	RoutineTriggerSchedule = "schedule"
	RoutineTriggerManual   = "manual"
	// RoutineTriggerEvent is a run of a linear routine, started by the events it was given.
	RoutineTriggerEvent = "event"
)

// Run statuses. Running is the only one that is not final.
const (
	RoutineRunRunning        = "running"
	RoutineRunDone           = "done"
	RoutineRunIdle           = "idle"
	RoutineRunFailed         = "failed"
	RoutineRunSkippedOverlap = "skipped_overlap"
	RoutineRunSkippedPaused  = "skipped_paused"
)

// Limits on what a routine holds. The instruction and the notes go into every brief, and the
// state goes into every brief and is rewritten by every run, so each is capped.
const (
	MaxRoutineNameRunes        = 80
	MaxRoutineInstructionRunes = 8000
	MaxRoutineNotesRunes       = 4000
	MaxRoutineStateBytes       = 16 * 1024
	// MaxRoutineRepos bounds a system routine's repositories: each is read through the App.
	MaxRoutineRepos = 20
)

// Routine is one recurring job.
type Routine struct {
	ID         string
	Scope      string
	OwnerLogin string
	Name       string
	// Kind is RoutineKindSchedule or RoutineKindLinear: what starts its runs.
	Kind string
	// Schedule is a five-field cron expression, read in Timezone. Empty for a linear routine,
	// which keeps Timezone for its daily spend cap.
	Schedule string
	Timezone string
	// Events are the Linear events a linear routine runs on, "Issue.create" each, and Teams
	// the Linear team keys it is limited to, empty for every team. Both are empty for a
	// routine on a schedule.
	Events []string
	Teams  []string
	// ChatMode is RoutineChatModeRoutine or RoutineChatModeSubject: whether a linear routine's
	// runs are all in ChatID or each in the chat of what its events are about.
	ChatMode             string
	Instruction          string
	InstructionUpdatedAt time.Time
	// Notes are the owner's standing instructions, briefed with every run. ProposedNote is one
	// the agent suggested and the owner has not yet accepted or dismissed.
	Notes         string
	ProposedNote  string
	PersonalityID string
	Model         string
	Effort        string
	Enabled       bool
	PausedReason  string
	// State is the agent's JSON object across runs, "{}" when it has kept nothing.
	State          json.RawMessage
	StateUpdatedAt *time.Time
	ChatID         string
	NextRunAt      time.Time
	LastRunAt      *time.Time
	// IdleCount is how many runs in a row, since the last one that did something, found
	// nothing to do.
	IdleCount int
	// Repos is a system routine's GitHub reach, owner/name each. Empty for a personal one.
	Repos []string
	// DailyCapUSD is what its runs may spend in a day in its timezone; nil is no cap of its own.
	DailyCapUSD *float64
	// PausedUntil is when a routine Podium paused for the day resumes by itself.
	PausedUntil *time.Time
	// CreatedBy is who set it up.
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// System reports whether the routine runs as nobody, configured by admins.
func (r Routine) System() bool { return r.Scope == RoutineScopeSystem }

// OnLinear reports whether the routine runs on Linear events rather than a schedule.
func (r Routine) OnLinear() bool { return r.Kind == RoutineKindLinear }

// PerSubject reports whether the routine's runs on Linear events each happen in the chat of
// what the events are about, rather than all in its own chat.
func (r Routine) PerSubject() bool { return r.OnLinear() && r.ChatMode == RoutineChatModeSubject }

// When is what starts the routine's runs as its history records it: the cron expression, or
// the Linear events in words. Nothing parses it.
//
// A chat per ticket is part of it, so that switching it is a change the history records.
func (r Routine) When() string {
	if r.PerSubject() {
		return DescribeLinearTrigger(r.Events, r.Teams) + " · a chat per ticket"
	}
	if r.OnLinear() {
		return DescribeLinearTrigger(r.Events, r.Teams)
	}
	return r.Schedule
}

// RoutineDraft is what a person saves. The schedule has already been checked and NextRunAt
// worked out by the caller, which owns the cron arithmetic.
type RoutineDraft struct {
	Name string
	// Kind is RoutineKindSchedule (or empty, which is the same) or RoutineKindLinear. A linear
	// draft names Events and, optionally, Teams, and no Schedule.
	Kind     string
	Schedule string
	Events   []string
	Teams    []string
	// ChatMode is where a linear draft's runs happen: RoutineChatModeRoutine (or empty, which
	// is the same) or RoutineChatModeSubject. A draft on a schedule is always the first.
	ChatMode      string
	Timezone      string
	Instruction   string
	Notes         string
	PersonalityID string
	Model         string
	Effort        string
	// Repos and DailyCapUSD: see Routine.
	Repos       []string
	DailyCapUSD *float64
	NextRunAt   time.Time
}

// RoutineRun is one firing of a routine.
type RoutineRun struct {
	ID           string
	RoutineID    string
	ScheduledFor time.Time
	Trigger      string
	Status       string
	Summary      string
	// Reported is what the run's agent said of itself: RoutineRunIdle, RoutineRunDone or "".
	Reported string
	// ChatID is the subject's chat the run happened in, "" for the routine's own chat.
	ChatID     string
	TurnID     string
	CostUSD    *float64
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// PrepareRoutine trims a draft and refuses what the store will not hold.
func PrepareRoutine(in RoutineDraft) (RoutineDraft, error) {
	out := in
	out.Name = strings.Join(strings.Fields(in.Name), " ")
	out.Instruction = strings.TrimSpace(in.Instruction)
	out.Notes = strings.TrimSpace(in.Notes)
	out.Schedule = strings.Join(strings.Fields(in.Schedule), " ")
	out.Timezone = strings.TrimSpace(in.Timezone)
	out.PersonalityID = strings.TrimSpace(in.PersonalityID)
	out.Model = strings.TrimSpace(in.Model)
	out.Effort = strings.TrimSpace(in.Effort)
	repos, err := cleanRepos(in.Repos)
	if err != nil {
		return RoutineDraft{}, err
	}
	out.Repos = repos
	if err := prepareTrigger(&out); err != nil {
		return RoutineDraft{}, err
	}
	if in.DailyCapUSD != nil {
		if *in.DailyCapUSD < 0 {
			return RoutineDraft{}, fmt.Errorf("%w: a daily spend cap cannot be negative", ErrInvalidRoutine)
		}
		if *in.DailyCapUSD == 0 {
			out.DailyCapUSD = nil
		}
	}
	switch {
	case out.Name == "":
		return RoutineDraft{}, fmt.Errorf("%w: a name is required", ErrInvalidRoutine)
	case utf8.RuneCountInString(out.Name) > MaxRoutineNameRunes:
		return RoutineDraft{}, fmt.Errorf("%w: the name is longer than %d characters", ErrInvalidRoutine, MaxRoutineNameRunes)
	case out.Instruction == "":
		return RoutineDraft{}, fmt.Errorf("%w: an instruction is required: it is what every run is asked to do", ErrInvalidRoutine)
	case utf8.RuneCountInString(out.Instruction) > MaxRoutineInstructionRunes:
		return RoutineDraft{}, fmt.Errorf("%w: the instruction is longer than %d characters", ErrInvalidRoutine, MaxRoutineInstructionRunes)
	case utf8.RuneCountInString(out.Notes) > MaxRoutineNotesRunes:
		return RoutineDraft{}, fmt.Errorf("%w: the notes are longer than %d characters", ErrInvalidRoutine, MaxRoutineNotesRunes)
	case out.Schedule == "" && out.Kind == RoutineKindSchedule:
		return RoutineDraft{}, fmt.Errorf("%w: a schedule is required", ErrInvalidRoutine)
	case out.Timezone == "":
		return RoutineDraft{}, fmt.Errorf("%w: a timezone is required", ErrInvalidRoutine)
	case out.NextRunAt.IsZero():
		return RoutineDraft{}, fmt.Errorf("%w: the schedule names no time to run", ErrInvalidRoutine)
	}
	return out, nil
}

// cleanRepos is a repository list as GitHub names them, owner/name, each once.
func cleanRepos(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		r = strings.Trim(strings.TrimSpace(r), "/")
		r = strings.TrimSuffix(strings.TrimPrefix(r, "https://github.com/"), ".git")
		if r == "" {
			continue
		}
		owner, name, ok := strings.Cut(r, "/")
		if !ok || owner == "" || name == "" || strings.ContainsAny(name, "/ ") || strings.Contains(owner, " ") {
			return nil, fmt.Errorf("%w: %q is not a repository; write it as owner/name", ErrInvalidRoutine, r)
		}
		key := strings.ToLower(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	if len(out) > MaxRoutineRepos {
		return nil, fmt.Errorf("%w: a routine reads at most %d repositories", ErrInvalidRoutine, MaxRoutineRepos)
	}
	return out, nil
}

// CleanRoutineState checks a state object a run handed back: a JSON object, within the cap.
func CleanRoutineState(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return json.RawMessage("{}"), nil
	}
	if len(trimmed) > MaxRoutineStateBytes {
		return nil, fmt.Errorf("%w: the state is %d bytes; keep it under %d — it is for exact facts, not a log",
			ErrInvalidRoutine, len(trimmed), MaxRoutineStateBytes)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: the state must be a JSON object", ErrInvalidRoutine)
	}
	compact, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: the state could not be encoded: %v", ErrInvalidRoutine, err)
	}
	return compact, nil
}

// CreateRoutine opens the routine's chat and the routine in it, for owner. personalityDisplay
// is the display name of draft.PersonalityID, snapshotted onto the chat as any personal chat
// does. A chat left behind by a failed insert is removed.
func (s *Store) CreateRoutine(ctx context.Context, owner string, draft RoutineDraft, personalityDisplay string) (Routine, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Routine{}, errors.New("create routine: an owner is required")
	}
	d, err := PrepareRoutine(draft)
	if err != nil {
		return Routine{}, err
	}
	chat, err := s.createChat(ctx, owner, d.Name, d.PersonalityID, personalityDisplay)
	if err != nil {
		return Routine{}, fmt.Errorf("create routine: open its chat: %w", err)
	}
	now := time.Now().UTC()
	row, err := s.q.InsertRoutine(ctx, db.InsertRoutineParams{
		ID:            ids.New("rtn"),
		Scope:         RoutineScopePersonal,
		OwnerLogin:    &owner,
		Name:          d.Name,
		Schedule:      d.Schedule,
		Timezone:      d.Timezone,
		Instruction:   d.Instruction,
		CreatedAt:     now,
		Notes:         d.Notes,
		PersonalityID: nullable(d.PersonalityID),
		Model:         d.Model,
		Effort:        d.Effort,
		ChatID:        chat.ID,
		NextRunAt:     d.NextRunAt.UTC(),
		Repos:         []string{},
		DailyCapUsd:   d.DailyCapUSD,
		CreatedBy:     owner,
		TriggerKind:   d.Kind,
		Events:        d.Events,
		Teams:         d.Teams,
		ChatMode:      d.ChatMode,
	})
	if err == nil {
		err = s.q.SetChatRoutine(ctx, db.SetChatRoutineParams{ID: chat.ID, RoutineID: &row.ID})
	}
	if err != nil {
		_, _ = s.q.DeleteChat(context.WithoutCancel(ctx), db.DeleteChatParams{ID: chat.ID, Login: &owner})
		return Routine{}, fmt.Errorf("create routine: %w", err)
	}
	r := routineFromRow(row)
	s.recordRevision(ctx, r, owner)
	return r, nil
}

// CreateSystemRoutine opens a system routine and its chat. The chat has no owner: admins read
// it through the API, and nobody writes in it. actor is the admin who set it up.
func (s *Store) CreateSystemRoutine(ctx context.Context, actor string, draft RoutineDraft) (Routine, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Routine{}, errors.New("create system routine: who is setting it up is required")
	}
	d, err := PrepareRoutine(draft)
	if err != nil {
		return Routine{}, err
	}
	chatID := ids.New("chat")
	key := ChatSourceKey(chatID)
	chatRow, err := s.q.CreateChat(ctx, db.CreateChatParams{
		ID: chatID, Title: d.Name, CreatedAt: time.Now().UTC(), SourceKey: &key, Origin: OriginWeb,
		StartedBy: actor,
	})
	if err != nil {
		return Routine{}, fmt.Errorf("create system routine: open its chat: %w", err)
	}
	now := time.Now().UTC()
	repos := d.Repos
	if repos == nil {
		repos = []string{}
	}
	row, err := s.q.InsertRoutine(ctx, db.InsertRoutineParams{
		ID:          ids.New("rtn"),
		Scope:       RoutineScopeSystem,
		Name:        d.Name,
		Schedule:    d.Schedule,
		Timezone:    d.Timezone,
		Instruction: d.Instruction,
		CreatedAt:   now,
		Notes:       d.Notes,
		Model:       d.Model,
		Effort:      d.Effort,
		ChatID:      chatRow.ID,
		NextRunAt:   d.NextRunAt.UTC(),
		Repos:       repos,
		DailyCapUsd: d.DailyCapUSD,
		CreatedBy:   actor,
		TriggerKind: d.Kind,
		Events:      d.Events,
		Teams:       d.Teams,
		ChatMode:    d.ChatMode,
	})
	if err == nil {
		err = s.q.SetChatRoutine(ctx, db.SetChatRoutineParams{ID: chatRow.ID, RoutineID: &row.ID})
	}
	if err != nil {
		_, _ = s.pool.Exec(context.WithoutCancel(ctx), "delete from chats where id = $1", chatRow.ID)
		return Routine{}, fmt.Errorf("create system routine: %w", err)
	}
	r := routineFromRow(row)
	s.recordRevision(ctx, r, actor)
	return r, nil
}

// recordRevision adds r's instruction as it now stands to its history. A history that missed
// one change is still worth more than a routine that failed to save, so the change stands
// whether or not this lands; it is written outside the caller's cancellation for that reason.
func (s *Store) recordRevision(ctx context.Context, r Routine, actor string) {
	_ = s.q.InsertRoutineRevision(context.WithoutCancel(ctx), db.InsertRoutineRevisionParams{
		RoutineID: r.ID, Instruction: r.Instruction, Notes: r.Notes, Schedule: r.When(),
		Timezone: r.Timezone, ChangedBy: actor, ChangedAt: time.Now().UTC(),
	})
}

// RoutineRevision is one entry in a routine's instruction history.
type RoutineRevision struct {
	ID          int64
	Instruction string
	Notes       string
	Schedule    string
	Timezone    string
	ChangedBy   string
	ChangedAt   time.Time
}

// ListRoutineRevisions is a routine's history, newest first.
func (s *Store) ListRoutineRevisions(ctx context.Context, routineID string, limit int) ([]RoutineRevision, error) {
	rows, err := s.q.ListRoutineRevisions(ctx, db.ListRoutineRevisionsParams{RoutineID: routineID, PageLimit: int32(clampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("list the revisions of routine %s: %w", routineID, err)
	}
	out := make([]RoutineRevision, 0, len(rows))
	for _, r := range rows {
		out = append(out, RoutineRevision{
			ID: r.ID, Instruction: r.Instruction, Notes: r.Notes, Schedule: r.Schedule, Timezone: r.Timezone,
			ChangedBy: r.ChangedBy, ChangedAt: r.ChangedAt.UTC(),
		})
	}
	return out, nil
}

// GetRoutine is one routine by id.
func (s *Store) GetRoutine(ctx context.Context, id string) (Routine, error) {
	row, err := s.q.GetRoutine(ctx, id)
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("get routine %s: %w", id, err)
	}
	return routineFromRow(row), nil
}

// RoutineByChat is the routine a chat belongs to.
func (s *Store) RoutineByChat(ctx context.Context, chatID string) (Routine, error) {
	row, err := s.q.GetRoutineByChat(ctx, chatID)
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine of chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("get the routine of chat %s: %w", chatID, err)
	}
	return routineFromRow(row), nil
}

// ListVisibleRoutines is owner's routines and, withSystem, every system routine: what a person
// sees on the Routines page, an admin's including the system ones.
func (s *Store) ListVisibleRoutines(ctx context.Context, owner string, withSystem bool) ([]Routine, error) {
	rows, err := s.q.ListVisibleRoutines(ctx, db.ListVisibleRoutinesParams{OwnerLogin: &owner, WithSystem: withSystem})
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// ListPersonalRoutines is every person's routines, for an admin's metadata view.
func (s *Store) ListPersonalRoutines(ctx context.Context) ([]Routine, error) {
	rows, err := s.q.ListPersonalRoutines(ctx)
	if err != nil {
		return nil, fmt.Errorf("list personal routines: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// CountRoutines is how many routines owner has.
func (s *Store) CountRoutines(ctx context.Context, owner string) (int, error) {
	n, err := s.q.CountRoutinesByOwner(ctx, &owner)
	if err != nil {
		return 0, fmt.Errorf("count the routines of %s: %w", owner, err)
	}
	return int(n), nil
}

// RoutineLastRun is the status and time of a routine's latest run that was not skipped.
type RoutineLastRun struct {
	Status string
	At     time.Time
}

// LastRoutineRuns is each named routine's latest run, by routine id. A routine that has not
// run is missing.
func (s *Store) LastRoutineRuns(ctx context.Context, routineIDs []string) (map[string]RoutineLastRun, error) {
	out := map[string]RoutineLastRun{}
	if len(routineIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.LastRoutineRuns(ctx, routineIDs)
	if err != nil {
		return nil, fmt.Errorf("read the last runs of %d routines: %w", len(routineIDs), err)
	}
	for _, r := range rows {
		at := r.CreatedAt
		if r.FinishedAt != nil {
			at = *r.FinishedAt
		}
		out[r.RoutineID] = RoutineLastRun{Status: r.Status, At: at.UTC()}
	}
	return out, nil
}

// ListRoutines is owner's routines by name. An empty owner is every routine, for the dev token.
func (s *Store) ListRoutines(ctx context.Context, owner string) ([]Routine, error) {
	var rows []db.Routine
	var err error
	if owner == "" {
		rows, err = s.q.ListAllRoutines(ctx)
	} else {
		rows, err = s.q.ListRoutinesByOwner(ctx, &owner)
	}
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// UpdateRoutine replaces what a person edits. Access is the caller's to check. actor is who
// made the change; a change to what the routine is told to do is added to its history.
func (s *Store) UpdateRoutine(ctx context.Context, id, actor string, draft RoutineDraft) (Routine, error) {
	d, err := PrepareRoutine(draft)
	if err != nil {
		return Routine{}, err
	}
	before, err := s.GetRoutine(ctx, id)
	if err != nil {
		return Routine{}, err
	}
	repos := d.Repos
	if repos == nil {
		repos = []string{}
	}
	row, err := s.q.UpdateRoutine(ctx, db.UpdateRoutineParams{
		ID:            id,
		Name:          d.Name,
		Schedule:      d.Schedule,
		Timezone:      d.Timezone,
		Instruction:   d.Instruction,
		Notes:         d.Notes,
		PersonalityID: nullable(d.PersonalityID),
		Model:         d.Model,
		Effort:        d.Effort,
		Repos:         repos,
		DailyCapUsd:   d.DailyCapUSD,
		TriggerKind:   d.Kind,
		Events:        d.Events,
		Teams:         d.Teams,
		ChatMode:      d.ChatMode,
		NextRunAt:     d.NextRunAt.UTC(),
		UpdatedAt:     time.Now().UTC(),
	})
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("update routine %s: %w", id, err)
	}
	r := routineFromRow(row)
	if revised(before, r) {
		s.recordRevision(ctx, r, actor)
	}
	return r, nil
}

// revised reports whether what a routine is told to do, or when, changed.
func revised(a, b Routine) bool {
	return a.Instruction != b.Instruction || a.Notes != b.Notes || a.When() != b.When() || a.Timezone != b.Timezone
}

// DeleteRoutine removes a routine and its runs. Its chat stays, as an ordinary chat.
func (s *Store) DeleteRoutine(ctx context.Context, id string) error {
	n, err := s.q.DeleteRoutine(ctx, id)
	if err != nil {
		return fmt.Errorf("delete routine %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	return nil
}

// SetRoutineEnabled pauses (with a reason) or resumes a routine. next is when it runs next.
func (s *Store) SetRoutineEnabled(ctx context.Context, id string, enabled bool, reason string, next time.Time) (Routine, error) {
	if enabled {
		reason = ""
	}
	return s.setRoutineEnabled(ctx, id, enabled, reason, nil, next)
}

// PauseRoutineUntil pauses a routine that resumes by itself at until: a spend cap's day.
func (s *Store) PauseRoutineUntil(ctx context.Context, id, reason string, until, next time.Time) (Routine, error) {
	u := until.UTC()
	return s.setRoutineEnabled(ctx, id, false, reason, &u, next)
}

func (s *Store) setRoutineEnabled(ctx context.Context, id string, enabled bool, reason string, until *time.Time, next time.Time) (Routine, error) {
	row, err := s.q.SetRoutineEnabled(ctx, db.SetRoutineEnabledParams{
		ID: id, Enabled: enabled, PausedReason: strings.TrimSpace(reason), PausedUntil: until,
		NextRunAt: next.UTC(), UpdatedAt: time.Now().UTC(),
	})
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("set routine %s enabled=%t: %w", id, enabled, err)
	}
	return routineFromRow(row), nil
}

// SetRoutineState stores the agent's state. It is checked by CleanRoutineState first.
func (s *Store) SetRoutineState(ctx context.Context, id string, state json.RawMessage) (Routine, error) {
	clean, err := CleanRoutineState(state)
	if err != nil {
		return Routine{}, err
	}
	now := time.Now().UTC()
	row, err := s.q.SetRoutineState(ctx, db.SetRoutineStateParams{ID: id, State: clean, UpdatedAt: &now})
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("set the state of routine %s: %w", id, err)
	}
	return routineFromRow(row), nil
}

// SetRoutineNotes stores the owner's notes and what is still proposed. A change to the notes
// is added to the routine's history as actor's.
func (s *Store) SetRoutineNotes(ctx context.Context, id, actor, notes, proposed string) (Routine, error) {
	notes = strings.TrimSpace(notes)
	if utf8.RuneCountInString(notes) > MaxRoutineNotesRunes {
		return Routine{}, fmt.Errorf("%w: the notes are longer than %d characters", ErrInvalidRoutine, MaxRoutineNotesRunes)
	}
	row, err := s.q.SetRoutineNotes(ctx, db.SetRoutineNotesParams{
		ID: id, Notes: notes, ProposedNote: strings.TrimSpace(proposed), UpdatedAt: time.Now().UTC(),
	})
	if noRows(err) {
		return Routine{}, fmt.Errorf("%w: routine %s", ErrNotFound, id)
	}
	if err != nil {
		return Routine{}, fmt.Errorf("set the notes of routine %s: %w", id, err)
	}
	r := routineFromRow(row)
	if before, ok := s.lastRevision(ctx, id); !ok || before.Notes != r.Notes {
		s.recordRevision(ctx, r, actor)
	}
	return r, nil
}

// ProposeRoutineNote records a note the agent suggested, replacing any still pending.
func (s *Store) ProposeRoutineNote(ctx context.Context, id, note string) error {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxRoutineNotesRunes/4 {
		return fmt.Errorf("%w: a proposed note is longer than %d characters; keep it to one standing instruction",
			ErrInvalidRoutine, MaxRoutineNotesRunes/4)
	}
	if err := s.q.SetRoutineProposedNote(ctx, db.SetRoutineProposedNoteParams{ID: id, ProposedNote: note}); err != nil {
		return fmt.Errorf("propose a note on routine %s: %w", id, err)
	}
	return nil
}

// DueRoutines is every enabled routine whose next run is at or before now.
func (s *Store) DueRoutines(ctx context.Context, now time.Time, limit int) ([]Routine, error) {
	rows, err := s.q.DueRoutines(ctx, db.DueRoutinesParams{Now: now.UTC(), PageLimit: int32(clampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("list due routines: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// AdvanceRoutine moves a routine to its next run. last is when it was last fired, nil to
// leave that as it was.
func (s *Store) AdvanceRoutine(ctx context.Context, id string, next time.Time, last *time.Time) error {
	if err := s.q.AdvanceRoutine(ctx, db.AdvanceRoutineParams{ID: id, NextRunAt: next.UTC(), LastRunAt: utcPtr(last)}); err != nil {
		return fmt.Errorf("advance routine %s: %w", id, err)
	}
	return nil
}

// ClaimRoutineRun records one firing. ok is false when another conductor already recorded the
// same one, and the caller then does nothing. A status other than running is final at once.
func (s *Store) ClaimRoutineRun(ctx context.Context, routineID string, scheduledFor time.Time, trigger, status string) (RoutineRun, bool, error) {
	now := time.Now().UTC()
	var finished *time.Time
	if status != RoutineRunRunning {
		finished = &now
	}
	row, err := s.q.InsertRoutineRun(ctx, db.InsertRoutineRunParams{
		ID: ids.New("rrun"), RoutineID: routineID, ScheduledFor: scheduledFor.UTC(),
		Trigger: trigger, Status: status, CreatedAt: now, FinishedAt: finished,
	})
	if noRows(err) {
		return RoutineRun{}, false, nil
	}
	if err != nil {
		return RoutineRun{}, false, fmt.Errorf("record a run of routine %s: %w", routineID, err)
	}
	return routineRunFromRow(row), true, nil
}

// GetRoutineRun is one run by id.
func (s *Store) GetRoutineRun(ctx context.Context, id string) (RoutineRun, error) {
	row, err := s.q.GetRoutineRun(ctx, id)
	if noRows(err) {
		return RoutineRun{}, fmt.Errorf("%w: routine run %s", ErrNotFound, id)
	}
	if err != nil {
		return RoutineRun{}, fmt.Errorf("get routine run %s: %w", id, err)
	}
	return routineRunFromRow(row), nil
}

// ListRoutineRuns pages a routine's runs, newest first. includeIdle adds the runs that did
// nothing: idle and skipped.
func (s *Store) ListRoutineRuns(ctx context.Context, routineID string, includeIdle bool, before string, limit int) ([]RoutineRun, string, error) {
	limit = clampLimit(limit)
	rows, err := s.q.ListRoutineRuns(ctx, db.ListRoutineRunsParams{
		RoutineID: routineID, IncludeIdle: includeIdle, BeforeID: before, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("list the runs of routine %s: %w", routineID, err)
	}
	out := make([]RoutineRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineRunFromRow(r))
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// RunningRoutineRuns is every run not yet settled, oldest first.
func (s *Store) RunningRoutineRuns(ctx context.Context) ([]RoutineRun, error) {
	rows, err := s.q.RunningRoutineRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list running routine runs: %w", err)
	}
	out := make([]RoutineRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineRunFromRow(r))
	}
	return out, nil
}

// SettleRoutineRun records how a run ended. ok is false when it had already been settled.
// An empty summary keeps whatever the run reported.
func (s *Store) SettleRoutineRun(ctx context.Context, id, status, summary string, cost *float64) (RoutineRun, bool, error) {
	now := time.Now().UTC()
	row, err := s.q.SettleRoutineRun(ctx, db.SettleRoutineRunParams{
		ID: id, Status: status, Summary: strings.TrimSpace(summary), CostUsd: cost, FinishedAt: &now,
	})
	if noRows(err) {
		return RoutineRun{}, false, nil
	}
	if err != nil {
		return RoutineRun{}, false, fmt.Errorf("settle routine run %s: %w", id, err)
	}
	return routineRunFromRow(row), true, nil
}

// LinkRoutineRunTurn records the turn a run started, on both rows.
func (s *Store) LinkRoutineRunTurn(ctx context.Context, runID, turnID string) error {
	if err := s.q.SetTurnRoutineRun(ctx, db.SetTurnRoutineRunParams{ID: turnID, RoutineRunID: &runID}); err != nil {
		return fmt.Errorf("link turn %s to routine run %s: %w", turnID, runID, err)
	}
	if err := s.q.SetRoutineRunTurn(ctx, db.SetRoutineRunTurnParams{ID: runID, TurnID: &turnID}); err != nil {
		return fmt.Errorf("link routine run %s to turn %s: %w", runID, turnID, err)
	}
	return nil
}

// ReportRoutineRun records what a run's agent said of itself: idle or done, and one line.
func (s *Store) ReportRoutineRun(ctx context.Context, runID, reported, summary string) error {
	summary = strings.TrimSpace(strings.SplitN(strings.TrimSpace(summary), "\n", 2)[0])
	if utf8.RuneCountInString(summary) > 200 {
		summary = string([]rune(summary)[:200])
	}
	if err := s.q.ReportRoutineRun(ctx, db.ReportRoutineRunParams{ID: runID, Reported: reported, Summary: summary}); err != nil {
		return fmt.Errorf("record what routine run %s reported: %w", runID, err)
	}
	return nil
}

// TurnForRoutineRun is the turn a run started.
func (s *Store) TurnForRoutineRun(ctx context.Context, runID string) (Turn, error) {
	row, err := s.q.TurnForRoutineRun(ctx, &runID)
	if noRows(err) {
		return Turn{}, fmt.Errorf("%w: turn of routine run %s", ErrNotFound, runID)
	}
	if err != nil {
		return Turn{}, fmt.Errorf("get the turn of routine run %s: %w", runID, err)
	}
	return turnFromRow(row), nil
}

// NoteRoutineIdle counts an idle run, or resets the count after a run that did something.
func (s *Store) NoteRoutineIdle(ctx context.Context, id string, idle bool) error {
	if err := s.q.SetRoutineIdleCount(ctx, db.SetRoutineIdleCountParams{ID: id, Idle: idle}); err != nil {
		return fmt.Errorf("count an idle run of routine %s: %w", id, err)
	}
	return nil
}

// RoutineFailuresSince is how many of a routine's runs failed since a moment.
func (s *Store) RoutineFailuresSince(ctx context.Context, routineID string, since time.Time) (int, error) {
	n, err := s.q.RoutineFailuresSince(ctx, db.RoutineFailuresSinceParams{RoutineID: routineID, Since: since.UTC()})
	if err != nil {
		return 0, fmt.Errorf("count the failed runs of routine %s: %w", routineID, err)
	}
	return int(n), nil
}

// SessionBusy is whether a session has a turn running or waiting.
func (s *Store) SessionBusy(ctx context.Context, sessionID string) (bool, error) {
	busy, err := s.q.SessionBusy(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("is session %s busy: %w", sessionID, err)
	}
	return busy, nil
}

// CountChatsStartedByRoutineSince is how many chats a routine's turns opened since a moment.
func (s *Store) CountChatsStartedByRoutineSince(ctx context.Context, routineID string, since time.Time) (int, error) {
	n, err := s.q.CountChatsStartedByRoutineSince(ctx, db.CountChatsStartedByRoutineSinceParams{
		RoutineID: routineID, Since: since.UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("count the chats routine %s opened: %w", routineID, err)
	}
	return int(n), nil
}

func (s *Store) lastRevision(ctx context.Context, id string) (RoutineRevision, bool) {
	revs, err := s.ListRoutineRevisions(ctx, id, 1)
	if err != nil || len(revs) == 0 {
		return RoutineRevision{}, false
	}
	return revs[0], true
}

// DueToResume is every routine paused until a moment that has passed.
func (s *Store) DueToResume(ctx context.Context, now time.Time, limit int) ([]Routine, error) {
	rows, err := s.q.DueToResume(ctx, db.DueToResumeParams{Now: utcPtr(&now), PageLimit: int32(clampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("list routines due to resume: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// PersonRoutineSpendSince is what owner's routines spent since a moment, in US dollars.
func (s *Store) PersonRoutineSpendSince(ctx context.Context, owner string, since time.Time) (float64, error) {
	v, err := s.q.PersonSpendSince(ctx, db.PersonSpendSinceParams{OwnerLogin: &owner, Since: since.UTC()})
	if err != nil {
		return 0, fmt.Errorf("sum the routine spend of %s: %w", owner, err)
	}
	return v, nil
}

// RunningRoutineRun is a routine's run still going, or ErrNotFound.
func (s *Store) RunningRoutineRun(ctx context.Context, routineID string) (RoutineRun, error) {
	row, err := s.q.RunningRoutineRunOf(ctx, routineID)
	if noRows(err) {
		return RoutineRun{}, fmt.Errorf("%w: a running run of routine %s", ErrNotFound, routineID)
	}
	if err != nil {
		return RoutineRun{}, fmt.Errorf("read the running run of routine %s: %w", routineID, err)
	}
	return routineRunFromRow(row), nil
}

// NotifyRoutine puts a routine notice in login's inbox: their routine was paused, and why.
func (s *Store) NotifyRoutine(ctx context.Context, login, chatID, actor, detail string) error {
	if err := s.q.NotifyRoutine(ctx, db.NotifyRoutineParams{Login: login, ChatID: chatID, Actor: actor, Detail: detail}); err != nil {
		return fmt.Errorf("notify %s about a routine: %w", login, err)
	}
	return nil
}

// RoutineSpendSince is what a routine's turns spent since a moment, in US dollars.
func (s *Store) RoutineSpendSince(ctx context.Context, routineID string, since time.Time) (float64, error) {
	v, err := s.q.RoutineSpendSince(ctx, db.RoutineSpendSinceParams{RoutineID: routineID, Since: since.UTC()})
	if err != nil {
		return 0, fmt.Errorf("sum the spend of routine %s: %w", routineID, err)
	}
	return v, nil
}

func routineFromRow(r db.Routine) Routine {
	state := json.RawMessage(r.State)
	if len(state) == 0 {
		state = json.RawMessage("{}")
	}
	return Routine{
		ID:                   r.ID,
		Scope:                r.Scope,
		OwnerLogin:           deref(r.OwnerLogin),
		Name:                 r.Name,
		Kind:                 r.TriggerKind,
		Schedule:             r.Schedule,
		Timezone:             r.Timezone,
		Events:               append([]string{}, r.Events...),
		Teams:                append([]string{}, r.Teams...),
		ChatMode:             r.ChatMode,
		Instruction:          r.Instruction,
		InstructionUpdatedAt: r.InstructionUpdatedAt.UTC(),
		Notes:                r.Notes,
		ProposedNote:         r.ProposedNote,
		PersonalityID:        deref(r.PersonalityID),
		Model:                r.Model,
		Effort:               r.Effort,
		Enabled:              r.Enabled,
		PausedReason:         r.PausedReason,
		State:                state,
		StateUpdatedAt:       utcPtr(r.StateUpdatedAt),
		ChatID:               r.ChatID,
		NextRunAt:            r.NextRunAt.UTC(),
		LastRunAt:            utcPtr(r.LastRunAt),
		IdleCount:            int(r.IdleCount),
		Repos:                append([]string{}, r.Repos...),
		DailyCapUSD:          r.DailyCapUsd,
		PausedUntil:          utcPtr(r.PausedUntil),
		CreatedBy:            r.CreatedBy,
		CreatedAt:            r.CreatedAt.UTC(),
		UpdatedAt:            r.UpdatedAt.UTC(),
	}
}

func routineRunFromRow(r db.RoutineRun) RoutineRun {
	return RoutineRun{
		ID:           r.ID,
		RoutineID:    r.RoutineID,
		ScheduledFor: r.ScheduledFor.UTC(),
		Trigger:      r.Trigger,
		Status:       r.Status,
		Summary:      r.Summary,
		Reported:     r.Reported,
		ChatID:       deref(r.ChatID),
		TurnID:       deref(r.TurnID),
		CostUSD:      r.CostUsd,
		CreatedAt:    r.CreatedAt.UTC(),
		FinishedAt:   utcPtr(r.FinishedAt),
	}
}

// RoutineLimits bound what one person's routines may do. Admins set them.
type RoutineLimits struct {
	// MaxPerPerson is how many routines one person may have.
	MaxPerPerson int `json:"max_per_person"`
	// PersonDailyCapUSD is what one person's routines may spend together in 24 hours; 0 is none.
	PersonDailyCapUSD float64 `json:"person_daily_cap_usd"`
}

// DefaultRoutineLimits are the limits until an admin sets them.
var DefaultRoutineLimits = RoutineLimits{MaxPerPerson: 10}

// MaxRoutinesPerPersonCeiling bounds what an admin may allow one person.
const MaxRoutinesPerPersonCeiling = 100

const routineLimitsKey = "routines.limits"

// GetRoutineLimits is the per-person limits, the defaults until an admin sets them.
func (s *Store) GetRoutineLimits(ctx context.Context) (RoutineLimits, error) {
	out := DefaultRoutineLimits
	err := s.GetSetting(ctx, routineLimitsKey, &out)
	if errors.Is(err, ErrNotFound) {
		return DefaultRoutineLimits, nil
	}
	if err != nil {
		return DefaultRoutineLimits, err
	}
	if out.MaxPerPerson <= 0 {
		out.MaxPerPerson = DefaultRoutineLimits.MaxPerPerson
	}
	return out, nil
}

// PutRoutineLimits stores the per-person limits.
func (s *Store) PutRoutineLimits(ctx context.Context, l RoutineLimits) (RoutineLimits, error) {
	switch {
	case l.MaxPerPerson < 1 || l.MaxPerPerson > MaxRoutinesPerPersonCeiling:
		return RoutineLimits{}, fmt.Errorf("%w: a person may have between 1 and %d routines", ErrInvalidRoutine, MaxRoutinesPerPersonCeiling)
	case l.PersonDailyCapUSD < 0:
		return RoutineLimits{}, fmt.Errorf("%w: a daily spend cap cannot be negative", ErrInvalidRoutine)
	}
	if err := s.PutSetting(ctx, routineLimitsKey, l); err != nil {
		return RoutineLimits{}, err
	}
	return l, nil
}
