// Package store is the conductor's own Postgres database (podium_agent): sessions, turns,
// the relay ledger and its settings. It is a separate database from the control plane's and
// nothing here imports internal/server/store — the conductor is an API client of
// podium-server, not a second owner of its schema.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// ErrNotFound is what the readers return for a row that is not there.
var ErrNotFound = errors.New("agent store: not found")

// ErrConflict is what a write returns when the name it wanted is already taken.
var ErrConflict = errors.New("agent store: already exists")

// ErrInvalidChatTitle is a title a rename would not store: empty, too long, or holding
// a control character. The handler maps it to InvalidArgument.
var ErrInvalidChatTitle = errors.New("invalid chat title")

// ErrInvalidSlackChannelDescription is a note the UI would not store: too long.
var ErrInvalidSlackChannelDescription = errors.New("invalid slack channel description")

// Page limits, the same shape podium-server uses.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

// Turn statuses. These are exactly the values the turns.status check constraint allows.
const (
	TurnRunning   = "running"
	TurnSucceeded = "succeeded"
	TurnFailed    = "failed"
	TurnLost      = "lost"
	TurnCancelled = "cancelled"
	TurnTimeout   = "timeout"
)

// Store is a handle on the conductor's database. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// New opens a pgx pool against databaseURL and verifies it is reachable. Call Migrate
// before using it. The caller owns the returned Store and must Close it.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse agent database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open agent postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping agent postgres: %w", err)
	}
	return &Store{pool: pool, q: db.New(pool)}, nil
}

// Close releases every pooled connection. It is idempotent.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether Postgres is reachable. /readyz calls this.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping agent postgres: %w", err)
	}
	return nil
}

// Session is one conversation. Its identity is SourceKey, which the source computes; the
// conductor never parses it.
type Session struct {
	ID         string
	SourceKind string
	SourceKey  string
	Profile    string
	Playbook   string
	CreatedAt  time.Time
	LastTurnAt *time.Time
	// GitHubReviewID is the pull-request review this session submitted. Zero means none.
	GitHubReviewID int64
	// Sharing is what a list says about the caller and the chat this session is the record
	// of (phase 5). Zero for a session read by id, and for one with no chat (a ticket the
	// Linear poller saw, dev).
	Sharing ChatSharing
}

// ChatSharing is one caller's relationship to one chat, as a list reports it.
type ChatSharing struct {
	// ChatID is the chat a session is the record of. Empty on a chat (it is the chat's ID).
	ChatID string
	// MyRole is chat_role: RoleOwner, RoleEditor, RoleViewer, or "" for none.
	MyRole string
	// AccessReason is why the caller can see it: AccessOwner, AccessParticipant,
	// AccessShared or AccessWorkspace.
	AccessReason string
	// SharedBy is who shared it with them by name, or "link"/"members" for a row opening it
	// wrote. Empty when it was not shared with them.
	SharedBy string
	// UnreadCount is what somebody else said after their read mark.
	UnreadCount int64
}

// Turn is one inbound message turned into one Podium task.
type Turn struct {
	ID         string
	SessionID  string
	TaskID     string
	TriggerRef string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	NumTurns   *int
	CostUSD    *float64
	FinalText  string
	// Backend is what this turn actually ran on, recorded at creation. Zero for a turn from
	// before the columns existed — never guessed from the playbook, whose model is only a
	// default and may have been edited since.
	Backend Backend
	// StartedBy is the Podium login whose turn this was and whose credentials it ran with,
	// TurnStartedByApp for a review nobody in Podium asked for, and empty for a turn from
	// before the column or one with no person.
	StartedBy string
	// RoutineRunID is the routine run this turn is, "" for a turn somebody asked for (0043).
	RoutineRunID string
}

// TurnStartedByApp is turns.started_by for a review the GitHub App ran on nobody's behalf:
// an automatic review, or a review command from somebody who has not linked GitHub.
const TurnStartedByApp = "github-app"

// TurnStartedByRoutine is turns.started_by for a system routine's run, which nobody started:
// "routine:<id>". Usage shows each system routine as its own line.
func TurnStartedByRoutine(routineID string) string { return "routine:" + routineID }

// Backend is the resolved agent, model and effort a turn ran on, and the provider that was
// billed for it. Provider is stored rather than derived from Agent so that remapping a
// backend to another provider later cannot rewrite what past turns cost whom.
type Backend struct {
	Agent    string
	Model    string
	Effort   string
	Provider string
}

// ChatChoice is what a chat is answered on: the OVERRIDE a person picked, all empty for the
// assistant's own model.
//
// The override and not the resolution, deliberately. A conversation that never asked for
// anything specific stays on whatever profile.yaml says and follows it when that changes;
// storing the resolved triple — which `turns` already records per turn — would pin every
// chat to the model its first turn happened to run, turning a default into a choice nobody
// made.
type ChatChoice struct {
	Agent  string
	Model  string
	Effort string
}

// UpsertSession returns the session for want.SourceKey, creating it if it is new. The playbook
// of an existing session is never changed: one session, one playbook, fixed at creation. The
// returned row is authoritative, so a caller that wanted a different playbook can see it did
// not get one.
func (s *Store) UpsertSession(ctx context.Context, want Session) (Session, error) {
	id := want.ID
	if id == "" {
		id = ids.New("sess")
	}
	created := want.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	row, err := s.q.UpsertSession(ctx, db.UpsertSessionParams{
		ID:         id,
		SourceKind: want.SourceKind,
		SourceKey:  want.SourceKey,
		Profile:    want.Profile,
		Playbook:   want.Playbook,
		CreatedAt:  created,
	})
	if err != nil {
		return Session{}, fmt.Errorf("upsert session %s: %w", want.SourceKey, err)
	}
	return sessionFromRow(row), nil
}

// GetSession reads one session by id.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row, err := s.q.GetSession(ctx, id)
	if noRows(err) {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session %s: %w", id, err)
	}
	return sessionFromRow(row), nil
}

// GetSessionByKey reads one session by its source key.
func (s *Store) GetSessionByKey(ctx context.Context, sourceKey string) (Session, error) {
	row, err := s.q.GetSessionByKey(ctx, sourceKey)
	if noRows(err) {
		return Session{}, fmt.Errorf("%w: session for %s", ErrNotFound, sourceKey)
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session for %s: %w", sourceKey, err)
	}
	return sessionFromRow(row), nil
}

