package store

// Routines on Linear events (0046, docs/agent.md "On a Linear event"). A linear routine names
// the Linear events it runs on, and optionally the teams it is limited to. The Linear agent
// app's webhook hands each data change to StoreRoutineEvents, which keeps one waiting row per
// routine that asks for it; the leading conductor's routine tick gives a run the oldest rows
// waiting, and the run's brief reads them back by the run's id, whichever conductor runs it.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// RoutineLinearTypes are the Linear entities a routine may run on, in the order the UI lists
// them. Linear sends others (reactions, documents, …); nothing runs on those.
var RoutineLinearTypes = []string{"Issue", "Comment", "Project", "ProjectUpdate", "Cycle", "IssueLabel"}

// RoutineLinearActions are what can happen to one, as Linear names it in a data change.
var RoutineLinearActions = []string{"create", "update", "remove"}

const (
	// MaxRoutineTeams bounds the teams one linear routine is limited to.
	MaxRoutineTeams = 20
	// maxTeamKeyRunes bounds one team key. Linear's are a few letters; this only stops a
	// paragraph being saved as one.
	maxTeamKeyRunes = 16
	// MaxRoutineEventPayloadBytes is the largest delivery kept whole. Linear may send up to a
	// megabyte; a larger one is kept as a stub that says so, because the run would trim it
	// to far less anyway.
	MaxRoutineEventPayloadBytes = 256 * 1024
)

var (
	linearTypeWords = map[string]string{
		"Issue": "issue", "Comment": "comment", "Project": "project", "ProjectUpdate": "project update",
		"Cycle": "cycle", "IssueLabel": "issue label",
	}
	linearActionWords = map[string]string{"create": "created", "update": "updated", "remove": "removed"}
)

// CanonicalRoutineEvent is an event name as the store keeps it ("Issue.create"), read without
// regard to case. ok is false for anything that is not one of the types and actions above.
func CanonicalRoutineEvent(event string) (string, bool) {
	typ, action, found := strings.Cut(strings.TrimSpace(event), ".")
	if !found {
		return "", false
	}
	var t, a string
	for _, known := range RoutineLinearTypes {
		if strings.EqualFold(known, typ) {
			t = known
		}
	}
	for _, known := range RoutineLinearActions {
		if strings.EqualFold(known, action) {
			a = known
		}
	}
	if t == "" || a == "" {
		return "", false
	}
	return t + "." + a, true
}

// RoutineEventLabel is an event in words: "issue created".
func RoutineEventLabel(event string) string {
	typ, action, _ := strings.Cut(event, ".")
	t, ok := linearTypeWords[typ]
	if !ok {
		t = strings.ToLower(typ)
	}
	a, ok := linearActionWords[action]
	if !ok {
		a = action
	}
	return strings.TrimSpace(t + " " + a)
}

// DescribeLinearTrigger is a linear routine's trigger in words, as schedule_text says a
// schedule: "On Linear: issue created, comment created (ENG)".
func DescribeLinearTrigger(events, teams []string) string {
	if len(events) == 0 {
		return "On Linear"
	}
	words := make([]string, 0, len(events))
	for _, e := range events {
		words = append(words, RoutineEventLabel(e))
	}
	out := "On Linear: " + strings.Join(words, ", ")
	if len(teams) > 0 {
		out += " (" + strings.Join(teams, ", ") + ")"
	}
	return out
}

