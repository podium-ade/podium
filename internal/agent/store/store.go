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

	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
	"github.com/alvaroibarguen/podium/internal/ids"
)

// ErrNotFound is what the readers return for a row that is not there.
var ErrNotFound = errors.New("agent store: not found")

// ErrInvalidChatTitle is a title a rename would not store: empty, too long, or holding
// a control character. The handler maps it to InvalidArgument.
var ErrInvalidChatTitle = errors.New("invalid chat title")

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
func (s *Store) ListSessions(ctx context.Context, limit int, cursor string) ([]Session, string, error) {
	limit = clampLimit(limit)
	rows, err := s.q.ListSessions(ctx, db.ListSessionsParams{AfterID: cursor, PageLimit: int32(limit)})
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionFromRow(r))
	}
	next := ""
	if len(out) == limit {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// CreateTurn records a turn as running. The task does not exist yet: SetTurnTask fills it
// in once CreateTask has answered.
func (s *Store) CreateTurn(ctx context.Context, sessionID, triggerRef string) (Turn, error) {
	now := time.Now().UTC()
	row, err := s.q.CreateTurn(ctx, db.CreateTurnParams{
		ID:         ids.New("turn"),
		SessionID:  sessionID,
		TriggerRef: triggerRef,
		Status:     TurnRunning,
		StartedAt:  now,
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
		ID:         r.ID,
		SourceKind: r.SourceKind,
		SourceKey:  r.SourceKey,
		Profile:    r.Profile,
		Playbook:   r.Playbook,
		CreatedAt:  r.CreatedAt.UTC(),
		LastTurnAt: utcPtr(r.LastTurnAt),
	}
}

func turnFromRow(r db.Turn) Turn {
	t := Turn{
		ID:         r.ID,
		SessionID:  r.SessionID,
		TaskID:     deref(r.TaskID),
		TriggerRef: r.TriggerRef,
		Status:     r.Status,
		StartedAt:  r.StartedAt.UTC(),
		FinishedAt: utcPtr(r.FinishedAt),
		CostUSD:    r.CostUsd,
		FinalText:  deref(r.FinalText),
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
)

// ChatSourceKeyPrefix is what a chat's session key starts with. The chat source builds it
// and the conductor treats it as opaque; the store needs it only to answer "is a turn of
// this chat running" in the same query that lists the chats.
const ChatSourceKeyPrefix = "chat:"

// ChatSourceKey is the session identity of one chat.
func ChatSourceKey(chatID string) string { return ChatSourceKeyPrefix + chatID }

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
	ID        string
	Title     string
	Login     string
	CreatedAt time.Time
	// Playbook is the playbook this chat runs. Empty until the first message; then it is
	// fixed — one chat, one playbook.
	Playbook string
	// AutoTitle is true when Podium may rewrite Title from the first query. False when
	// the caller supplied a title at create.
	AutoTitle bool
	// LastMessageAt is nil for a chat nobody has spoken in yet.
	LastMessageAt *time.Time
	// Preview is the head of the last message, or "" when there is none.
	Preview string
	// TurnRunning is true while a turn of this chat is in flight.
	TurnRunning bool
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
}

// CreateChat opens a chat owned by login. An empty title becomes DefaultChatTitle, and
// AutoTitle stays true so the first query may rename it. A supplied title is kept.
func (s *Store) CreateChat(ctx context.Context, login, title string) (Chat, error) {
	if login == "" {
		return Chat{}, errors.New("create chat: a login is required")
	}
	auto := true
	if strings.TrimSpace(title) == "" {
		title = DefaultChatTitle
	} else {
		auto = false
	}
	row, err := s.q.CreateChat(ctx, db.CreateChatParams{
		ID:        ids.New("chat"),
		Title:     title,
		Login:     login,
		CreatedAt: time.Now().UTC(),
		AutoTitle: auto,
	})
	if err != nil {
		return Chat{}, fmt.Errorf("create chat for %s: %w", login, err)
	}
	return chatFromRow(row), nil
}

// RenameChat sets the title of one of login's chats. Another login's chat is not
// found, the same as every other chat read: knowing the id is not access. The row it
// returns has AutoTitle cleared: the name is the owner's now.
func (s *Store) RenameChat(ctx context.Context, id, login, title string) (Chat, error) {
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
	row, err := s.q.RenameChat(ctx, db.RenameChatParams{ID: id, Login: login, Title: cleaned})
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

// ListChats returns one login's own chats, newest first. Another login's are not returned
// and cannot be paged into: the filter is in the query, not in the caller.
func (s *Store) ListChats(ctx context.Context, login string, limit int, cursor string) ([]Chat, string, error) {
	if login == "" {
		return nil, "", errors.New("list chats: a login is required")
	}
	limit = clampLimit(limit)
	rows, err := s.q.ListChats(ctx, db.ListChatsParams{
		SourceKeyPrefix: ChatSourceKeyPrefix,
		Login:           login,
		AfterID:         cursor,
		PageLimit:       int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("list chats of %s: %w", login, err)
	}
	out := make([]Chat, 0, len(rows))
	for _, r := range rows {
		c := Chat{
			ID:          r.ID,
			Title:       r.Title,
			Login:       r.Login,
			CreatedAt:   r.CreatedAt.UTC(),
			Playbook:    r.Playbook,
			AutoTitle:   r.AutoTitle,
			TurnRunning: r.TurnRunning,
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
	raw, err := marshalAttachments(msg.Attachments)
	if err != nil {
		return ChatMessage{}, err
	}
	ts := msg.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	row, err := s.q.AppendChatMessage(ctx, db.AppendChatMessageParams{
		ChatID:      msg.ChatID,
		Role:        msg.Role,
		Text:        msg.Text,
		Attachments: raw,
		Ts:          ts,
	})
	if err != nil {
		return ChatMessage{}, fmt.Errorf("append %s message to chat %s: %w", msg.Role, msg.ChatID, err)
	}
	return chatMessageFromRow(row)
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

// SetChatPlaybook records the playbook a chat started with. An already-set playbook is
// left alone and the current row is returned: one chat, one playbook.
func (s *Store) SetChatPlaybook(ctx context.Context, id, playbook string) (Chat, error) {
	playbook = strings.TrimSpace(playbook)
	if id == "" || playbook == "" {
		return Chat{}, errors.New("set chat playbook: an id and a playbook are required")
	}
	row, err := s.q.SetChatPlaybook(ctx, db.SetChatPlaybookParams{ID: id, Playbook: playbook})
	if noRows(err) {
		return s.GetChat(ctx, id)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("set playbook of chat %s: %w", id, err)
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
	return Chat{
		ID:        row.ID,
		Title:     row.Title,
		Login:     row.Login,
		CreatedAt: row.CreatedAt.UTC(),
		Playbook:  row.Playbook,
		AutoTitle: row.AutoTitle,
	}
}

// ChatTurnRunning reports whether a turn of this chat is in flight. It is the server-side
// half of "one turn at a time per conversation": the UI disables the composer, and this is
// what makes a second send impossible rather than unlikely.
func (s *Store) ChatTurnRunning(ctx context.Context, chatID string) (bool, error) {
	running, err := s.q.ChatTurnRunning(ctx, ChatSourceKey(chatID))
	if err != nil {
		return false, fmt.Errorf("read whether a turn of chat %s is running: %w", chatID, err)
	}
	return running, nil
}

// RunningChatTask is the Podium task currently answering this chat. Empty when nothing is
// in flight, when the chat has never had a turn, and when the turn row exists but
// CreateTask has not answered yet — there is then nothing to cancel.
func (s *Store) RunningChatTask(ctx context.Context, chatID string) (string, error) {
	sess, err := s.GetSessionByKey(ctx, ChatSourceKey(chatID))
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	turns, err := s.ListTurns(ctx, sess.ID, 1)
	if err != nil {
		return "", err
	}
	if len(turns) == 0 || turns[0].Status != TurnRunning {
		return "", nil
	}
	return turns[0].TaskID, nil
}

// DeleteChat removes one login's chat and every message in it (ON DELETE CASCADE).
// ErrNotFound means it was not there or not theirs: the two are the same answer so the
// existence of another login's chat is not leaked. Sessions and turns are left alone —
// they are the audit of the work, not the transcript.
func (s *Store) DeleteChat(ctx context.Context, id, login string) error {
	if login == "" {
		return errors.New("delete chat: a login is required")
	}
	if id == "" {
		return errors.New("delete chat: an id is required")
	}
	n, err := s.q.DeleteChat(ctx, db.DeleteChatParams{ID: id, Login: login})
	if err != nil {
		return fmt.Errorf("delete chat %s: %w", id, err)
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
