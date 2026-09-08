//go:build integration

package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedTables is every table 0001_init.sql creates. linear_cursor is written by step
// 20's Linear source and the two chat tables by step 21's chat source; no step has needed a
// second migration.
var expectedTables = []string{
	"schema_migrations", "sessions", "turns", "relayed", "settings",
	"linear_cursor", "chats", "chat_messages",
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var reg *string
	err := s.pool.QueryRow(context.Background(), "select to_regclass('public.'||$1)::text", name).Scan(&reg)
	require.NoError(t, err)
	return reg != nil
}

func TestMigrateFromEmptyAndAgain(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, newDatabase(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	for _, name := range expectedTables {
		require.False(t, tableExists(t, s, name), "%s must not exist before Migrate", name)
	}
	require.NoError(t, s.Migrate(ctx))
	for _, name := range expectedTables {
		require.True(t, tableExists(t, s, name), "%s must exist after Migrate", name)
	}

	// The second run is a no-op, and the recorded version list does not grow.
	var before int
	require.NoError(t, s.pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&before))
	require.NoError(t, s.Migrate(ctx))
	var after int
	require.NoError(t, s.pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&after))
	assert.Equal(t, before, after, "a second Migrate must apply nothing")
}

// Two conductors starting at once is the normal case during a rolling restart, and the
// advisory lock is what makes it safe. Both must succeed and the schema must be applied once.
func TestConcurrentMigrationsApplyExactlyOnce(t *testing.T) {
	ctx := context.Background()
	url := newDatabase(t)

	const racers = 4
	stores := make([]*Store, racers)
	for i := range stores {
		s, err := New(ctx, url)
		require.NoError(t, err)
		t.Cleanup(s.Close)
		stores[i] = s
	}

	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i, s := range stores {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			errs[i] = s.Migrate(ctx)
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "racer %d", i)
	}

	names, err := migrationNames()
	require.NoError(t, err)
	var versions int
	require.NoError(t, stores[0].pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&versions))
	assert.Equal(t, len(names), versions, "every migration must be recorded exactly once")
}

func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	want := Session{SourceKind: "slack", SourceKey: "slack:C1:1.1", Profile: "podium", Playbook: "general"}
	sess, err := s.UpsertSession(ctx, want)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sess.ID, "sess_"), "ids are prefixed ULIDs: %s", sess.ID)
	assert.Equal(t, "general", sess.Playbook)
	assert.Nil(t, sess.LastTurnAt, "a session with no turn has never had one")

	// One session, one playbook: a second event asking for another playbook gets the original.
	again, err := s.UpsertSession(ctx, Session{
		SourceKind: "slack", SourceKey: "slack:C1:1.1", Profile: "podium", Playbook: "coder",
	})
	require.NoError(t, err)
	assert.Equal(t, sess.ID, again.ID, "the same source key is the same session")
	assert.Equal(t, "general", again.Playbook, "the playbook of an existing session is never changed")

	byID, err := s.GetSession(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, byID.ID)
	byKey, err := s.GetSessionByKey(ctx, "slack:C1:1.1")
	require.NoError(t, err)
	assert.Equal(t, sess.ID, byKey.ID)

	_, err = s.GetSession(ctx, "sess_nope")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetSessionByKey(ctx, "slack:C9:9.9")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestListSessionsIsNewestFirstAndPages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	var ids []string
	for i := range 5 {
		sess, err := s.UpsertSession(ctx, Session{
			SourceKind: "dev", SourceKey: "dev:C1:" + string(rune('a'+i)), Profile: "podium", Playbook: "general",
		})
		require.NoError(t, err)
		ids = append(ids, sess.ID)
	}

	page, next, err := s.ListSessions(ctx, 2, "")
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, ids[4], page[0].ID, "newest first")
	assert.Equal(t, ids[3], page[1].ID)
	require.Equal(t, ids[3], next)

	page, next, err = s.ListSessions(ctx, 2, next)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, ids[2], page[0].ID)
	assert.Equal(t, ids[1], page[1].ID)

	page, next, err = s.ListSessions(ctx, 2, next)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Empty(t, next, "a short page is the last page")
}

func TestTurnRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess, err := s.UpsertSession(ctx, Session{
		SourceKind: "dev", SourceKey: "dev:C1:1.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)

	turn, err := s.CreateTurn(ctx, sess.ID, "C1/1.1", Backend{})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(turn.ID, "turn_"), "ids are prefixed ULIDs: %s", turn.ID)
	assert.Equal(t, TurnRunning, turn.Status)
	assert.Empty(t, turn.TaskID, "the task does not exist yet")

	// Creating a turn is what marks the session as active.
	sess, err = s.GetSession(ctx, sess.ID)
	require.NoError(t, err)
	require.NotNil(t, sess.LastTurnAt)

	require.NoError(t, s.SetTurnTask(ctx, turn.ID, "task_01"))
	running, err := s.ListRunningTurns(ctx)
	require.NoError(t, err)
	require.Len(t, running, 1)
	assert.Equal(t, "task_01", running[0].TaskID)

	turns, cost := 7, 0.1234
	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, &turns, &cost, "the answer"))

	got, err := s.GetTurn(ctx, turn.ID)
	require.NoError(t, err)
	assert.Equal(t, TurnSucceeded, got.Status)
	require.NotNil(t, got.FinishedAt)
	require.NotNil(t, got.NumTurns)
	assert.Equal(t, 7, *got.NumTurns)
	require.NotNil(t, got.CostUSD)
	assert.InDelta(t, 0.1234, *got.CostUSD, 1e-9, "numeric(12,6) keeps six decimal places")
	assert.Equal(t, "the answer", got.FinalText)

	running, err = s.ListRunningTurns(ctx)
	require.NoError(t, err)
	assert.Empty(t, running, "a finished turn is not in flight")

	listed, err := s.ListTurns(ctx, sess.ID, 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, turn.ID, listed[0].ID)
}