// ListSessions returns sessions newest first. The cursor is the last id of the previous
// page, which sorts by time because every id is a ULID.
//
// all is the dev token: every source. Otherwise the sessions whose chat login may read:
// their own web chats and every mirrored conversation they took part in.
//
// section is optional and is one of the Section constants; leaving it out is SectionMine.
func (s *Store) ListSessions(ctx context.Context, login string, all bool, limit int, cursor string, section ...string) ([]Session, string, error) {
	limit = clampLimit(limit)
	sec := ""
	if len(section) > 0 {
		sec = section[0]
	}
	if !validSection(sec) {
		return nil, "", fmt.Errorf("list sessions: %w: %q", ErrInvalidSection, sec)
	}
	rows, err := s.q.ListSessions(ctx, db.ListSessionsParams{
		IncludeAll: all,
		Viewer:     login,
		Section:    sec,
		AfterID:    cursor,
		PageLimit:  int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		sess := sessionFromRow(db.Session{
			ID: r.ID, SourceKind: r.SourceKind, SourceKey: r.SourceKey, Profile: r.Profile,
			Playbook: r.Playbook, CreatedAt: r.CreatedAt, LastTurnAt: r.LastTurnAt,
			GithubReviewID: r.GithubReviewID,
		})
		sess.Sharing = ChatSharing{
			ChatID: r.ChatID, MyRole: r.MyRole, AccessReason: r.AccessReason,
			SharedBy: r.SharedBy, UnreadCount: r.UnreadCount,
		}
		out = append(out, sess)
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// CreateTurn records a turn as running. The task does not exist yet: SetTurnTask fills it
// in once CreateTask has answered.
func (s *Store) CreateTurn(ctx context.Context, sessionID, triggerRef string, b Backend) (Turn, error) {
	return s.CreateTurnFor(ctx, sessionID, triggerRef, "", b)
}

// CreateTurnFor is CreateTurn with the login the turn is for, which is what usage is broken
// down by. CreateTurn is the same call for a turn with nobody behind it.
func (s *Store) CreateTurnFor(ctx context.Context, sessionID, triggerRef, startedBy string, b Backend) (Turn, error) {
	now := time.Now().UTC()
	row, err := s.q.CreateTurn(ctx, db.CreateTurnParams{
		ID:         ids.New("turn"),
		SessionID:  sessionID,
		TriggerRef: triggerRef,
		Status:     TurnRunning,
		StartedAt:  now,
		Agent:      b.Agent,
		Model:      b.Model,
		Effort:     b.Effort,
		Provider:   b.Provider,
		StartedBy:  startedBy,
	})
	if err != nil {
		return Turn{}, fmt.Errorf("create turn for session %s: %w", sessionID, err)
	}
	if err := s.q.TouchSession(ctx, db.TouchSessionParams{LastTurnAt: &now, ID: sessionID}); err != nil {
		return Turn{}, fmt.Errorf("touch session %s: %w", sessionID, err)
	}
	return turnFromRow(row), nil
}

// SetTurnTask binds a turn to the Podium task that is running it.
func (s *Store) SetTurnTask(ctx context.Context, turnID, taskID string) error {
	if err := s.q.SetTurnTask(ctx, db.SetTurnTaskParams{TaskID: &taskID, ID: turnID}); err != nil {
		return fmt.Errorf("set task %s on turn %s: %w", taskID, turnID, err)
	}
	return nil
}

// FinishTurn records how a turn ended. numTurns and costUSD are nil when the runtime's
// turn.json never arrived; finalText is what was actually relayed, which may be empty.
func (s *Store) FinishTurn(
	ctx context.Context, turnID, status string, numTurns *int, costUSD *float64, finalText string,
) error {
	now := time.Now().UTC()
	var turns *int32
	if numTurns != nil {
		v := int32(*numTurns)
		turns = &v
	}
	var text *string
	if finalText != "" {
		text = &finalText
	}
	if err := s.q.FinishTurn(ctx, db.FinishTurnParams{
		Status:     status,
		FinishedAt: &now,
		NumTurns:   turns,
		CostUsd:    costUSD,
		FinalText:  text,
		ID:         turnID,
	}); err != nil {
		return fmt.Errorf("finish turn %s as %s: %w", turnID, status, err)
	}
	return nil
}

// ListTurns returns a session's turns, newest first.
func (s *Store) ListTurns(ctx context.Context, sessionID string, limit int) ([]Turn, error) {
	rows, err := s.q.ListTurns(ctx, db.ListTurnsParams{
		SessionID: sessionID, PageLimit: int32(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list turns of session %s: %w", sessionID, err)
	}
	out := make([]Turn, 0, len(rows))
	for _, r := range rows {
		out = append(out, turnFromRow(r))
	}
	return out, nil
}

// ListRunningTurns is the recovery pass's working set: every turn that was in flight when
// the process died.
func (s *Store) ListRunningTurns(ctx context.Context) ([]Turn, error) {
	rows, err := s.q.ListRunningTurns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list running turns: %w", err)
	}
	out := make([]Turn, 0, len(rows))
	for _, r := range rows {
		out = append(out, turnFromRow(r))
	}
	return out, nil
}

// GetTurn reads one turn by id.
func (s *Store) GetTurn(ctx context.Context, id string) (Turn, error) {
	row, err := s.q.GetTurn(ctx, id)
	if noRows(err) {
		return Turn{}, fmt.Errorf("%w: turn %s", ErrNotFound, id)
	}
	if err != nil {
		return Turn{}, fmt.Errorf("get turn %s: %w", id, err)
	}
	return turnFromRow(row), nil
}

// MarkRelayed claims (taskID, seq) for relaying. The bool is "this is the first time", and
// it is the whole of the exactly-once guarantee: a replayed stream claims nothing and the
// caller therefore says nothing twice.
func (s *Store) MarkRelayed(ctx context.Context, taskID string, seq uint64) (bool, error) {
	if seq > 1<<62 {
		return false, fmt.Errorf("task %s: event seq %d overflows bigint", taskID, seq)
	}
	_, err := s.q.MarkRelayed(ctx, db.MarkRelayedParams{TaskID: taskID, Seq: int64(seq)})
	if noRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark relayed %s/%d: %w", taskID, seq, err)
	}
	return true, nil
}

// MaxRelayedSeq is where a resumed follow starts: the highest seq this conductor has
// already said out loud for a task.
func (s *Store) MaxRelayedSeq(ctx context.Context, taskID string) (uint64, error) {
	high, err := s.q.MaxRelayedSeq(ctx, taskID)
	if err != nil {
		return 0, fmt.Errorf("read max relayed seq of %s: %w", taskID, err)
	}
	if high < 0 {
		return 0, nil
	}
	return uint64(high), nil
}

// CountRelayed is how many of a task's events have been relayed. Tests use it to prove
// "exactly once".
func (s *Store) CountRelayed(ctx context.Context, taskID string) (int, error) {
	n, err := s.q.CountRelayed(ctx, taskID)
	if err != nil {
		return 0, fmt.Errorf("count relayed of %s: %w", taskID, err)
	}
	return int(n), nil
}

// PutSetting stores one JSON value under key. Step 18 keeps the provider-key metadata here;
// nothing in this step writes one.
func (s *Store) PutSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	if err := s.q.PutSetting(ctx, db.PutSettingParams{
		Key: key, Value: raw, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("put setting %s: %w", key, err)
	}
	return nil
}

// GetSetting decodes one stored value into out. ErrNotFound means the key was never set.
func (s *Store) GetSetting(ctx context.Context, key string, out any) error {
	raw, err := s.q.GetSetting(ctx, key)
	if noRows(err) {
		return fmt.Errorf("%w: setting %s", ErrNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("get setting %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode setting %s: %w", key, err)
	}
	return nil
}

// SettingsWithPrefix is every stored value whose key starts with prefix, raw, keyed by the
// full key. It is how the people who each saved their own provider credential are found.
func (s *Store) SettingsWithPrefix(ctx context.Context, prefix string) (map[string][]byte, error) {
	rows, err := s.q.ListSettingsByPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list settings %s*: %w", prefix, err)
	}
	out := make(map[string][]byte, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// DeleteSetting removes one stored value. A key that was never set is not an error: the
// caller wants it gone, and it is.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if err := s.q.DeleteSetting(ctx, key); err != nil {
		return fmt.Errorf("delete setting %s: %w", key, err)
	}
	return nil
}

// LinearCursorKey is the only key in the linear_cursor table: the high-water mark of
// Issue.updatedAt the Linear source has already turned into events.
const LinearCursorKey = "issues_updated_at"

// GetLinearCursor reads the poll watermark. ErrNotFound means the source has never run
// against this database, which is what makes the first tick look 24 hours back.
func (s *Store) GetLinearCursor(ctx context.Context, key string) (time.Time, error) {
	at, err := s.q.GetLinearCursor(ctx, key)
	if noRows(err) {
		return time.Time{}, fmt.Errorf("%w: linear cursor %s", ErrNotFound, key)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get linear cursor %s: %w", key, err)
	}
	return at.UTC(), nil
}

// PutLinearCursor advances the poll watermark. The caller writes it AFTER the page's
// events have been handed to the conductor, so a crash in between replays the page rather
// than losing it.
func (s *Store) PutLinearCursor(ctx context.Context, key string, at time.Time) error {
	if err := s.q.PutLinearCursor(ctx, db.PutLinearCursorParams{Key: key, UpdatedAt: at.UTC()}); err != nil {
		return fmt.Errorf("put linear cursor %s: %w", key, err)
	}
	return nil
}

func sessionFromRow(r db.Session) Session {
	return Session{
		ID:             r.ID,
		SourceKind:     r.SourceKind,
		SourceKey:      r.SourceKey,
		Profile:        r.Profile,
		Playbook:       r.Playbook,
		CreatedAt:      r.CreatedAt.UTC(),
		LastTurnAt:     utcPtr(r.LastTurnAt),
		GitHubReviewID: r.GithubReviewID,
	}
}

// SetGitHubReviewID records the latest pull-request review a session submitted. The next
// round of review starts from its commit.
func (s *Store) SetGitHubReviewID(ctx context.Context, sourceKey string, reviewID int64) error {
	if reviewID <= 0 {
		return fmt.Errorf("set github review %s: id must be positive", sourceKey)
	}
	n, err := s.q.SetGitHubReviewID(ctx, db.SetGitHubReviewIDParams{
		ReviewID:  reviewID,
		SourceKey: sourceKey,
	})
	if err != nil {
		return fmt.Errorf("set github review %s: %w", sourceKey, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, sourceKey)
	}
	return nil
}

func turnFromRow(r db.Turn) Turn {
	t := Turn{
		ID:           r.ID,
		SessionID:    r.SessionID,
		TaskID:       deref(r.TaskID),
		TriggerRef:   r.TriggerRef,
		Status:       r.Status,
		StartedAt:    r.StartedAt.UTC(),
		FinishedAt:   utcPtr(r.FinishedAt),
		CostUSD:      r.CostUsd,
		FinalText:    deref(r.FinalText),
		StartedBy:    r.StartedBy,
		RoutineRunID: deref(r.RoutineRunID),
		Backend: Backend{
			Agent:    deref(r.Agent),
			Model:    deref(r.Model),
			Effort:   deref(r.Effort),
			Provider: deref(r.Provider),
		},
	}
	if r.NumTurns != nil {
		n := int(*r.NumTurns)
		t.NumTurns = &n
	}
	return t
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return limit
	}
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// ---------------------------------------------------------------------------
// The web chat (step 21)
// ---------------------------------------------------------------------------

// Chat message roles. They are the same two words conductor.RoleUser/RoleAssistant use —
// deliberately duplicated rather than imported, because the conductor imports this package
// and one of them has to be the copy. These are the column's values; those are the brief
// schema's.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	// RoleProgress is a line a task said on its way to an answer. It is the task talking,
	// so it is stored and rendered like anything else it said — but it is not an answer,
	// which is why it is not RoleAssistant: the brief leaves it out, and an attachment
	// lands on the answer rather than on the last thought before it.
	RoleProgress = "progress"
	// RoleActivity is one tool call or reasoning block, as the JSON document the runtime
	// wrote. It is for the web chat to draw and is never history: the brief and the chat
	// list's preview both leave it out.
	RoleActivity = "activity"
	// RoleNotice is Podium saying something about the chat itself rather than in it: that it
	// became multiplayer, and from then on answers only when mentioned. It is drawn as a
	// divider, and is never history or a preview.
	RoleNotice = "notice"
)

// ChatSourceKeyPrefix is what a chat's session key starts with. The chat source builds it
// and the conductor treats it as opaque; the store needs it only to answer "is a turn of
// this chat running" in the same query that lists the chats.
const ChatSourceKeyPrefix = "chat:"

// ChatSourceKey is the session identity of one chat.
func ChatSourceKey(chatID string) string { return ChatSourceKeyPrefix + chatID }

// The origins a chat can have. OriginWeb is a conversation Podium owns and may be written
// to; the others are ones it mirrors, which live on their own surface and are answered there:
// a Slack thread, a pull request, a Linear agent session.
const (
	OriginWeb    = "web"
	OriginSlack  = "slack"
	OriginGitHub = "github"
	OriginLinear = "linear"
)

// DefaultChatTitle is what a chat created with no title is called.
const DefaultChatTitle = "New chat"

// MaxChatTitleRunes is the longest title RenameChat will store. The rail truncates
// visually well before this; the cap exists so a paste cannot write a multi-kilobyte
// title into every ListChats payload.
const MaxChatTitleRunes = 80

// ChatPreviewChars is how much of the last message the chat list shows.
const ChatPreviewChars = 80

// Chat is one web-chat conversation, owned by the login that created it.
type Chat struct {
	ID    string
	Title string
	// HistorySeeded is a mirrored chat whose record was seeded with what its conversation
	// said before Podium was in it (0039).
	HistorySeeded bool
	// StartedFromChat is the conversation the assistant opened this chat from, "" for a chat
	// somebody opened themselves or one whose origin was deleted (0041).
	StartedFromChat string
	// RoutineID is the routine this chat belongs to, "" for any other chat (0043). A routine
	// chat is a web chat its owner writes in, and is never shared.
	RoutineID string
	// SourceURL is the link to a mirrored chat's conversation where it lives (a Slack
	// thread's permalink), "" when the source gave none (0038).
	SourceURL string
	// ChannelPrivacy is what Slack says about a mirrored chat's channel (ChannelPublic, …),
	// "" when it is not known (0037).
	ChannelPrivacy string
	// DeletedAt is set on a mirrored chat its owner deleted (0036). It stays in the database
	// as the conversation's record, and is nobody's but the dev token's.
	DeletedAt *time.Time
	// Login owns the chat, and is empty for a MIRRORED conversation: a Slack thread
	// belongs to the workspace rather than to a Podium identity. Empty is what makes it
	// readable by every login and renameable and deletable by none, because both of those
	// queries filter on `login = @login` and no null matches that.
	Login string
	// Origin is where the conversation actually lives: OriginWeb for a chat Podium owns,
	// OriginSlack for a thread it is only mirroring. The UI reads it to decide whether the
	// composer is offered at all.
	Origin string
	// StartedBy is the person who asked first, by display name. It is a mirrored thread's
	// attribution, in place of the login it has not got, and empty for a web chat.
	StartedBy string
	// Participants is everyone who has spoken, first appearance first. Loaded for one chat
	// and not for a list: it is a second query per conversation and the list only needs
	// StartedBy.
	Participants []ChatParticipant
	CreatedAt    time.Time
	// ChatChoice is what this chat is answered on, remembered so a model is picked once per
	// conversation rather than on every message.
	ChatChoice
	// AutoTitle is true when Podium may rewrite Title from the first query. False when
	// the caller supplied a title at create.
	AutoTitle bool
	// LastMessageAt is nil for a chat nobody has spoken in yet.
	LastMessageAt *time.Time
	// Preview is the head of the last message, or "" when there is none.
	Preview string
	// TurnRunning is true while a turn of this chat is in flight.
	TurnRunning bool
	// TaskRunning is true while a task this conversation delegated is still running, which
	// outlasts the turn that delegated it.
	TaskRunning bool
	// Channel is the Slack channel's human name for a mirrored thread, without a leading #.
	// Empty for a web chat and for a thread whose name has not been resolved yet.
	Channel string
	// PersonalityID is the personal assistant this chat talks to. Empty means Podium.
	// There is no foreign key: deleting the assistant leaves the chat readable.
	PersonalityID string
	// PersonalityDisplay is that assistant's display name at the moment the chat was
	// opened, so a deleted assistant's threads stay labeled.
	PersonalityDisplay string
	// SourceKey is the conductor session this chat is the record of: chat:<id> for a web
	// chat, the Slack thread or the pull request for a mirrored one. The API reads the
	// conversation's surfaces from it.
	SourceKey string
	// Multiplayer is sticky (0034): set once a chat has had a second Podium person in it, is
	// shared, or mirrors a conversation that lives elsewhere. A web message must then tag
	// Podium to start a turn.
	Multiplayer bool
	// GeneralAccess is who beyond the named grants may open it (0035): AccessRestricted,
	// AccessMembers or AccessLink. GeneralRole is what they get: RoleViewer or RoleEditor.
	GeneralAccess string
	GeneralRole   string
	// LinkToken is the secret in a link share, or "" before link sharing was first turned
	// on. Only the owner is ever shown it; the API strips it for everybody else.
	LinkToken string
	// Sharing is the caller's relationship to the chat, filled by ListChats (phase 5).
	Sharing ChatSharing
}

// ChatAttachment is a file a turn produced, resolved to the artifact it is. The id is
// stored, never the name alone: two turns both producing report.csv are two artifacts.
type ChatAttachment struct {
	ArtifactID  string `json:"artifact_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// ChatMessage is one stored turn of a conversation. Text is content: a human wrote the user
// messages and a task wrote the assistant ones, and nothing here interprets either.
type ChatMessage struct {
	ChatID      string
	Seq         uint64
	Role        string
	Text        string
	Attachments []ChatAttachment
	TS          time.Time
	// Author is who said it, by display name. Empty for a web chat, whose login already
	// says who is typing, and set for every message of a mirrored Slack thread — including
	// the bot's own, which is what lets the UI show the thread as the people in it saw it.
	Author string
	// TaskID is the task whose words these are, and empty for the assistant's own. It is
	// what lets a reader tell the three kinds of progress apart: the assistant thinking on
	// this host, the conductor announcing a delegation, and a delegated task talking.
	TaskID string

	// AuthorLogin is the Podium person who said it. Empty for somebody who is not in Podium,
	// and for the bot. AuthorHandle and AuthorAvatar are what the UI draws beside the name:
	// a GitHub @handle, a Slack display name's avatar.
	AuthorLogin  string
	AuthorHandle string
	AuthorAvatar string
	// Via is the surface the message came in through (ViaWeb, ViaSlack, ViaGitHub, ViaLinear,
	// ViaBot).
	// Empty on a write means ViaWeb for a user message and ViaBot for anything else.
	Via string
	// ExternalID is the message's identity on its own surface, and empty for a web message.
	// ExternalURL links back to it.
	ExternalID  string
	ExternalURL string
	// Addressed is whether the message was put to Podium. Only an addressed message ever
	// starts a turn; the rest are the conversation around it.
	Addressed bool
	// EditedAt and DeletedAt are an edit or a delete made on the surface. A deleted message
	// keeps its text in the store; the API and the transcript do not hand it out.
	EditedAt  *time.Time
	DeletedAt *time.Time
	// Mentions is the Podium logins the message named with the composer's person chip
	// (0034), for phase 5's notifications. Empty for everything but a web message.
	Mentions []string
	// Deliveries is where the bot's message was owed beyond the web chat, and how that went:
	// the chat_outbox rows behind it (0033). Read with the message, never written with it.
	// Empty for every message that is not the bot's answer in a conversation living elsewhere.
	Deliveries []MessageDelivery
}

// MessageDelivery is one surface a bot message was owed to.
type MessageDelivery struct {
	SurfaceKind string
	Status      string
	Attempts    int
	LastError   string
}

// The surfaces a chat message can come in through.
const (
	ViaWeb    = "web"
	ViaSlack  = "slack"
	ViaGitHub = "github"
	ViaLinear = "linear"
	// ViaBot is what the conductor itself wrote: an answer, a progress line, a failure.
	ViaBot = "bot"
)

// via is the surface a message is stored under when the writer did not say.
func (m ChatMessage) via() string {
	if m.Via != "" {
		return m.Via
	}
	if m.Role == "user" {
		return ViaWeb
	}
	return ViaBot
}

// CreateChat opens a Podium chat owned by login. An empty title becomes DefaultChatTitle,
// and AutoTitle stays true so the first query may rename it. A supplied title is kept.
func (s *Store) CreateChat(ctx context.Context, login, title string) (Chat, error) {
	return s.createChat(ctx, login, title, "", "")
}

// CreatePersonalityChat opens a chat bound to one of login's assistants. display is
// snapshotted onto the row so the thread stays labeled after that assistant is deleted.
func (s *Store) CreatePersonalityChat(ctx context.Context, login, title, personalityID, display string) (Chat, error) {
	if strings.TrimSpace(personalityID) == "" {
		return Chat{}, errors.New("create chat: a personality id is required")
	}
	return s.createChat(ctx, login, title, personalityID, display)
}

func (s *Store) createChat(ctx context.Context, login, title, personalityID, display string) (Chat, error) {
	if login == "" {
		return Chat{}, errors.New("create chat: a login is required")
	}
	auto := true
	if strings.TrimSpace(title) == "" {
		title = DefaultChatTitle
	} else {
		auto = false
	}
	id := ids.New("chat")
	key := ChatSourceKey(id)
	var pid *string
	if personalityID != "" {
		pid = &personalityID
	}
	row, err := s.q.CreateChat(ctx, db.CreateChatParams{
		ID:                 id,
		Title:              title,
		Login:              &login,
		CreatedAt:          time.Now().UTC(),
		AutoTitle:          auto,
		SourceKey:          &key,
		Origin:             OriginWeb,
		PersonalityID:      pid,
		PersonalityDisplay: display,
	})
	if err != nil {
		return Chat{}, fmt.Errorf("create chat for %s: %w", login, err)
	}
	return chatFromRow(row), nil
}

// CreateMirrorChat opens the chat that MIRRORS a conversation living somewhere else. It has
// no login, because nobody in Slack has one; sourceKey is the conductor session's own key,
// which is both the link the list joins on and the uniqueness that stops one thread
// becoming two chats.
//
// The title comes from the first thing asked, the same rule a web chat follows, and
// AutoTitle stays true so a later turn may still improve it.
func (s *Store) CreateMirrorChat(ctx context.Context, sourceKey, origin, startedBy, title string) (Chat, error) {
	switch {
	case sourceKey == "":
		return Chat{}, errors.New("create mirror chat: a source key is required")
	case origin == "" || origin == OriginWeb:
		return Chat{}, fmt.Errorf("create mirror chat: %q is not a mirrored origin", origin)
	}
	if strings.TrimSpace(title) == "" {
		title = DefaultChatTitle
	}
	row, err := s.q.CreateChat(ctx, db.CreateChatParams{
		ID:        ids.New("chat"),
		Title:     title,
		Login:     nil,
		CreatedAt: time.Now().UTC(),
		AutoTitle: true,
		SourceKey: &sourceKey,
		StartedBy: startedBy,
		Origin:    origin,
	})
	if err != nil {
		return Chat{}, fmt.Errorf("create mirror chat for %s: %w", sourceKey, err)
	}
	return chatFromRow(row), nil
}

// ChatBySourceKey reads the chat mirroring one conversation, or ErrNotFound.
func (s *Store) ChatBySourceKey(ctx context.Context, sourceKey string) (Chat, error) {
	row, err := s.q.GetChatBySourceKey(ctx, &sourceKey)
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat for %s", ErrNotFound, sourceKey)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("get chat for %s: %w", sourceKey, err)
	}
	return chatFromRow(row), nil
}

// RenameChat sets the title of one of login's chats. shared is the dev token, and it is
// what lets that token rename a mirrored thread. Another login's web chat is not found,
// the same as every other chat read: knowing the id is not access. The row it returns has
// AutoTitle cleared: the name is the owner's now.
func (s *Store) RenameChat(ctx context.Context, id, login, title string, shared bool) (Chat, error) {
	if login == "" {
		return Chat{}, errors.New("rename chat: a login is required")
	}
	if id == "" {
		return Chat{}, errors.New("rename chat: chat id is required")
	}
	cleaned, err := cleanChatTitle(title)
	if err != nil {
		return Chat{}, err
	}
	row, err := s.q.RenameChat(ctx, db.RenameChatParams{
		ID: id, Login: &login, Title: cleaned, IncludeShared: shared,
	})
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, id)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("rename chat %s: %w", id, err)
	}
	return chatFromRow(row), nil
}

// GetChat reads one chat by id, whoever owns it. The caller checks the login: a handler
// that must not leak another login's chat needs the row to compare against.
func (s *Store) GetChat(ctx context.Context, id string) (Chat, error) {
	row, err := s.q.GetChat(ctx, id)
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, id)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("get chat %s: %w", id, err)
	}
	return chatFromRow(row), nil
}

// ListChatsScope narrows a list to one assistant. The zero value lists every chat the
// caller can already see. FilterPersonality with an empty PersonalityID is Podium:
// only rows whose personality_id is null. A set id matches that assistant.
//
// Section is one of the Section constants (phase 5); empty is SectionMine.
type ListChatsScope struct {
	FilterPersonality bool
	PersonalityID     string
	Section           string
}

// ListChats returns chats newest first. all is the dev token and returns every chat,
// including mirrored threads and other logins' web chats. Otherwise only this login's
// web chats are returned, and another login's cannot be paged into: the filter is in
// the query, not in the caller. An optional scope limits the page to one assistant;
// leaving it out keeps the previous "every chat" result.
func (s *Store) ListChats(ctx context.Context, login string, all bool, limit int, cursor string, scope ...ListChatsScope) ([]Chat, string, error) {
	if login == "" && !all {
		return nil, "", errors.New("list chats: a login is required")
	}
	var sc ListChatsScope
	if len(scope) > 0 {
		sc = scope[0]
	}
	if !validSection(sc.Section) {
		return nil, "", fmt.Errorf("list chats: %w: %q", ErrInvalidSection, sc.Section)
	}
	limit = clampLimit(limit)
	rows, err := s.q.ListChats(ctx, db.ListChatsParams{
		IncludeAll:        all,
		Viewer:            login,
		Section:           sc.Section,
		AfterID:           cursor,
		FilterPersonality: sc.FilterPersonality,
		PersonalityID:     sc.PersonalityID,
		PageLimit:         int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("list chats of %s: %w", login, err)
	}
	out := make([]Chat, 0, len(rows))
	for _, r := range rows {
		owner := ""
		if r.Login != nil {
			owner = *r.Login
		}
		c := Chat{
			ID:                 r.ID,
			Title:              r.Title,
			Login:              owner,
			Origin:             r.Origin,
			StartedBy:          r.StartedBy,
			CreatedAt:          r.CreatedAt.UTC(),
			AutoTitle:          r.AutoTitle,
			TurnRunning:        r.TurnRunning,
			TaskRunning:        r.TaskRunning,
			Channel:            r.Channel,
			PersonalityID:      deref(r.PersonalityID),
			PersonalityDisplay: r.PersonalityDisplay,
			SourceKey:          deref(r.SourceKey),
			Multiplayer:        r.Multiplayer,
			GeneralAccess:      r.GeneralAccess,
			GeneralRole:        r.GeneralRole,
			LinkToken:          deref(r.LinkToken),
			RoutineID:          deref(r.RoutineID),
			Sharing: ChatSharing{
				MyRole: r.MyRole, AccessReason: r.AccessReason,
				SharedBy: r.SharedBy, UnreadCount: r.UnreadCount,
			},
		}
		if r.HasMessage {
			at := r.LastMessageAt.UTC()
			c.LastMessageAt = &at
			c.Preview = preview(r.LastText, ChatPreviewChars)
		}
		out = append(out, c)
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// AppendChatMessage stores one message and gives it the chat's next seq. The seq comes from
// the insert itself, so two concurrent appends get two seqs rather than one collision.
func (s *Store) AppendChatMessage(ctx context.Context, msg ChatMessage) (ChatMessage, error) {
	return s.appendChatMessage(ctx, msg, true)
}

// appendChatMessage is AppendChatMessage with Addressed decided by the caller. Append itself
// always stores true: everything that appends without saying otherwise — a web send, an
// answer, a progress line — was put to Podium or written by it. RecordChatMessage is the
// writer that says otherwise, and it passes msg.Addressed through.
func (s *Store) appendChatMessage(ctx context.Context, msg ChatMessage, addressed bool) (ChatMessage, error) {
	raw, err := marshalAttachments(msg.Attachments)
	if err != nil {
		return ChatMessage{}, err
	}
	ts := msg.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	row, err := s.q.AppendChatMessage(ctx, db.AppendChatMessageParams{
		ChatID:       msg.ChatID,
		Role:         msg.Role,
		Text:         msg.Text,
		Attachments:  raw,
		Ts:           ts,
		Author:       msg.Author,
		TaskID:       msg.TaskID,
		AuthorLogin:  nullable(msg.AuthorLogin),
		AuthorHandle: msg.AuthorHandle,
		AuthorAvatar: msg.AuthorAvatar,
		Via:          msg.via(),
		ExternalID:   nullable(msg.ExternalID),
		ExternalUrl:  msg.ExternalURL,
		Addressed:    addressed,
		Mentions:     nonNilStrings(msg.Mentions),
	})
	if noRows(err) {
		return ChatMessage{}, fmt.Errorf("%w: chat %s", ErrNotFound, msg.ChatID)
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("append %s message to chat %s: %w", msg.Role, msg.ChatID, err)
	}
	return chatMessageFromRow(row)
}

// RecordChatMessage stores a message that has an identity on its own surface, once. It
// reports false, and writes nothing, when that message is already in the chat: the same
// Slack event retried, a GitHub webhook redelivered, two conductors hearing one event. The
// caller starts a turn only on true, which is what makes "record it" and "act on it" one
// decision rather than two that can disagree.
//
// A message with no ExternalID cannot be recognised a second time, so it is simply appended.
func (s *Store) RecordChatMessage(ctx context.Context, msg ChatMessage) (ChatMessage, bool, error) {
	if msg.ExternalID == "" {
		row, err := s.appendChatMessage(ctx, msg, msg.Addressed)
		return row, err == nil, err
	}
	raw, err := marshalAttachments(msg.Attachments)
	if err != nil {
		return ChatMessage{}, false, err
	}
	ts := msg.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	row, err := s.q.RecordChatMessage(ctx, db.RecordChatMessageParams{
		ChatID:       msg.ChatID,
		Role:         msg.Role,
		Text:         msg.Text,
		Attachments:  raw,
		Ts:           ts,
		Author:       msg.Author,
		TaskID:       msg.TaskID,
		AuthorLogin:  nullable(msg.AuthorLogin),
		AuthorHandle: msg.AuthorHandle,
		AuthorAvatar: msg.AuthorAvatar,
		Via:          msg.via(),
		ExternalID:   msg.ExternalID,
		ExternalUrl:  msg.ExternalURL,
		Addressed:    msg.Addressed,
		Mentions:     nonNilStrings(msg.Mentions),
	})
	if noRows(err) {
		// Either the message is already here, or the chat is not. The chat is the caller's
		// own (it just found or made it), so this is the duplicate.
		return ChatMessage{}, false, nil
	}
	if err != nil {
		return ChatMessage{}, false, fmt.Errorf("record %s message %s in chat %s: %w",
			msg.Via, msg.ExternalID, msg.ChatID, err)
	}
	out, err := chatMessageFromRow(row)
	return out, err == nil, err
}

// EditChatMessage applies an edit made on a message's own surface. ErrNotFound means the
// message was never recorded — said before the chat existed, or in a thread Podium does not
// hold — and there is nothing to change.
func (s *Store) EditChatMessage(
	ctx context.Context, chatID, via, externalID, text string, at time.Time,
) (ChatMessage, error) {
	row, err := s.q.EditChatMessageByExternal(ctx, db.EditChatMessageByExternalParams{
		ChatID: chatID, Via: via, ExternalID: externalID, Text: text, EditedAt: &at,
	})
	if noRows(err) {
		return ChatMessage{}, fmt.Errorf("%w: message %s/%s in chat %s", ErrNotFound, via, externalID, chatID)
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("edit message %s/%s in chat %s: %w", via, externalID, chatID, err)
	}
	return chatMessageFromRow(row)
}

// DeleteChatMessage marks a message deleted on its own surface. The row and its text stay.
func (s *Store) DeleteChatMessage(
	ctx context.Context, chatID, via, externalID string, at time.Time,
) (ChatMessage, error) {
	row, err := s.q.DeleteChatMessageByExternal(ctx, db.DeleteChatMessageByExternalParams{
		ChatID: chatID, Via: via, ExternalID: externalID, DeletedAt: &at,
	})
	if noRows(err) {
		return ChatMessage{}, fmt.Errorf("%w: message %s/%s in chat %s", ErrNotFound, via, externalID, chatID)
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("delete message %s/%s in chat %s: %w", via, externalID, chatID, err)
	}
	return chatMessageFromRow(row)
}

// GetChatMessage reads one message by its seq.
func (s *Store) GetChatMessage(ctx context.Context, chatID string, seq uint64) (ChatMessage, error) {
	if seq > 1<<62 {
		return ChatMessage{}, fmt.Errorf("chat %s: seq %d overflows bigint", chatID, seq)
	}
	row, err := s.q.GetChatMessage(ctx, db.GetChatMessageParams{ChatID: chatID, Seq: int64(seq)})
	if noRows(err) {
		return ChatMessage{}, fmt.Errorf("%w: message %d of chat %s", ErrNotFound, seq, chatID)
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("get message %d of chat %s: %w", seq, chatID, err)
	}
	msg, err := chatMessageFromRow(row)
	if err != nil {
		return ChatMessage{}, err
	}
	out := []ChatMessage{msg}
	if err := s.attachDeliveries(ctx, chatID, out); err != nil {
		return ChatMessage{}, err
	}
	return out[0], nil
}

// attachDeliveries fills Deliveries on the bot's messages in msgs from one read of the
// chat's outbox. A chat with no bot messages is not read at all.
func (s *Store) attachDeliveries(ctx context.Context, chatID string, msgs []ChatMessage) error {
	bot := false
	for _, m := range msgs {
		if m.Via == ViaBot {
			bot = true
			break
		}
	}
	if !bot {
		return nil
	}
	items, err := s.ListChatOutbox(ctx, chatID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	bySeq := map[uint64][]MessageDelivery{}
	for _, it := range items {
		bySeq[it.MessageSeq] = append(bySeq[it.MessageSeq], MessageDelivery{
			SurfaceKind: it.SurfaceKind, Status: it.Status, Attempts: it.Attempts, LastError: it.LastError,
		})
	}
	for i := range msgs {
		if d, ok := bySeq[msgs[i].Seq]; ok && msgs[i].Via == ViaBot {
			msgs[i].Deliveries = d
		}
	}
	return nil
}

// ListChatMessages returns a chat's messages with seq > fromSeq, in seq order. fromSeq is
// exclusive, which is what makes replay-then-follow exactly once.
func (s *Store) ListChatMessages(ctx context.Context, chatID string, fromSeq uint64) ([]ChatMessage, error) {
	if fromSeq > 1<<62 {
		return nil, fmt.Errorf("chat %s: from_seq %d overflows bigint", chatID, fromSeq)
	}
	rows, err := s.q.ListChatMessages(ctx, db.ListChatMessagesParams{
		ChatID: chatID, FromSeq: int64(fromSeq),
	})
	if err != nil {
		return nil, fmt.Errorf("list messages of chat %s: %w", chatID, err)
	}
	out := make([]ChatMessage, 0, len(rows))
	for _, r := range rows {
		msg, err := chatMessageFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := s.attachDeliveries(ctx, chatID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// AttachToLastAssistantMessage adds one file to the newest assistant message of a chat and
// returns the message as it now stands.
//
// It is an update rather than part of the insert because the turn loop's ordering is fixed:
// a final is posted before its attachments are resolved (step 17), so the row exists before
// the artifact ids do. ErrNotFound means the turn produced a file but said nothing — the
// caller decides what to do about that.
func (s *Store) AttachToLastAssistantMessage(
	ctx context.Context, chatID string, file ChatAttachment,
) (ChatMessage, error) {
	row, err := s.q.LastAssistantMessage(ctx, chatID)
	if noRows(err) {
		return ChatMessage{}, fmt.Errorf("%w: chat %s has no message to attach to", ErrNotFound, chatID)
	}
	if err != nil {
		return ChatMessage{}, fmt.Errorf("read the last message of chat %s: %w", chatID, err)
	}
	msg, err := chatMessageFromRow(row)
	if err != nil {
		return ChatMessage{}, err
	}
	for _, have := range msg.Attachments {
		if have.ArtifactID == file.ArtifactID {
			return msg, nil
		}
	}
	msg.Attachments = append(msg.Attachments, file)
	raw, err := marshalAttachments(msg.Attachments)
	if err != nil {
		return ChatMessage{}, err
	}
	updated, err := s.q.SetChatMessageAttachments(ctx, db.SetChatMessageAttachmentsParams{
		Attachments: raw, ChatID: chatID, Seq: row.Seq,
	})
	if err != nil {
		return ChatMessage{}, fmt.Errorf("attach %s to chat %s: %w", file.Name, chatID, err)
	}
	return chatMessageFromRow(updated)
}

// SetSessionPlaybook changes which playbook a session runs. It is for a CONVERSATION only:
// a chat window whose person picks a playbook per message. A thread keeps what it started
// with, and UpsertSession is what enforces that.
func (s *Store) SetSessionPlaybook(ctx context.Context, id, playbook string) error {
	if err := s.q.SetSessionPlaybook(ctx, db.SetSessionPlaybookParams{ID: id, Playbook: playbook}); err != nil {
		return fmt.Errorf("set playbook %s on session %s: %w", playbook, id, err)
	}
	return nil
}

// SetChatChoice records what a chat is answered on. An all-empty choice is a real value —
// it means the assistant's own model — so this writes whatever it is given rather than
// treating empty as "leave it alone": switching back to the default is a choice too.
func (s *Store) SetChatChoice(ctx context.Context, id string, c ChatChoice) (Chat, error) {
	if id == "" {
		return Chat{}, errors.New("set chat choice: an id is required")
	}
	row, err := s.q.SetChatChoice(ctx, db.SetChatChoiceParams{
		ID:     id,
		Agent:  strings.TrimSpace(c.Agent),
		Model:  strings.TrimSpace(c.Model),
		Effort: strings.TrimSpace(c.Effort),
	})
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, id)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("set choice of chat %s: %w", id, err)
	}
	return chatFromRow(row), nil
}

// SetChatTitle rewrites an auto-named chat. A title supplied at create is left alone and
// the current row is returned.
func (s *Store) SetChatTitle(ctx context.Context, id, title string) (Chat, error) {
	title = strings.TrimSpace(title)
	if id == "" || title == "" {
		return Chat{}, errors.New("set chat title: an id and a title are required")
	}
	row, err := s.q.SetChatTitle(ctx, db.SetChatTitleParams{ID: id, Title: title})
	if noRows(err) {
		return s.GetChat(ctx, id)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("set title of chat %s: %w", id, err)
	}
	return chatFromRow(row), nil
}

func chatFromRow(row db.Chat) Chat {
	login := ""
	if row.Login != nil {
		login = *row.Login
	}
	return Chat{
		ID:                 row.ID,
		Title:              row.Title,
		Login:              login,
		Origin:             row.Origin,
		StartedBy:          row.StartedBy,
		CreatedAt:          row.CreatedAt.UTC(),
		AutoTitle:          row.AutoTitle,
		ChatChoice:         ChatChoice{Agent: row.Agent, Model: row.Model, Effort: row.Effort},
		Channel:            row.Channel,
		PersonalityID:      deref(row.PersonalityID),
		PersonalityDisplay: row.PersonalityDisplay,
		SourceKey:          deref(row.SourceKey),
		Multiplayer:        row.Multiplayer,
		GeneralAccess:      row.GeneralAccess,
		GeneralRole:        row.GeneralRole,
		LinkToken:          deref(row.LinkToken),
		DeletedAt:          row.DeletedAt,
		ChannelPrivacy:     row.ChannelPrivacy,
		SourceURL:          row.SourceUrl,
		HistorySeeded:      row.HistorySeeded,
		StartedFromChat:    deref(row.StartedFromChat),
		RoutineID:          deref(row.RoutineID),
	}
}

// SetChatStartedFrom records that turnID opened chatID from the conversation fromChat ("" when
// that conversation has no chat row).
func (s *Store) SetChatStartedFrom(ctx context.Context, chatID, fromChat, turnID string) (Chat, error) {
	row, err := s.q.SetChatStartedFrom(ctx, db.SetChatStartedFromParams{
		ID: chatID, StartedFromChat: nullable(fromChat), StartedByTurn: &turnID,
	})
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("record where chat %s was started from: %w", chatID, err)
	}
	return chatFromRow(row), nil
}

// CountChatsStartedByTurn is how many chats one turn has opened.
func (s *Store) CountChatsStartedByTurn(ctx context.Context, turnID string) (int, error) {
	n, err := s.q.CountChatsStartedByTurn(ctx, turnID)
	if err != nil {
		return 0, fmt.Errorf("count the chats turn %s opened: %w", turnID, err)
	}
	return int(n), nil
}

// ChatTurnRunning reports whether a turn of this chat is in flight. It is the server-side
// half of "one turn at a time per conversation": the UI disables the composer, and this is
// what makes a second send impossible rather than unlikely.
func (s *Store) ChatTurnRunning(ctx context.Context, chatID string) (bool, error) {
	// The chat's own session key, not chat:<id>: a mirrored conversation's session is its
	// Slack thread or pull request, and a turn running there is running in this chat too.
	running, err := s.q.ChatTurnRunning(ctx, chatID)
	if err != nil {
		return false, fmt.Errorf("read whether a turn of chat %s is running: %w", chatID, err)
	}
	return running, nil
}

// ChatTaskRunning reports whether a task this chat delegated is still running. It fills
// Chat.TaskRunning for a chat read on its own (GetChat), which the list query computes inline.
func (s *Store) ChatTaskRunning(ctx context.Context, chatID string) (bool, error) {
	running, err := s.q.ChatTaskRunning(ctx, chatID)
	if err != nil {
		return false, fmt.Errorf("read whether a task of chat %s is running: %w", chatID, err)
	}
	return running, nil
}

// RunningChatTask is the Podium task currently answering this chat. Empty when nothing is
// in flight, when the chat has never had a turn, and when the turn row exists but
// CreateTask has not answered yet — there is then nothing to cancel.
func (s *Store) RunningChatTask(ctx context.Context, chatID string) (string, error) {
	turn, ok, err := s.RunningChatTurn(ctx, chatID)
	if err != nil || !ok {
		return "", err
	}
	return turn.TaskID, nil
}

// RunningChatTurn is the turn currently answering this chat's session, whichever surface
// started it. ok is false when nothing is running.
func (s *Store) RunningChatTurn(ctx context.Context, chatID string) (Turn, bool, error) {
	key := ChatSourceKey(chatID)
	if chat, err := s.GetChat(ctx, chatID); err == nil && chat.SourceKey != "" {
		key = chat.SourceKey
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return Turn{}, false, err
	}
	sess, err := s.GetSessionByKey(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, false, nil
	}
	if err != nil {
		return Turn{}, false, err
	}
	turns, err := s.ListTurns(ctx, sess.ID, 1)
	if err != nil {
		return Turn{}, false, err
	}
	if len(turns) == 0 || turns[0].Status != TurnRunning {
		return Turn{}, false, nil
	}
	return turns[0], true, nil
}

// DeleteChat deletes one login's chat for everyone. A web chat is removed with every message
// in it (ON DELETE CASCADE). A mirrored chat is tombstoned instead (0036): Podium's record of a
// Slack thread or a pull request would otherwise be opened afresh by the conversation's next
// message, and a delete that comes back is not a delete. ErrNotFound means it was not there or
// not theirs: the two are the same answer so the existence of another login's chat is not
// leaked. shared is the dev token deleting a mirrored chat nobody owns. Sessions and turns are
// left alone — they are the audit of the work, not the transcript.
func (s *Store) DeleteChat(ctx context.Context, id, login string, shared bool) error {
	if login == "" {
		return errors.New("delete chat: a login is required")
	}
	if id == "" {
		return errors.New("delete chat: an id is required")
	}
	n, err := s.q.DeleteChat(ctx, db.DeleteChatParams{ID: id, Login: &login, IncludeShared: shared})
	if err != nil {
		return fmt.Errorf("delete chat %s: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC()
	n, err = s.q.TombstoneChat(ctx, db.TombstoneChatParams{ID: id, Login: &login, IncludeShared: shared, DeletedAt: &now})
	if err != nil {
		return fmt.Errorf("delete mirrored chat %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: chat %s", ErrNotFound, id)
	}
	return nil
}

// Where a chat's pull-request link came from. They are exactly the values the
// chat_pull_requests.source check constraint allows.
const (
	// PullRequestFromTurn is a link a turn's own answer named.
	PullRequestFromTurn = "turn"
	// PullRequestFromHuman is a link a person attached by hand.
	PullRequestFromHuman = "human"
)

// ChatPullRequest is one pull request a chat's work produced. Owner, Repo and Number are
// what the URL itself said — nothing here was learnt from GitHub, and the conductor holds
// no credential that could ask it.
type ChatPullRequest struct {
	ChatID string
	// URL is canonical: https://github.com/<owner>/<repo>/pull/<number>. It is the
	// identity of the link, which is what makes /pull/12/files and /pull/12 one row.
	URL    string
	Owner  string
	Repo   string
	Number int
	// Source is PullRequestFromTurn or PullRequestFromHuman.
	Source    string
	CreatedAt time.Time
}

// LinkChatPullRequest records a pull request a turn's answer named, and reports whether
// this call is what linked it. False means the chat already had it — the same turn saying
// it twice, or a human having detached it, and neither is an error.
func (s *Store) LinkChatPullRequest(ctx context.Context, pr ChatPullRequest) (bool, error) {
	n, err := s.q.LinkChatPullRequest(ctx, db.LinkChatPullRequestParams{
		ChatID:    pr.ChatID,
		Url:       pr.URL,
		Owner:     pr.Owner,
		Repo:      pr.Repo,
		Number:    int32(pr.Number),
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("link %s to chat %s: %w", pr.URL, pr.ChatID, err)
	}
	return n > 0, nil
}

// AttachChatPullRequest is a person linking one by hand. It revives a link they detached
// earlier and takes it over from the turn that found it: re-attaching what you removed is
// meant, and the row is yours afterwards.
func (s *Store) AttachChatPullRequest(ctx context.Context, pr ChatPullRequest) (ChatPullRequest, error) {
	row, err := s.q.AttachChatPullRequest(ctx, db.AttachChatPullRequestParams{
		ChatID:    pr.ChatID,
		Url:       pr.URL,
		Owner:     pr.Owner,
		Repo:      pr.Repo,
		Number:    int32(pr.Number),
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return ChatPullRequest{}, fmt.Errorf("attach %s to chat %s: %w", pr.URL, pr.ChatID, err)
	}
	return pullRequestFromRow(row), nil
}

// DetachChatPullRequest takes one link off a chat. ErrNotFound means it was not linked, or
// was detached already; the two are the same answer.
func (s *Store) DetachChatPullRequest(ctx context.Context, chatID, url string) error {
	now := time.Now().UTC()
	n, err := s.q.DetachChatPullRequest(ctx, db.DetachChatPullRequestParams{
		DetachedAt: &now, ChatID: chatID, Url: url,
	})
	if err != nil {
		return fmt.Errorf("detach %s from chat %s: %w", url, chatID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: chat %s has no link to %s", ErrNotFound, chatID, url)
	}
	return nil
}

// ListChatPullRequests returns a chat's links, oldest first. Detached ones are not there.
func (s *Store) ListChatPullRequests(ctx context.Context, chatID string) ([]ChatPullRequest, error) {
	rows, err := s.q.ListChatPullRequests(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list the pull requests of chat %s: %w", chatID, err)
	}
	out := make([]ChatPullRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, pullRequestFromRow(r))
	}
	return out, nil
}

func pullRequestFromRow(r db.ChatPullRequest) ChatPullRequest {
	return ChatPullRequest{
		ChatID:    r.ChatID,
		URL:       r.Url,
		Owner:     r.Owner,
		Repo:      r.Repo,
		Number:    int(r.Number),
		Source:    r.Source,
		CreatedAt: r.CreatedAt.UTC(),
	}
}

// marshalAttachments always writes a JSON array: the column is `not null default '[]'` and
// a nil slice must not become the literal null.
func marshalAttachments(files []ChatAttachment) ([]byte, error) {
	if files == nil {
		files = []ChatAttachment{}
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("encode chat attachments: %w", err)
	}
	return raw, nil
}

func chatMessageFromRow(r db.ChatMessage) (ChatMessage, error) {
	msg := ChatMessage{
		ChatID: r.ChatID,
		Seq:    uint64(r.Seq),
		Role:   r.Role,
		Text:   r.Text,
		TS:     r.Ts.UTC(),
		Author: r.Author,
		TaskID: r.TaskID,

		AuthorHandle: r.AuthorHandle,
		AuthorAvatar: r.AuthorAvatar,
		Via:          r.Via,
		ExternalURL:  r.ExternalUrl,
		Addressed:    r.Addressed,
		EditedAt:     utcPtr(r.EditedAt),
		DeletedAt:    utcPtr(r.DeletedAt),
		Mentions:     r.Mentions,
	}
	if r.AuthorLogin != nil {
		msg.AuthorLogin = *r.AuthorLogin
	}
	if r.ExternalID != nil {
		msg.ExternalID = *r.ExternalID
	}
	if len(r.Attachments) > 0 {
		if err := json.Unmarshal(r.Attachments, &msg.Attachments); err != nil {
			return ChatMessage{}, fmt.Errorf("decode attachments of %s/%d: %w", r.ChatID, r.Seq, err)
		}
	}
	return msg, nil
}

// cleanChatTitle is what a rename stores: trimmed, internal whitespace collapsed, no
// control characters, and no longer than MaxChatTitleRunes. An empty title after that
// is refused rather than becoming DefaultChatTitle — renaming to nothing is a mistake,
// not a request for a new chat.
func cleanChatTitle(title string) (string, error) {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "", fmt.Errorf("%w: a title is required", ErrInvalidChatTitle)
	}
	if strings.ContainsFunc(title, unicode.IsControl) {
		return "", fmt.Errorf("%w: a title cannot hold control characters", ErrInvalidChatTitle)
	}
	n := utf8.RuneCountInString(title)
	if n > MaxChatTitleRunes {
		return "", fmt.Errorf("%w: %d runes is more than the %d-rune limit", ErrInvalidChatTitle, n, MaxChatTitleRunes)
	}
	return title, nil
}

// preview is the head of a message, cut on a rune boundary so a multi-byte character is
// never split in half.
func preview(text string, limit int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimRight(string(runes[:limit]), " \t\n") + "…"
}

// nullable is a text column's value for an empty Go string: NULL, not ”.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nonNilStrings is s, or an empty slice: a nil slice is NULL to Postgres, and an array column
// that is NOT NULL refuses it.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
