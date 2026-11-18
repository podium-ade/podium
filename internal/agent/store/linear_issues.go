package store

// A Linear issue's or project's conversation (0048, docs/agent.md "Linear"): one chat and one
// session per issue, keyed linear:<issue id>, however its turns were started — a delegation, an
// @mention, a routine run about it. What the webhooks said about the issue is kept beside it
// (linear_issues), and so is who touched it (linear_people), which is who may read it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// Linear conversation keys. An issue's is the key the poller always used for its ticket, so a
// workspace that moves from the poller to the app keeps one session per ticket.
const (
	linearKeyPrefix        = "linear:"
	linearProjectKeyPrefix = "linear:project:"
	linearSessionKeyPrefix = "linear:session:"
)

// LinearIssueKey is the conversation of one Linear issue.
func LinearIssueKey(issueID string) string { return linearKeyPrefix + issueID }

// LinearProjectKey is the conversation of one Linear project.
func LinearProjectKey(projectID string) string { return linearProjectKeyPrefix + projectID }

// LinearKind is what a conversation key is about: "issue", "project", or "" for a key that is
// no Linear issue's or project's conversation — another surface's, or a Linear agent session's
// from before 0048.
func LinearKind(sourceKey string) (kind, id string) {
	switch {
	case strings.HasPrefix(sourceKey, linearSessionKeyPrefix):
		return "", ""
	case strings.HasPrefix(sourceKey, linearProjectKeyPrefix):
		id = strings.TrimPrefix(sourceKey, linearProjectKeyPrefix)
		if id == "" {
			return "", ""
		}
		return LinearKindProject, id
	case strings.HasPrefix(sourceKey, linearKeyPrefix):
		id = strings.TrimPrefix(sourceKey, linearKeyPrefix)
		if id == "" || strings.Contains(id, ":") {
			return "", ""
		}
		return LinearKindIssue, id
	}
	return "", ""
}

// IsLinearConversation reports whether a key is a Linear issue's or project's conversation.
func IsLinearConversation(sourceKey string) bool {
	kind, _ := LinearKind(sourceKey)
	return kind != ""
}

// What a Linear conversation is about.
const (
	LinearKindIssue   = "issue"
	LinearKindProject = "project"
)

// How somebody touched a Linear issue (linear_people.via).
const (
	LinearViaCreated   = "created"
	LinearViaAssigned  = "assigned"
	LinearViaDelegated = "delegated"
	LinearViaMentioned = "mentioned"
	LinearViaPrompted  = "prompted"
	LinearViaCommented = "commented"
	LinearViaChanged   = "changed"
	LinearViaRoutine   = "routine"
	// LinearViaPersonMentioned is a person @mentioned in the issue's description or a comment;
	// LinearViaMentioned is a person who @mentioned the app.
	LinearViaPersonMentioned = "person_mentioned"
)

// LinearIssue is what the webhooks said about the issue or project a conversation is about.
type LinearIssue struct {
	SourceKey  string
	Kind       string
	LinearID   string
	Identifier string
	Title      string
	URL        string
	TeamKey    string
	StateName  string
	StateType  string
	// AskedAt is set while the conversation waits on a person, and AskedOf is whose turn asked.
	AskedAt   *time.Time
	AskedOf   string
	UpdatedAt time.Time
}

// LinearPerson is one way somebody touched an issue or project. Exactly one of LinearUser
// ("<organization>/<user>", as identities keys a Linear account) and Login is set.
type LinearPerson struct {
	LinearUser string
	Login      string
	Via        string
}

// UpsertLinearIssue records what a delivery said about an issue or project. Empty fields keep
// what was said before. A key that is no Linear conversation is refused.
func (s *Store) UpsertLinearIssue(ctx context.Context, li LinearIssue) error {
	kind, id := LinearKind(li.SourceKey)
	if kind == "" {
		return fmt.Errorf("record a Linear issue: %q is not an issue's or project's conversation", li.SourceKey)
	}
	at := li.UpdatedAt
	if at.IsZero() {
		at = time.Now()
	}
	err := s.q.UpsertLinearIssue(ctx, db.UpsertLinearIssueParams{
		SourceKey: li.SourceKey, Kind: kind, LinearID: id,
		Identifier: strings.TrimSpace(li.Identifier), Title: strings.TrimSpace(li.Title),
		Url: strings.TrimSpace(li.URL), TeamKey: strings.ToUpper(strings.TrimSpace(li.TeamKey)),
		StateName: strings.TrimSpace(li.StateName), StateType: strings.TrimSpace(li.StateType),
		UpdatedAt: at.UTC(),
	})
	if err != nil {
		return fmt.Errorf("record Linear %s %s: %w", kind, id, err)
	}
	return nil
}

