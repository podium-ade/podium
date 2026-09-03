//go:build integration

package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func eventBatch(from, to uint64) []Event {
	var out []Event
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	for seq := from; seq <= to; seq++ {
		out = append(out, Event{
			Seq:     seq,
			Kind:    "log",
			TS:      base.Add(time.Duration(seq) * time.Second),
			Payload: []byte(`{"n":` + strconv.FormatUint(seq, 10) + `}`),
		})
	}
	return out
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestAppendEventsIsIdempotentAndReturnsHighWaterMark(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	// Nothing stored yet.
	high, err := s.AppendEvents(ctx, task.ID, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(0), high)

	high, err = s.AppendEvents(ctx, task.ID, eventBatch(1, 3))
	require.NoError(t, err)
	require.Equal(t, uint64(3), high)
	require.Equal(t, 3, countRows(t, s, "select count(*) from task_events where task_id = $1", task.ID))

	// A node replaying its buffer re-sends the same batch: no duplicates, same mark.
	high, err = s.AppendEvents(ctx, task.ID, eventBatch(1, 3))
	require.NoError(t, err)
	require.Equal(t, uint64(3), high)
	require.Equal(t, 3, countRows(t, s, "select count(*) from task_events where task_id = $1", task.ID))

	// An overlapping batch inserts only what is new.
	high, err = s.AppendEvents(ctx, task.ID, eventBatch(2, 5))
	require.NoError(t, err)
	require.Equal(t, uint64(5), high)
	require.Equal(t, 5, countRows(t, s, "select count(*) from task_events where task_id = $1", task.ID))

	// The first write of a seq wins; a replay never overwrites it.
	replay := []Event{{Seq: 1, Kind: "tampered", TS: time.Now().UTC(), Payload: []byte(`{"n":999}`)}}
	_, err = s.AppendEvents(ctx, task.ID, replay)
	require.NoError(t, err)
	got, err := s.ListEvents(ctx, task.ID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, "log", got[0].Kind)
	require.JSONEq(t, `{"n":1}`, string(got[0].Payload))
}

func TestListEventsPagesFromSeq(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	_, err := s.AppendEvents(ctx, task.ID, eventBatch(1, 10))
	require.NoError(t, err)

	got, err := s.ListEvents(ctx, task.ID, 1, 4)
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.Equal(t, uint64(1), got[0].Seq)
	require.Equal(t, uint64(4), got[3].Seq)
	require.Equal(t, time.UTC, got[0].TS.Location())

	got, err = s.ListEvents(ctx, task.ID, 8, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, uint64(8), got[0].Seq)

	got, err = s.ListEvents(ctx, task.ID, 99, 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestEventsCascadeWithTheTask(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)
	_, err := s.AppendEvents(ctx, task.ID, eventBatch(1, 2))
	require.NoError(t, err)

	_, err = s.pool.Exec(ctx, "delete from tasks where id = $1", task.ID)
	require.NoError(t, err)
	require.Equal(t, 0, countRows(t, s, "select count(*) from task_events where task_id = $1", task.ID))
}

func logBatch(from, to uint64, at time.Time) []LogChunk {
	var out []LogChunk
	for seq := from; seq <= to; seq++ {
		out = append(out, LogChunk{
			Seq:    seq,
			Stream: "stdout",
			TS:     at.Add(time.Duration(seq) * time.Millisecond),
			Bytes:  []byte("tick " + strconv.FormatUint(seq, 10) + "\n"),
		})
	}
	return out
}

func TestAppendAndListLogChunks(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)
	now := time.Now().UTC()

	high, err := s.AppendLogChunks(ctx, task.ID, logBatch(1, 5, now))
	require.NoError(t, err)
	require.Equal(t, uint64(5), high)

	// Idempotent on (task_id, seq), like events.
	high, err = s.AppendLogChunks(ctx, task.ID, logBatch(1, 5, now))
	require.NoError(t, err)
	require.Equal(t, uint64(5), high)
	require.Equal(t, 5, countRows(t, s, "select count(*) from task_log_chunks where task_id = $1", task.ID))

	high, err = s.AppendLogChunks(ctx, task.ID, []LogChunk{{
		Seq: 6, Stream: "stderr", Sidecar: "db", TS: now, Bytes: []byte("boom\n"),
	}})
	require.NoError(t, err)
	require.Equal(t, uint64(6), high)

	got, err := s.ListLogChunks(ctx, task.ID, 5, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "stdout", got[0].Stream)
	require.Empty(t, got[0].Sidecar)
	require.Equal(t, []byte("tick 5\n"), got[0].Bytes)
	require.Equal(t, "stderr", got[1].Stream)
	require.Equal(t, "db", got[1].Sidecar)
}

// TestPruneLogChunksOnlyTouchesRolledUpTasks: the prune is scoped by task, not by chunk
// age. A task whose logs are not in the object store keeps every row no matter how old
// they are — deleting them would lose the log and, worse, move the resume offsets a
// reconnecting node is handed (step 12) backwards.
func TestPruneLogChunksOnlyTouchesRolledUpTasks(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	rolled := mustCreateTask(t, s, 1)
	live := mustCreateTask(t, s, 2)

	old := time.Now().UTC().Add(-48 * time.Hour)
	for _, task := range []Task{rolled, live} {
		_, err := s.AppendLogChunks(ctx, task.ID, logBatch(1, 3, old))
		require.NoError(t, err)
		_, err = s.AppendLogChunks(ctx, task.ID, logBatch(4, 6, time.Now().UTC()))
		require.NoError(t, err)
	}

	// Nothing has been rolled up, so nothing may go.
	n, err := s.PruneLogChunks(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 6, countRows(t, s, "select count(*) from task_log_chunks where task_id = $1", live.ID))

	require.NoError(t, s.MarkLogsRolledUp(ctx, rolled.ID, LogRollUp{
		HighSeq: 6, StdoutOffset: 4096, StderrOffset: 512,
	}))

	// The grace period has not passed yet.
	n, err = s.PruneLogChunks(ctx, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, n)

	n, err = s.PruneLogChunks(ctx, time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(6), n)
	require.Zero(t, countRows(t, s, "select count(*) from task_log_chunks where task_id = $1", rolled.ID))
	require.Equal(t, 6, countRows(t, s, "select count(*) from task_log_chunks where task_id = $1", live.ID),
		"a task that has not been rolled up keeps its rows")
}

// TestRollUpMarksKeepTheReconciliationAnswersMonotonic is the invariant the log roll-up
// rests on: store.TaskStreamOffsets and store.MaxTaskSeq are what a restarting node is
// told to resume from, and they are computed from the rows the prune deletes.
func TestRollUpMarksKeepTheReconciliationAnswersMonotonic(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	_, err := s.AppendLogChunks(ctx, task.ID, []LogChunk{
		{Seq: 1, Stream: StreamStdout, TS: time.Now().UTC(), Bytes: []byte("a"), SourceOffset: 100},
		{Seq: 2, Stream: StreamStderr, TS: time.Now().UTC(), Bytes: []byte("b"), SourceOffset: 20},
		{Seq: 3, Stream: StreamSidecar, Sidecar: "db", TS: time.Now().UTC(), Bytes: []byte("c"), SourceOffset: 999},
	})
	require.NoError(t, err)

	before, err := s.TaskStreamOffsets(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, StreamOffsets{Stdout: 100, Stderr: 20}, before,
		"a sidecar's offset is not the task container's")
	highBefore, err := s.MaxTaskSeq(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, uint64(3), highBefore)

	require.NoError(t, s.MarkLogsRolledUp(ctx, task.ID, LogRollUp{
		HighSeq: highBefore, StdoutOffset: before.Stdout, StderrOffset: before.Stderr,
	}))
	n, err := s.PruneLogChunks(ctx, time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	after, err := s.TaskStreamOffsets(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	highAfter, err := s.MaxTaskSeq(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, highBefore, highAfter)

	// A second roll-up may only ever raise the marks.
	require.NoError(t, s.MarkLogsRolledUp(ctx, task.ID, LogRollUp{}))
	again, err := s.TaskStreamOffsets(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, before, again)
}

func TestSubscribeTaskEventsDeliversNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	ch, err := s.SubscribeTaskEvents(ctx)
	require.NoError(t, err)

	// LISTEN is established before SubscribeTaskEvents returns, so this cannot race.
	_, err = s.AppendEvents(ctx, task.ID, eventBatch(1, 1))
	require.NoError(t, err)
	require.NoError(t, s.NotifyTaskEvents(ctx, task.ID))

	select {
	case got := <-ch:
		require.Equal(t, task.ID, got)
	case <-time.After(10 * time.Second):
		t.Fatal("no notification within 10s")
	}

	cancel()
	select {
	case _, open := <-ch:
		require.False(t, open, "cancelling the context must close the channel")
	case <-time.After(10 * time.Second):
		t.Fatal("subscription did not shut down")
	}
}
