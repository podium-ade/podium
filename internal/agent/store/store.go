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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
	"github.com/alvaroibarguen/podium/internal/ids"
)

// ErrNotFound is what the readers return for a row that is not there.
var ErrNotFound = errors.New("agent store: not found")

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
	Skill      string
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

// UpsertSession returns the session for want.SourceKey, creating it if it is new. The skill
// of an existing session is never changed: one session, one skill, fixed at creation. The
// returned row is authoritative, so a caller that wanted a different skill can see it did
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
		Skill:      want.Skill,
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

func sessionFromRow(r db.Session) Session {
	return Session{
		ID:         r.ID,
		SourceKind: r.SourceKind,
		SourceKey:  r.SourceKey,
		Profile:    r.Profile,
		Skill:      r.Skill,
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
