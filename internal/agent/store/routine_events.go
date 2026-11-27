package store

// Routines on events (0046, 0057, docs/agent.md "On a Linear event", "On a GitHub event"). A
// routine on Linear events names the Linear events it runs on, and optionally the teams it is
// limited to; one on GitHub events names GitHub events, and optionally the repositories. The
// Linear agent app's webhook hands each data change to StoreRoutineEvents, and the GitHub App's
// each delivery to StoreGitHubRoutineEvents, which keep one waiting row per routine that asks
// for it; the leading conductor's routine tick gives a run the oldest rows waiting, and the
// run's brief reads them back by the run's id, whichever conductor runs it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
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

// RoutineGitHubEvents are the GitHub events a routine may run on, in the order the UI lists
// them, "<event>.<action>" as GitHub names them in a webhook delivery — with some of
// Podium's own: "failed" is a workflow run or check suite that completed with a failing
// conclusion (GitHubFailedConclusions), and "merged" a pull request closed by merging it. A
// delivery answers to its own name and to Podium's, so a routine on
// "workflow_run.completed" sees a failed run as well. GitHub sends many others; nothing runs
// on those.
//
// "checks" is no delivery of GitHub's: it is a commit whose every check has finished, made
// once per commit when its last workflow run, check suite or status finishes
// (ghreview/commit_checks.go). "checks.failed" is one where at least one of them failed.
var RoutineGitHubEvents = []string{
	"checks.completed", "checks.failed",
	"workflow_run.failed", "workflow_run.completed",
	"check_suite.failed", "check_suite.completed",
	"pull_request.opened", "pull_request.ready_for_review", "pull_request.synchronize",
	"pull_request.merged", "pull_request.closed",
	"pull_request_review.submitted",
	"issues.opened",
}

// GitHubFailedConclusions are the conclusions a "failed" workflow run or check suite has
// completed with. A cancelled, skipped or neutral one has not failed.
var GitHubFailedConclusions = []string{"failure", "timed_out", "startup_failure"}

var githubEventWords = map[string]string{
	"checks.completed": "all checks finished", "checks.failed": "all checks finished, at least one failed",
	"workflow_run.failed": "workflow run failed", "workflow_run.completed": "workflow run completed",
	"check_suite.failed": "check suite failed", "check_suite.completed": "check suite completed",
	"pull_request.opened": "pull request opened", "pull_request.ready_for_review": "pull request ready for review",
	"pull_request.synchronize": "pull request got new commits", "pull_request.merged": "pull request merged",
	"pull_request.closed":           "pull request closed",
	"pull_request_review.submitted": "review submitted",
	"issues.opened":                 "issue opened",
}

// CanonicalGitHubEvent is a GitHub event name as the store keeps it ("workflow_run.failed"),
// read without regard to case. ok is false for anything not in RoutineGitHubEvents.
func CanonicalGitHubEvent(event string) (string, bool) {
	event = strings.ToLower(strings.TrimSpace(event))
	if slices.Contains(RoutineGitHubEvents, event) {
		return event, true
	}
	return "", false
}

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