// GetLinearIssue is what the webhooks said about one conversation's issue or project.
func (s *Store) GetLinearIssue(ctx context.Context, sourceKey string) (LinearIssue, error) {
	row, err := s.q.GetLinearIssue(ctx, sourceKey)
	if noRows(err) {
		return LinearIssue{}, fmt.Errorf("%w: Linear issue %s", ErrNotFound, sourceKey)
	}
	if err != nil {
		return LinearIssue{}, fmt.Errorf("read Linear issue %s: %w", sourceKey, err)
	}
	return LinearIssue{
		SourceKey: row.SourceKey, Kind: row.Kind, LinearID: row.LinearID, Identifier: row.Identifier,
		Title: row.Title, URL: row.Url, TeamKey: row.TeamKey, StateName: row.StateName, StateType: row.StateType,
		AskedAt: utcPtr(row.AskedAt), AskedOf: row.AskedOf, UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

// SetLinearIssueAsked marks a conversation as waiting on a person (asked true), whose turn asked
// being askedOf, or as not waiting (asked false). A key that is no Linear conversation is
// ignored.
func (s *Store) SetLinearIssueAsked(ctx context.Context, sourceKey string, asked bool, askedOf string) error {
	kind, id := LinearKind(sourceKey)
	if kind == "" {
		return nil
	}
	if !asked {
		if err := s.q.ClearLinearIssueAsked(ctx, sourceKey); err != nil {
			return fmt.Errorf("clear the question in %s: %w", sourceKey, err)
		}
		return nil
	}
	now := time.Now().UTC()
	if err := s.q.SetLinearIssueAsked(ctx, db.SetLinearIssueAskedParams{
		SourceKey: sourceKey, Kind: kind, LinearID: id, AskedAt: &now, AskedOf: askedOf,
	}); err != nil {
		return fmt.Errorf("record the question in %s: %w", sourceKey, err)
	}
	return nil
}

// RecordLinearPeople records who touched an issue or project. A person with neither a Linear
// account nor a login, or with both, is skipped: identity is never guessed.
func (s *Store) RecordLinearPeople(ctx context.Context, sourceKey string, people []LinearPerson, at time.Time) error {
	if !IsLinearConversation(sourceKey) {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	var errs []error
	for _, p := range people {
		user, login, via := strings.TrimSpace(p.LinearUser), strings.TrimSpace(p.Login), strings.TrimSpace(p.Via)
		if (user == "") == (login == "") || via == "" {
			continue
		}
		if err := s.q.InsertLinearPerson(ctx, db.InsertLinearPersonParams{
			SourceKey: sourceKey, LinearUser: user, Login: login, Via: via, SeenAt: at.UTC(),
		}); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("record who touched %s: %w", sourceKey, err)
	}
	return nil
}

// LinearTouched reports whether login touched the issue or project a conversation is about.
func (s *Store) LinearTouched(ctx context.Context, sourceKey, login string) (bool, error) {
	if !IsLinearConversation(sourceKey) || login == "" {
		return false, nil
	}
	ok, err := s.q.LinearTouched(ctx, db.LinearTouchedParams{SourceKey: sourceKey, Viewer: login})
	if err != nil {
		return false, fmt.Errorf("read who touched %s: %w", sourceKey, err)
	}
	return ok, nil
}

// EnsureLinearConversation is the chat of a Linear issue's or project's conversation, opened now
// when it has none — by a routine run about an issue nobody delegated or mentioned the app on.
// It is opened as the first prompt on the issue would open it: a mirrored chat of origin linear
// that nobody owns until a Podium person speaks in it. Its session is opened with it, so a
// prompt that arrives later is a turn of the same one. created reports whether this call opened
// the chat; two openers at once read the winner's.
func (s *Store) EnsureLinearConversation(ctx context.Context, sourceKey, title string) (Chat, bool, error) {
	if !IsLinearConversation(sourceKey) {
		return Chat{}, false, fmt.Errorf("open a Linear conversation: %q is not an issue's or project's", sourceKey)
	}
	chat, err := s.ChatBySourceKey(ctx, sourceKey)
	if err == nil {
		return chat, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Chat{}, false, err
	}
	created := true
	chat, err = s.CreateMirrorChat(ctx, sourceKey, OriginLinear, "", title)
	if err != nil {
		again, rerr := s.ChatBySourceKey(ctx, sourceKey)
		if rerr != nil {
			return Chat{}, false, err
		}
		chat, created = again, false
	}
	if _, err := s.UpsertSession(ctx, Session{SourceKind: OriginLinear, SourceKey: sourceKey}); err != nil {
		return chat, created, err
	}
	return chat, created, nil
}

// LinearConversation is one row of the Linear view.
type LinearConversation struct {
	ChatID    string
	SourceKey string
	// Kind is LinearKindIssue or LinearKindProject.
	Kind       string
	Identifier string
	// Title is the issue's or project's title as the webhooks gave it, and the chat's title
	// when they gave none.
	Title     string
	URL       string
	TeamKey   string
	StateName string
	StateType string
	// State is where Podium is with it: LinearStateWaiting, …Working, …Failed, …Done or …New.
	State          string
	AskedOf        string
	LastActivityAt time.Time
	LastText       string
	CostUSD        float64
	// MyRole is the caller's role in the chat, "" for an admin who reads it as an admin.
	MyRole string
	// Involved is a conversation the caller is involved in (linear_involved): assigned or
	// created the issue, asked Podium about it, spoke in it, or was @mentioned in it.
	Involved bool
}

// Where Podium is with a Linear conversation.
const (
	LinearStateWaiting = "waiting"
	LinearStateWorking = "working"
	LinearStateFailed  = "failed"
	LinearStateDone    = "done"
	LinearStateNew     = "new"
	// LinearStateAttention is a filter, not a state: waiting and failed together.
	LinearStateAttention = "attention"
)

// ErrBadLinearCursor is a Linear view cursor this store did not hand out.
var ErrBadLinearCursor = errors.New("not a cursor of the Linear view")

// LinearConversationFilter narrows the Linear view.
type LinearConversationFilter struct {
	// SeeAll is an admin, an owner or the dev token, who may read every conversation.
	SeeAll bool
	// All lists every conversation the caller may read, not only those they are involved in.
	// The store does not check it is SeeAll's to ask: the API does.
	All bool
	// Team is a team key, "" for every team.
	Team string
	// State is "", a state, or LinearStateAttention: a board's column is one state.
	State string
}

// LinearConversationPage is one page of the Linear view.
type LinearConversationPage struct {
	Rows []LinearConversation
	// Next is the cursor of the next page, "" for none.
	Next string
	// Total is how many conversations the filter matches, counted on the first page only.
	Total int
}

// ListLinearConversations is a page of the Linear view for viewer, most recently active first.
func (s *Store) ListLinearConversations(
	ctx context.Context, viewer string, f LinearConversationFilter, limit int, cursor string,
) (LinearConversationPage, error) {
	limit = clampLimit(limit)
	params := db.ListLinearConversationsParams{
		Viewer: viewer, SeeAll: f.SeeAll, ListAll: f.All, Team: strings.ToUpper(strings.TrimSpace(f.Team)),
		FilterState: strings.TrimSpace(f.State),
		PageLimit:   int32(limit + 1),
	}
	if cursor != "" {
		at, id, ok := parseLinearCursor(cursor)
		if !ok {
			return LinearConversationPage{}, fmt.Errorf("%w: %q", ErrBadLinearCursor, cursor)
		}
		params.AfterAt, params.AfterID = &at, id
	}
	rows, err := s.q.ListLinearConversations(ctx, params)
	if err != nil {
		return LinearConversationPage{}, fmt.Errorf("list Linear conversations: %w", err)
	}
	total := 0
	if cursor == "" && len(rows) > 0 {
		total = int(rows[0].Total)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = last.LastActivityAt.UTC().Format(time.RFC3339Nano) + "|" + last.ChatID
	}
	out := make([]LinearConversation, 0, len(rows))
	for _, r := range rows {
		title := r.IssueTitle
		if title == "" {
			title = r.ChatTitle
		}
		url := r.IssueUrl
		if url == "" {
			url = r.SourceUrl
		}
		out = append(out, LinearConversation{
			ChatID: r.ChatID, SourceKey: deref(r.SourceKey), Kind: r.Kind, Identifier: r.Identifier, Title: title,
			URL: url, TeamKey: r.TeamKey, StateName: r.StateName, StateType: r.StateType, State: r.State,
			AskedOf: r.AskedOf, LastActivityAt: r.LastActivityAt.UTC(), LastText: preview(r.LastText, ChatPreviewChars),
			CostUSD: r.CostUsd, MyRole: r.MyRole, Involved: r.Involved,
		})
	}
	return LinearConversationPage{Rows: out, Next: next, Total: total}, nil
}

// LinearConversationTeams is every team a conversation viewer lists is in: those they may read
// and, unless all, are involved in.
func (s *Store) LinearConversationTeams(ctx context.Context, viewer string, seeAll, all bool) ([]string, error) {
	teams, err := s.q.LinearConversationTeams(ctx, db.LinearConversationTeamsParams{Viewer: viewer, SeeAll: seeAll, ListAll: all})
	if err != nil {
		return nil, fmt.Errorf("list the Linear view's teams: %w", err)
	}
	return teams, nil
}

func parseLinearCursor(cursor string) (time.Time, string, bool) {
	at, id, ok := strings.Cut(cursor, "|")
	if !ok || id == "" {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", false
	}
	return t, id, true
}