// prepareTrigger checks what starts a draft's runs. A routine runs on a schedule or on Linear
// events, never both: a linear draft names events and no schedule, and a scheduled one names
// no events or teams. A linear draft's next run is never read; it is set to now only because
// the column is not null.
//
// A chat per ticket is a linear routine's alone: a scheduled run is about no ticket.
func prepareTrigger(d *RoutineDraft) error {
	d.Kind = strings.TrimSpace(d.Kind)
	d.ChatMode = strings.ToLower(strings.TrimSpace(d.ChatMode))
	switch d.ChatMode {
	case "":
		d.ChatMode = RoutineChatModeRoutine
	case RoutineChatModeRoutine, RoutineChatModeSubject:
	default:
		return fmt.Errorf("%w: a routine's runs happen in its own chat (%q) or in a chat per ticket (%q), not %q",
			ErrInvalidRoutine, RoutineChatModeRoutine, RoutineChatModeSubject, d.ChatMode)
	}
	switch d.Kind {
	case "", RoutineKindSchedule:
		d.Kind = RoutineKindSchedule
		if len(cleanList(d.Events)) > 0 || len(cleanList(d.Teams)) > 0 {
			return fmt.Errorf("%w: a routine on a schedule names no Linear events or teams", ErrInvalidRoutine)
		}
		if d.ChatMode != RoutineChatModeRoutine {
			return fmt.Errorf("%w: a routine on a schedule runs in its own chat; a chat per ticket is for a "+
				"routine on Linear events", ErrInvalidRoutine)
		}
		d.Events, d.Teams = []string{}, []string{}
		return nil
	case RoutineKindLinear:
	default:
		return fmt.Errorf("%w: a routine runs on a schedule or on Linear events, not %q", ErrInvalidRoutine, d.Kind)
	}
	if d.Schedule != "" {
		return fmt.Errorf("%w: a routine runs on a schedule or on Linear events, not both", ErrInvalidRoutine)
	}
	events, err := cleanEvents(d.Events)
	if err != nil {
		return err
	}
	teams, err := cleanTeams(d.Teams)
	if err != nil {
		return err
	}
	d.Events, d.Teams = events, teams
	if d.NextRunAt.IsZero() {
		d.NextRunAt = time.Now().UTC()
	}
	return nil
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cleanEvents is a linear routine's events, each once, in the order the UI lists them.
func cleanEvents(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, e := range cleanList(in) {
		canon, ok := CanonicalRoutineEvent(e)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a Linear event a routine can run on; name one of %s, each "+
				"created, updated or removed, as Issue.create", ErrInvalidRoutine, e, strings.Join(RoutineLinearTypes, ", "))
		}
		seen[canon] = true
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%w: a routine on Linear events names at least one event", ErrInvalidRoutine)
	}
	out := make([]string, 0, len(seen))
	for _, t := range RoutineLinearTypes {
		for _, a := range RoutineLinearActions {
			if seen[t+"."+a] {
				out = append(out, t+"."+a)
			}
		}
	}
	return out, nil
}