// The check constraint is the schema's half of the "these are the only statuses" contract.
func TestTheSchemaRefusesAnInventedTurnStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess, err := s.UpsertSession(ctx, Session{
		SourceKind: "dev", SourceKey: "dev:C1:1.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := s.CreateTurn(ctx, sess.ID, "C1/1.1", Backend{})
	require.NoError(t, err)

	err = s.FinishTurn(ctx, turn.ID, "nearly-worked", nil, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "turns_status_check")
}

// MarkRelayed is the whole of the relay's exactly-once guarantee, so it gets its own test:
// the first claim wins, every later one loses, and losing is not an error.
func TestMarkRelayedIsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	first, err := s.MarkRelayed(ctx, "task_01", 3)
	require.NoError(t, err)
	assert.True(t, first, "the first claim of a seq wins")

	again, err := s.MarkRelayed(ctx, "task_01", 3)
	require.NoError(t, err)
	assert.False(t, again, "a replayed event claims nothing and is not an error")

	other, err := s.MarkRelayed(ctx, "task_01", 4)
	require.NoError(t, err)
	assert.True(t, other)

	otherTask, err := s.MarkRelayed(ctx, "task_02", 3)
	require.NoError(t, err)
	assert.True(t, otherTask, "seq is only unique within a task")

	high, err := s.MaxRelayedSeq(ctx, "task_01")
	require.NoError(t, err)
	assert.Equal(t, uint64(4), high)

	n, err := s.CountRelayed(ctx, "task_01")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// A task nothing was relayed for resumes from the beginning.
	high, err = s.MaxRelayedSeq(ctx, "task_unknown")
	require.NoError(t, err)
	assert.Zero(t, high)
}

// Concurrent claims of one seq must produce exactly one winner: two conductors following
// the same task would otherwise both post the answer.
func TestConcurrentMarkRelayedHasOneWinner(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	const racers = 8
	var wg sync.WaitGroup
	wins := make([]bool, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			won, err := s.MarkRelayed(ctx, "task_01", 1)
			require.NoError(t, err)
			wins[i] = won
		}(i)
	}
	wg.Wait()

	won := 0
	for _, w := range wins {
		if w {
			won++
		}
	}
	assert.Equal(t, 1, won, "exactly one claimer may post")
}

// GetSetting/PutSetting are step 18's, written now because they are twenty lines.
func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	type providerKey struct {
		Provider string `json:"provider"`
		SetBy    string `json:"set_by"`
	}
	var got providerKey
	require.ErrorIs(t, s.GetSetting(ctx, "provider_key", &got), ErrNotFound)

	require.NoError(t, s.PutSetting(ctx, "provider_key", providerKey{Provider: "anthropic", SetBy: "alvaro"}))
	require.NoError(t, s.GetSetting(ctx, "provider_key", &got))
	assert.Equal(t, providerKey{Provider: "anthropic", SetBy: "alvaro"}, got)

	require.NoError(t, s.PutSetting(ctx, "provider_key", providerKey{Provider: "anthropic", SetBy: "sam"}))
	require.NoError(t, s.GetSetting(ctx, "provider_key", &got))
	assert.Equal(t, "sam", got.SetBy, "a second put replaces the value")
}

// TestTheLinearCursorRoundTrips. The watermark is the whole of the Linear source's state:
// the source itself never touches this store, so this pair of methods is the only place it
// is persisted, and losing it means replaying 24 hours of tickets.
func TestTheLinearCursorRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, err := s.GetLinearCursor(ctx, LinearCursorKey)
	require.ErrorIs(t, err, ErrNotFound, "a database that has never polled has no watermark")

	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	require.NoError(t, s.PutLinearCursor(ctx, LinearCursorKey, first))
	got, err := s.GetLinearCursor(ctx, LinearCursorKey)
	require.NoError(t, err)
	assert.True(t, got.Equal(first), "want %s, got %s", first, got)
	assert.Equal(t, time.UTC, got.Location(), "every timestamp out of this store is UTC")

	second := first.Add(30 * time.Minute)
	require.NoError(t, s.PutLinearCursor(ctx, LinearCursorKey, second))
	got, err = s.GetLinearCursor(ctx, LinearCursorKey)
	require.NoError(t, err)
	assert.True(t, got.Equal(second), "a second put advances the watermark")
}

// Steps 20 and 21 own these; step 17 creates them and leaves them empty so neither step
// has to ship a migration for a shape that is already decided. linear_cursor is now
// written by the Linear source, so only the chat tables are still untouched.
func TestTheLaterStepsTablesExistAndAreEmpty(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, table := range []string{"chats", "chat_messages"} {
		var n int
		require.NoError(t, s.pool.QueryRow(ctx, "select count(*) from "+table).Scan(&n))
		assert.Zero(t, n, "%s must be created empty", table)
	}
}