// RoutineEventLabel is an event in words: "issue created", "workflow run failed".
func RoutineEventLabel(event string) string {
	if words, ok := githubEventWords[event]; ok {
		return words
	}
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

// DescribeGitHubTrigger is a github routine's trigger in words: "On GitHub: workflow run
// failed (acme/api)".
func DescribeGitHubTrigger(events, repos []string) string {
	if len(events) == 0 {
		return "On GitHub"
	}
	words := make([]string, 0, len(events))
	for _, e := range events {
		words = append(words, RoutineEventLabel(e))
	}
	out := "On GitHub: " + strings.Join(words, ", ")
	if len(repos) > 0 {
		out += " (" + strings.Join(repos, ", ") + ")"
	}
	return out
}

// DescribeEventTrigger is a routine on events' trigger in words, Linear's or GitHub's by kind.
func DescribeEventTrigger(kind string, events, teams []string) string {
	if kind == RoutineKindGitHub {
		return DescribeGitHubTrigger(events, teams)
	}
	return DescribeLinearTrigger(events, teams)
}

// prepareTrigger checks what starts a draft's runs. A routine runs on a schedule or on events
// — Linear's or GitHub's — never both: a draft on events names events and no schedule, and a
// scheduled one names no events or teams. A draft on events' next run is never read; it is set
// to now only because the column is not null.
//
// Running in each ticket's conversation is a linear routine's alone: a scheduled run is about
// no ticket, and a GitHub event's runs are in the routine's own chat.
func prepareTrigger(d *RoutineDraft) error {
	d.Kind = strings.TrimSpace(d.Kind)
	d.ChatMode = strings.ToLower(strings.TrimSpace(d.ChatMode))
	switch d.ChatMode {
	case "":
		d.ChatMode = RoutineChatModeRoutine
	case RoutineChatModeRoutine, RoutineChatModeSubject:
	default:
		return fmt.Errorf("%w: a routine's runs happen in its own chat (%q) or in each ticket's conversation (%q), not %q",
			ErrInvalidRoutine, RoutineChatModeRoutine, RoutineChatModeSubject, d.ChatMode)
	}
	switch d.Kind {
	case "", RoutineKindSchedule:
		d.Kind = RoutineKindSchedule
		if len(cleanList(d.Events)) > 0 || len(cleanList(d.Teams)) > 0 {
			return fmt.Errorf("%w: a routine on a schedule names no Linear events or teams", ErrInvalidRoutine)
		}
		if d.ChatMode != RoutineChatModeRoutine {
			return fmt.Errorf("%w: a routine on a schedule runs in its own chat; running in each ticket's conversation "+
				"is for a routine on Linear events", ErrInvalidRoutine)
		}
		d.Events, d.Teams = []string{}, []string{}
		return nil
	case RoutineKindGitHub:
		return prepareGitHubTrigger(d)
	case RoutineKindLinear:
	default:
		return fmt.Errorf("%w: a routine runs on a schedule, on Linear events or on GitHub events, not %q",
			ErrInvalidRoutine, d.Kind)
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

// prepareGitHubTrigger is prepareTrigger for a draft on GitHub events: events and, optionally,
// repositories, and no schedule. Its runs are in each pull request's conversation or all in its
// own chat, as its chat mode says.
func prepareGitHubTrigger(d *RoutineDraft) error {
	if d.Schedule != "" {
		return fmt.Errorf("%w: a routine runs on a schedule or on GitHub events, not both", ErrInvalidRoutine)
	}
	events, err := cleanGitHubEvents(d.Events)
	if err != nil {
		return err
	}
	repos, err := cleanEventRepos(d.Teams)
	if err != nil {
		return err
	}
	d.Events, d.Teams = events, repos
	if d.NextRunAt.IsZero() {
		d.NextRunAt = time.Now().UTC()
	}
	return nil
}

// cleanGitHubEvents is a github routine's events, each once, in the order the UI lists them.
func cleanGitHubEvents(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, e := range cleanList(in) {
		canon, ok := CanonicalGitHubEvent(e)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a GitHub event a routine can run on; name one of %s",
				ErrInvalidRoutine, e, strings.Join(RoutineGitHubEvents, ", "))
		}
		seen[canon] = true
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%w: a routine on GitHub events names at least one event", ErrInvalidRoutine)
	}
	out := make([]string, 0, len(seen))
	for _, e := range RoutineGitHubEvents {
		if seen[e] {
			out = append(out, e)
		}
	}
	return out, nil
}

// githubRepoRE is a repository a github routine is limited to: an owner, or owner/name, in
// GitHub's alphabet, lower case.
var githubRepoRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38})(?:/[a-z0-9._-]{1,100})?$`)

// cleanEventRepos is a github routine's repositories, owner/name or an owner each, lower case, each
// once. A full GitHub URL is read as the repository it names.
func cleanEventRepos(in []string) ([]string, error) {
	out := []string{}
	for _, r := range cleanList(in) {
		r = strings.ToLower(r)
		r = strings.TrimPrefix(strings.TrimPrefix(r, "https://"), "http://")
		r = strings.TrimSuffix(strings.TrimPrefix(r, "github.com/"), "/")
		r = strings.TrimSuffix(r, ".git")
		if !githubRepoRE.MatchString(r) {
			return nil, fmt.Errorf("%w: %q is not a GitHub repository; write owner/name, as acme/api, or an owner "+
				"for all of its repositories", ErrInvalidRoutine, r)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if len(out) > MaxRoutineTeams {
		return nil, fmt.Errorf("%w: a routine is limited to at most %d repositories", ErrInvalidRoutine, MaxRoutineTeams)
	}
	return out, nil
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
		Kind: RoutineKindLinear, DeliveryID: d.DeliveryID, Events: []string{event},
		Payload: BoundRoutineEventPayload(d.Payload), ReceivedAt: at.UTC(), Scopes: []string{},
	}
	if team := strings.ToUpper(strings.TrimSpace(d.TeamKey)); team != "" {
		params.Scopes = []string{team}
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

// GitHubDelivery is one delivery from the GitHub App's webhook, as the webhook hands it over.
type GitHubDelivery struct {
	// DeliveryID is GitHub's X-GitHub-Delivery. A redelivery carries the same one.
	DeliveryID string
	// Events are every name in RoutineGitHubEvents the delivery answers to, the most specific
	// first: a failed workflow run is "workflow_run.failed" and "workflow_run.completed".
	Events []string
	// Repo is the repository it is in, owner/name.
	Repo string
	// Payload is what a run is briefed with: the parts of the delivery a routine reads, never
	// the whole of it. One over MaxRoutineEventPayloadBytes is kept as a stub.
	Payload    json.RawMessage
	ReceivedAt time.Time
	// SubjectKey is the pull request it is about, GitHubPullSubject's, "" for none: a push to a
	// branch, an issue. SubjectTitle names it ("#42: Fix login").
	SubjectKey   string
	SubjectTitle string
	// Facts are who set it off and the pull request it is about, which a routine's conditions
	// (RoutineGitHubFilters) are asked about.
	Facts GitHubEventFacts
}

// StoreGitHubRoutineEvents keeps d for every enabled github routine that asks for one of its
// events, in its repositories when the routine names some, and whose conditions it meets, and
// reports how many it was kept for. A delivery kept before is not kept again.
func (s *Store) StoreGitHubRoutineEvents(ctx context.Context, d GitHubDelivery) (int, error) {
	events := make([]string, 0, len(d.Events))
	for _, e := range d.Events {
		if canon, ok := CanonicalGitHubEvent(e); ok && !slices.Contains(events, canon) {
			events = append(events, canon)
		}
	}
	if len(events) == 0 || strings.TrimSpace(d.DeliveryID) == "" {
		return 0, nil
	}
	at := d.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	params := db.InsertRoutineEventsParams{
		Kind: RoutineKindGitHub, DeliveryID: d.DeliveryID, Events: events,
		Payload: BoundRoutineEventPayload(d.Payload), ReceivedAt: at.UTC(), Scopes: githubScopes(d.Repo),
	}
	if key := strings.TrimSpace(d.SubjectKey); key != "" {
		_, title := subjectOf("PullRequest", strings.TrimPrefix(key, "PullRequest:"), d.SubjectTitle)
		params.SubjectKey, params.SubjectTitle = &key, &title
	}
	candidates, err := s.q.RoutineEventCandidates(ctx, db.RoutineEventCandidatesParams{
		Kind: RoutineKindGitHub, Events: events, Scopes: params.Scopes,
	})
	if err != nil {
		return 0, fmt.Errorf("routines for GitHub delivery %s: %w", d.DeliveryID, err)
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if ok, _ := githubFiltersFrom(c.GithubFilters).Admits(d.Facts); ok {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	params.OnlyIds = ids
	n, err := s.q.InsertRoutineEvents(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("store GitHub delivery %s for routines: %w", d.DeliveryID, err)
	}
	return int(n), nil
}

// GitHubRoutinesWant reports whether an enabled routine on GitHub events asks for one of
// events on repo ("owner/name"). It is asked before an event that costs GitHub API calls to
// make is made.
func (s *Store) GitHubRoutinesWant(ctx context.Context, repo string, events []string) (bool, error) {
	want, err := s.q.RoutinesWantEvents(ctx, db.RoutinesWantEventsParams{
		Kind: RoutineKindGitHub, Events: events, Scopes: githubScopes(repo),
	})
	if err != nil {
		return false, fmt.Errorf("routines on %v for %s: %w", events, repo, err)
	}
	return want, nil
}

// githubScopes is what a routine's repositories are matched against for a delivery on repo:
// the repository, and its owner, since a routine limited to an owner sees every repository
// of theirs.
func githubScopes(repo string) []string {
	scopes := []string{}
	if repo = strings.ToLower(strings.TrimSpace(repo)); repo != "" {
		scopes = append(scopes, repo)
		if owner, _, ok := strings.Cut(repo, "/"); ok && owner != "" {
			scopes = append(scopes, owner)
		}
	}
	return scopes
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
		"note": fmt.Sprintf("The payload was over %d KB, so only its type, action and url were kept; "+
			"read the rest from where it came from.", MaxRoutineEventPayloadBytes/1024),
	})
	return stub
}

// RoutinesWithPendingEvents is every enabled routine on events with an event waiting.
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

// RoutineEventSubject is what a Linear event is about, for a routine that runs in each ticket's
// conversation (0047, 0048): key tells subjects apart ("Issue:<id>"), and title names one in words ("ENG-12 Fix
// login"). An issue and the comments on it are one subject, as are a project and its updates.
//
// Linear documents no exact payload for each type, so the payload is read leniently: a field
// that is missing or of another shape means the event has no subject, never an error. An event
// with no subject has an empty key, and runs in the routine's own chat.
//
// An issue's and a project's subject is a conversation of its own (LinearConversationKey); a
// cycle's and a label's is not, and runs in the routine's own chat.
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
			Key: r.SubjectKey, Title: r.SubjectTitle, Oldest: r.Oldest.UTC(), Pending: int(r.Pending),
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

// SetRoutineRunChat records the chat a run happened in when it was not the routine's own: a
// Linear issue's or project's conversation.
func (s *Store) SetRoutineRunChat(ctx context.Context, runID, chatID string) error {
	if err := s.q.SetRoutineRunChat(ctx, db.SetRoutineRunChatParams{ID: runID, ChatID: &chatID}); err != nil {
		return fmt.Errorf("record the chat of routine run %s: %w", runID, err)
	}
	return nil
}

// GitHubPullSubject is the subject of an event about a pull request: "PullRequest:<owner>/<name>#<n>",
// with the repository as GitHub names it, since its conversation's key is (GitHubPullKey).
func GitHubPullSubject(repo string, number int) string {
	return fmt.Sprintf("PullRequest:%s#%d", repo, number)
}

// githubConversationPrefix starts a pull request's conversation's key. The review source keys
// a pull request's session so (ghreview.SourceKey); a web message in its chat and a routine's
// run about it are turns of the same one.
const githubConversationPrefix = "github:"

// GitHubPullKey is a pull request's conversation: github:<owner>/<name>#<number>.
func GitHubPullKey(repo string, number int) string {
	return fmt.Sprintf("%s%s#%d", githubConversationPrefix, repo, number)
}

// IsGitHubConversation reports whether a key is a pull request's conversation.
func IsGitHubConversation(sourceKey string) bool {
	repo, num, ok := strings.Cut(strings.TrimPrefix(sourceKey, githubConversationPrefix), "#")
	if !ok || !strings.HasPrefix(sourceKey, githubConversationPrefix) {
		return false
	}
	owner, name, ok := strings.Cut(repo, "/")
	n, err := strconv.Atoi(num)
	return ok && owner != "" && name != "" && !strings.Contains(name, "/") && err == nil && n > 0
}

// ConversationKey is the conversation a subject is: a Linear issue's or project's
// (LinearConversationKey), or a pull request's (GitHubPullKey). "" is a subject that is no
// conversation, whose events run in the routine's own chat.
func ConversationKey(subjectKey string) string {
	if rest, ok := strings.CutPrefix(subjectKey, "PullRequest:"); ok {
		key := githubConversationPrefix + rest
		if IsGitHubConversation(key) {
			return key
		}
		return ""
	}
	return LinearConversationKey(subjectKey)
}

// EnsureConversation is the chat of the conversation key names, opened now when nothing has
// opened it yet: a Linear issue's or project's, or a pull request's.
func (s *Store) EnsureConversation(ctx context.Context, key, title string) (Chat, bool, error) {
	if IsGitHubConversation(key) {
		return s.EnsureGitHubConversation(ctx, key, title)
	}
	return s.EnsureLinearConversation(ctx, key, title)
}

// EnsureGitHubConversation is a pull request's conversation's chat, opened now when the review
// source has not opened it: a mirror chat of origin github, nobody's, under the key its reviews
// and web messages use, with its session. The review source finds it by that key, as it would
// one it opened itself.
func (s *Store) EnsureGitHubConversation(ctx context.Context, sourceKey, title string) (Chat, bool, error) {
	if !IsGitHubConversation(sourceKey) {
		return Chat{}, false, fmt.Errorf("open a pull request's conversation: %q is not one", sourceKey)
	}
	chat, err := s.ChatBySourceKey(ctx, sourceKey)
	if err == nil {
		return chat, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Chat{}, false, err
	}
	created := true
	chat, err = s.CreateMirrorChat(ctx, sourceKey, OriginGitHub, "", title)
	if err != nil {
		again, rerr := s.ChatBySourceKey(ctx, sourceKey)
		if rerr != nil {
			return Chat{}, false, err
		}
		chat, created = again, false
	}
	if _, err := s.UpsertSession(ctx, Session{SourceKind: OriginGitHub, SourceKey: sourceKey}); err != nil {
		return chat, created, err
	}
	return chat, created, nil
}

// NoteRoutineReader records login, a routine's owner, as somebody who may read the conversation
// key their routine ran in: as having touched the Linear issue, or with a viewer's row on the
// pull request's chat, which also lists it among their chats. An empty login — a system
// routine's run — records nobody.
func (s *Store) NoteRoutineReader(ctx context.Context, key, chatID, login string) error {
	if login == "" {
		return nil
	}
	if IsGitHubConversation(key) {
		_, err := s.JoinChat(ctx, chatID, login, RoleViewer, GrantedByRoutine)
		return err
	}
	return s.RecordLinearPeople(ctx, key, []LinearPerson{{Login: login, Via: LinearViaRoutine}}, time.Now())
}

// NotePullRequestAuthors makes the author of each pull request runID's events are about an
// editor of chatID, that pull request's conversation, which also lists it among their chats: a
// routine reviewing or fixing somebody's pull request is work on their behalf, whoever owns the
// routine, and they may answer it there. A viewer's row they had is raised. An author is
// somebody only once their GitHub account is proven to be a Podium login (Connect GitHub, by its
// numeric id); anybody else is nobody to tell.
func (s *Store) NotePullRequestAuthors(ctx context.Context, runID, chatID string) error {
	ids, err := s.q.RoutineRunPullAuthors(ctx, runID)
	if err != nil {
		return fmt.Errorf("read the pull request authors of run %s: %w", runID, err)
	}
	for _, id := range ids {
		login, err := s.IdentityLogin(ctx, ProviderGitHub, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := s.GrantChatEditor(ctx, chatID, login, GrantedByAuthor); err != nil {
			return err
		}
	}
	return nil
}

// LinearConversationKey is the conversation a subject is (0048): an issue's, linear:<issue id>,
// which is the key every agent session on the issue is a turn of, or a project's,
// linear:project:<project id>. "" is a subject that is no conversation — a cycle, a label, or
// none — whose events run in the routine's own chat.
func LinearConversationKey(subjectKey string) string {
	kind, id, ok := strings.Cut(subjectKey, ":")
	if !ok || id == "" {
		return ""
	}
	switch kind {
	case "Issue":
		return LinearIssueKey(id)
	case "Project":
		return LinearProjectKey(id)
	}
	return ""
}

// RoutineRanInChat reports whether a routine's run happened in chatID as a conversation that is
// no routine's own: a pull request's, which admins then read.
func (s *Store) RoutineRanInChat(ctx context.Context, chatID string) (bool, error) {
	ok, err := s.q.RoutineRanInChat(ctx, chatID)
	if err != nil {
		return false, fmt.Errorf("ask whether a routine ran in chat %s: %w", chatID, err)
	}
	return ok, nil
}