// cleanTeams is a linear routine's team keys, upper case, each once.
func cleanTeams(in []string) ([]string, error) {
	out := []string{}
	for _, t := range cleanList(in) {
		t = strings.ToUpper(t)
		if len([]rune(t)) > maxTeamKeyRunes || strings.ContainsFunc(t, func(r rune) bool {
			return (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_'
		}) {
			return nil, fmt.Errorf("%w: %q is not a Linear team key; write the short key, as ENG", ErrInvalidRoutine, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxRoutineTeams {
		return nil, fmt.Errorf("%w: a routine is limited to at most %d teams", ErrInvalidRoutine, MaxRoutineTeams)
	}
	return out, nil
}

// LinearDelivery is one data change from Linear, as the webhook hands it over.
type LinearDelivery struct {
	// DeliveryID is Linear's id for the delivery. A retry carries the same one.
	DeliveryID string
	// Event is "<Type>.<action>", as a routine names it.
	Event string
	// TeamKey is the key of the team the entity is in, "" when it names none.
	TeamKey string
	// Payload is the delivery without anything secret. One over MaxRoutineEventPayloadBytes is
	// kept as a stub.
	Payload    json.RawMessage
	ReceivedAt time.Time
}

// RoutineEvent is one Linear event waiting for, or given to, a routine's run.
type RoutineEvent struct {
	ID         int64
	RoutineID  string
	DeliveryID string
	Event      string
	Payload    json.RawMessage
	ReceivedAt time.Time
	// RunID is the run it was given to, "" while it waits.
	RunID string
}

// StoreRoutineEvents keeps d for every enabled linear routine that asks for its event, in its
// team when the routine names teams, and reports how many it was kept for. A delivery kept
// before is not kept again. It is one statement.
func (s *Store) StoreRoutineEvents(ctx context.Context, d LinearDelivery) (int, error) {
	event, ok := CanonicalRoutineEvent(d.Event)
	if !ok || strings.TrimSpace(d.DeliveryID) == "" {
		return 0, nil
	}
	at := d.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	// The subject is read from the delivery as Linear sent it, before a large one is cut to a
	// stub, and is kept whatever the routine's chat mode: a routine switched to a chat per
	// ticket then runs the events already waiting in theirs.
	params := db.InsertRoutineEventsParams{
		DeliveryID: d.DeliveryID, Event: event, Payload: BoundRoutineEventPayload(d.Payload),
		ReceivedAt: at.UTC(), TeamKey: strings.ToUpper(strings.TrimSpace(d.TeamKey)),
	}
	if key, title := RoutineEventSubject(event, d.Payload); key != "" {
		params.SubjectKey, params.SubjectTitle = &key, &title
	}
	n, err := s.q.InsertRoutineEvents(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("store Linear delivery %s for routines: %w", d.DeliveryID, err)
	}
	return int(n), nil
}

// BoundRoutineEventPayload is a payload the store keeps: the payload itself when it is JSON
// within MaxRoutineEventPayloadBytes, and otherwise a stub that keeps its type, action and URL
// and says the rest was too large.
func BoundRoutineEventPayload(raw json.RawMessage) json.RawMessage {
	if len(raw) <= MaxRoutineEventPayloadBytes && json.Valid(raw) {
		return raw
	}
	var head struct {
		Type   string `json:"type"`
		Action string `json:"action"`
		URL    string `json:"url"`
	}
	_ = json.Unmarshal(raw, &head)
	stub, _ := json.Marshal(map[string]any{
		"type": head.Type, "action": head.Action, "url": head.URL, "tooLarge": true,
		"note": fmt.Sprintf("Linear's payload was over %d KB, so only its type, action and url were kept; "+
			"read the rest from Linear.", MaxRoutineEventPayloadBytes/1024),
	})
	return stub
}

// RoutinesWithPendingEvents is every enabled linear routine with an event waiting.
func (s *Store) RoutinesWithPendingEvents(ctx context.Context, limit int) ([]Routine, error) {
	rows, err := s.q.RoutinesWithPendingEvents(ctx, int32(clampLimit(limit)))
	if err != nil {
		return nil, fmt.Errorf("list routines with Linear events waiting: %w", err)
	}
	out := make([]Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, routineFromRow(r))
	}
	return out, nil
}

// CapPendingRoutineEvents drops a routine's waiting events beyond the newest keep, and reports
// how many it dropped.
func (s *Store) CapPendingRoutineEvents(ctx context.Context, routineID string, keep int) (int, error) {
	n, err := s.q.CapPendingRoutineEvents(ctx, db.CapPendingRoutineEventsParams{RoutineID: routineID, Keep: int32(keep)})
	if err != nil {
		return 0, fmt.Errorf("cap the Linear events waiting for routine %s: %w", routineID, err)
	}
	return int(n), nil
}

// AttachRoutineEvents gives run the oldest of its routine's waiting events, at most max, and
// reports how many. An event is given to one run only.
func (s *Store) AttachRoutineEvents(ctx context.Context, runID, routineID string, limit int) (int, error) {
	n, err := s.q.AttachRoutineEvents(ctx, db.AttachRoutineEventsParams{RunID: runID, RoutineID: routineID, Max: int32(limit)})
	if err != nil {
		return 0, fmt.Errorf("give routine run %s its Linear events: %w", runID, err)
	}
	return int(n), nil
}

// ReleaseRoutineEvents puts a run's events back to waiting, for a run that never started.
func (s *Store) ReleaseRoutineEvents(ctx context.Context, runID string) (int, error) {
	n, err := s.q.ReleaseRoutineEvents(ctx, &runID)
	if err != nil {
		return 0, fmt.Errorf("put routine run %s's Linear events back: %w", runID, err)
	}
	return int(n), nil
}

// RoutineEventsForRun is the events a run was given, oldest first.
func (s *Store) RoutineEventsForRun(ctx context.Context, runID string) ([]RoutineEvent, error) {
	rows, err := s.q.RoutineEventsForRun(ctx, &runID)
	if err != nil {
		return nil, fmt.Errorf("read routine run %s's Linear events: %w", runID, err)
	}
	out := make([]RoutineEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, RoutineEvent{
			ID: r.ID, RoutineID: r.RoutineID, DeliveryID: r.DeliveryID, Event: r.Event,
			Payload: json.RawMessage(r.Payload), ReceivedAt: r.ReceivedAt.UTC(), RunID: deref(r.RunID),
		})
	}
	return out, nil
}

// PruneRoutineEvents deletes the events nobody will run: waiting since before pendingBefore,
// and given to a run since before keptBefore. It reports how many it deleted.
func (s *Store) PruneRoutineEvents(ctx context.Context, pendingBefore, keptBefore time.Time) (int, error) {
	n, err := s.q.PruneRoutineEvents(ctx, db.PruneRoutineEventsParams{
		PendingBefore: pendingBefore.UTC(), KeptBefore: keptBefore.UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("prune routines' Linear events: %w", err)
	}
	return int(n), nil
}

// PendingRoutineEvents is how many events wait for each named routine. A routine with none is
// missing.
func (s *Store) PendingRoutineEvents(ctx context.Context, routineIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(routineIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.PendingRoutineEventCounts(ctx, routineIDs)
	if err != nil {
		return nil, fmt.Errorf("count the Linear events waiting for %d routines: %w", len(routineIDs), err)
	}
	for _, r := range rows {
		out[r.RoutineID] = int(r.Pending)
	}
	return out, nil
}

// CountRoutineRunsSince is how many of a routine's runs of one trigger started since a moment.
func (s *Store) CountRoutineRunsSince(ctx context.Context, routineID, trigger string, since time.Time) (int, error) {
	n, err := s.q.CountRoutineRunsSince(ctx, db.CountRoutineRunsSinceParams{
		RoutineID: routineID, Trigger: trigger, Since: since.UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("count the %s runs of routine %s: %w", trigger, routineID, err)
	}
	return int(n), nil
}

// DeleteUnstartedRoutineRun removes a run that was claimed and then found nothing to give it.
func (s *Store) DeleteUnstartedRoutineRun(ctx context.Context, runID string) error {
	if err := s.q.DeleteUnstartedRoutineRun(ctx, runID); err != nil {
		return fmt.Errorf("delete unstarted routine run %s: %w", runID, err)
	}
	return nil
}

// RoutineEventSubject is what a Linear event is about, for a routine with a chat per ticket
// (0047): key tells subjects apart ("Issue:<id>"), and title names one in words ("ENG-12 Fix
// login"). An issue and the comments on it are one subject, as are a project and its updates.
//
// Linear documents no exact payload for each type, so the payload is read leniently: a field
// that is missing or of another shape means the event has no subject, never an error. An event
// with no subject has an empty key, and runs in the routine's own chat.
func RoutineEventSubject(event string, payload json.RawMessage) (key, title string) {
	var p struct {
		Data struct {
			ID         any    `json:"id"`
			Identifier any    `json:"identifier"`
			Title      any    `json:"title"`
			Name       any    `json:"name"`
			Number     any    `json:"number"`
			IssueID    any    `json:"issueId"`
			ProjectID  any    `json:"projectId"`
			Issue      *named `json:"issue"`
			Project    *named `json:"project"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return "", ""
	}
	d := p.Data
	typ, _, _ := strings.Cut(event, ".")
	switch typ {
	case "Issue":
		return subjectOf("Issue", text(d.ID), joinWords(text(d.Identifier), text(d.Title)))
	case "Comment":
		id, words := text(d.IssueID), ""
		if d.Issue != nil {
			id = firstOf(id, text(d.Issue.ID))
			words = joinWords(text(d.Issue.Identifier), text(d.Issue.Title))
		}
		return subjectOf("Issue", id, words)
	case "Project":
		return subjectOf("Project", text(d.ID), text(d.Name))
	case "ProjectUpdate":
		id, words := text(d.ProjectID), ""
		if d.Project != nil {
			id = firstOf(id, text(d.Project.ID))
			words = text(d.Project.Name)
		}
		return subjectOf("Project", id, words)
	case "Cycle":
		words := text(d.Name)
		if words == "" && text(d.Number) != "" {
			words = "Cycle " + text(d.Number)
		}
		return subjectOf("Cycle", text(d.ID), words)
	case "IssueLabel":
		return subjectOf("IssueLabel", text(d.ID), text(d.Name))
	}
	return "", ""
}

// named is an entity a payload nests: a comment's issue, a project update's project.
type named struct {
	ID         any `json:"id"`
	Identifier any `json:"identifier"`
	Title      any `json:"title"`
	Name       any `json:"name"`
}

// maxSubjectTitleRunes bounds a subject's title, which names a chat.
const maxSubjectTitleRunes = 120

func subjectOf(kind, id, title string) (string, string) {
	if id == "" {
		return "", ""
	}
	title = strings.Join(strings.Fields(title), " ")
	if r := []rune(title); len(r) > maxSubjectTitleRunes {
		title = strings.TrimSpace(string(r[:maxSubjectTitleRunes])) + "…"
	}
	return kind + ":" + id, title
}

// text is a payload value as text: a string as it is, a number in decimal, anything else "".
func text(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinWords(a, b string) string { return strings.TrimSpace(a + " " + b) }

// RoutineSubject is one subject a routine's waiting events are about.
type RoutineSubject struct {
	// Key is "" for the events about nothing in particular, which run in the routine's chat.
	Key   string
	Title string
	// Oldest is when its longest-waiting event arrived.
	Oldest  time.Time
	Pending int
	// ChatID is the chat it already has, "" when none has been opened for it yet.
	ChatID string
}

// MaxRoutineSubjectsListed bounds the subjects one tick weighs for a routine.
const MaxRoutineSubjectsListed = 50

// PendingRoutineSubjects is what a routine's waiting events are about, the subject waiting
// longest first.
func (s *Store) PendingRoutineSubjects(ctx context.Context, routineID string) ([]RoutineSubject, error) {
	rows, err := s.q.PendingRoutineSubjects(ctx, db.PendingRoutineSubjectsParams{
		RoutineID: routineID, PageLimit: MaxRoutineSubjectsListed,
	})
	if err != nil {
		return nil, fmt.Errorf("list what routine %s's waiting Linear events are about: %w", routineID, err)
	}
	out := make([]RoutineSubject, 0, len(rows))
	for _, r := range rows {
		out = append(out, RoutineSubject{
			Key: r.SubjectKey, Title: r.SubjectTitle, Oldest: r.Oldest.UTC(), Pending: int(r.Pending), ChatID: r.ChatID,
		})
	}
	return out, nil
}

// AttachRoutineSubjectEvents is AttachRoutineEvents for the waiting events about one subject;
// "" is the events about nothing in particular.
func (s *Store) AttachRoutineSubjectEvents(ctx context.Context, runID, routineID, subjectKey string, limit int) (int, error) {
	n, err := s.q.AttachRoutineSubjectEvents(ctx, db.AttachRoutineSubjectEventsParams{
		RunID: runID, RoutineID: routineID, SubjectKey: subjectKey, Max: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("give routine run %s the Linear events about %s: %w", runID, subjectKey, err)
	}
	return int(n), nil
}

// RoutineSubjectChat is one subject's chat in a routine with a chat per ticket.
type RoutineSubjectChat struct {
	RoutineID    string
	SubjectKey   string
	SubjectTitle string
	ChatID       string
	CreatedAt    time.Time
}

func subjectChatFromRow(r db.RoutineSubjectChat) RoutineSubjectChat {
	return RoutineSubjectChat{
		RoutineID: r.RoutineID, SubjectKey: r.SubjectKey, SubjectTitle: r.SubjectTitle,
		ChatID: r.ChatID, CreatedAt: r.CreatedAt.UTC(),
	}
}

// RoutineSubjectOfChat is the subject a chat is for, or ErrNotFound for any chat that is not
// one subject's: the routine's own chat included.
func (s *Store) RoutineSubjectOfChat(ctx context.Context, chatID string) (RoutineSubjectChat, error) {
	row, err := s.q.GetRoutineSubjectChatByChat(ctx, chatID)
	if noRows(err) {
		return RoutineSubjectChat{}, fmt.Errorf("%w: the subject of chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return RoutineSubjectChat{}, fmt.Errorf("read the subject of chat %s: %w", chatID, err)
	}
	return subjectChatFromRow(row), nil
}

// CountRoutineSubjectChatsSince is how many chats per ticket a routine opened since a moment.
func (s *Store) CountRoutineSubjectChatsSince(ctx context.Context, routineID string, since time.Time) (int, error) {
	n, err := s.q.CountRoutineSubjectChatsSince(ctx, db.CountRoutineSubjectChatsSinceParams{
		RoutineID: routineID, Since: since.UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("count the chats per ticket routine %s opened: %w", routineID, err)
	}
	return int(n), nil
}

// RoutineSubjectChatTitle is what a subject's chat is called: the routine's name and the
// subject in words, or its key when Linear sent no words for it.
func RoutineSubjectChatTitle(routineName, subjectKey, subjectTitle string) string {
	what := firstOf(strings.TrimSpace(subjectTitle), subjectKey)
	title := strings.TrimSpace(routineName) + " · " + what
	if r := []rune(title); len(r) > MaxChatTitleRunes {
		title = strings.TrimSpace(string(r[:MaxChatTitleRunes-1])) + "…"
	}
	return title
}

// EnsureRoutineSubjectChat is the chat a subject of r has, opened now if it has none. It is
// opened as r's own chat was — the same owner (none for a system routine), the same assistant
// — and belongs to r, so it is never shared, gets r's brief and card, and stays an ordinary chat
// if r is deleted. created reports whether it was opened by this call.
func (s *Store) EnsureRoutineSubjectChat(ctx context.Context, r Routine, subjectKey, subjectTitle string) (chatID string, created bool, err error) {
	if subjectKey == "" {
		return r.ChatID, false, nil
	}
	if row, err := s.q.GetRoutineSubjectChat(ctx, db.GetRoutineSubjectChatParams{RoutineID: r.ID, SubjectKey: subjectKey}); err == nil {
		return row.ChatID, false, nil
	} else if !noRows(err) {
		return "", false, fmt.Errorf("read the chat of %s for routine %s: %w", subjectKey, r.ID, err)
	}
	own, err := s.GetChat(ctx, r.ChatID)
	if err != nil {
		return "", false, fmt.Errorf("open a chat for %s: read routine %s's own chat: %w", subjectKey, r.ID, err)
	}
	title := RoutineSubjectChatTitle(r.Name, subjectKey, subjectTitle)
	var chatRowID string
	if r.System() {
		id := ids.New("chat")
		key := ChatSourceKey(id)
		row, err := s.q.CreateChat(ctx, db.CreateChatParams{
			ID: id, Title: title, CreatedAt: time.Now().UTC(), SourceKey: &key, Origin: OriginWeb,
			StartedBy: r.CreatedBy,
		})
		if err != nil {
			return "", false, fmt.Errorf("open a chat for %s of system routine %s: %w", subjectKey, r.ID, err)
		}
		chatRowID = row.ID
	} else {
		c, err := s.createChat(ctx, r.OwnerLogin, title, own.PersonalityID, own.PersonalityDisplay)
		if err != nil {
			return "", false, fmt.Errorf("open a chat for %s of routine %s: %w", subjectKey, r.ID, err)
		}
		chatRowID = c.ID
	}
	// The chat is removed whatever goes wrong from here: a chat that belongs to no routine
	// would be an ordinary empty chat in its owner's list.
	discard := func() {
		_, _ = s.pool.Exec(context.WithoutCancel(ctx), "delete from chats where id = $1", chatRowID)
	}
	routineID := r.ID
	if err := s.q.SetChatRoutine(ctx, db.SetChatRoutineParams{ID: chatRowID, RoutineID: &routineID}); err != nil {
		discard()
		return "", false, fmt.Errorf("give routine %s the chat for %s: %w", r.ID, subjectKey, err)
	}
	row, err := s.q.InsertRoutineSubjectChat(ctx, db.InsertRoutineSubjectChatParams{
		RoutineID: r.ID, SubjectKey: subjectKey, SubjectTitle: strings.TrimSpace(subjectTitle),
		ChatID: chatRowID, CreatedAt: time.Now().UTC(),
	})
	if noRows(err) {
		// Another tick gave the subject its chat first; that one is the subject's.
		discard()
		existing, gerr := s.q.GetRoutineSubjectChat(ctx, db.GetRoutineSubjectChatParams{RoutineID: r.ID, SubjectKey: subjectKey})
		if gerr != nil {
			return "", false, fmt.Errorf("read the chat of %s for routine %s: %w", subjectKey, r.ID, gerr)
		}
		return existing.ChatID, false, nil
	}
	if err != nil {
		discard()
		return "", false, fmt.Errorf("record the chat for %s of routine %s: %w", subjectKey, r.ID, err)
	}
	return row.ChatID, true, nil
}

// SetRoutineRunChat records that a run happened in one subject's chat.
func (s *Store) SetRoutineRunChat(ctx context.Context, runID, chatID string) error {
	if err := s.q.SetRoutineRunChat(ctx, db.SetRoutineRunChatParams{ID: runID, ChatID: &chatID}); err != nil {
		return fmt.Errorf("record the chat of routine run %s: %w", runID, err)
	}
	return nil
}
