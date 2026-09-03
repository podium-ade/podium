//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateListAndGetArtifacts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)

	first, err := s.CreateArtifact(ctx, NewArtifact{
		TaskID:      task.ID,
		Name:        "report.txt",
		ObjectKey:   "tasks/" + task.ID + "/artifacts/art_1-report.txt",
		SizeBytes:   17,
		ContentType: "text/plain",
		SHA256:      "abc123",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.ID)
	require.Equal(t, ArtifactKindFile, first.Kind, "kind defaults to file")
	require.False(t, first.CreatedAt.IsZero())

	logArtifact, err := s.CreateArtifact(ctx, NewArtifact{
		TaskID:      task.ID,
		Kind:        ArtifactKindLog,
		Name:        "stdout",
		ObjectKey:   "tasks/" + task.ID + "/logs/stdout.log.zst",
		SizeBytes:   99,
		ContentType: "text/plain+zstd",
	})
	require.NoError(t, err)

	all, err := s.ListArtifacts(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, all, 2)

	logs, err := s.ListArtifactsOfKind(ctx, task.ID, ArtifactKindLog)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, logArtifact.ID, logs[0].ID)

	got, err := s.GetArtifact(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, first, got)

	_, err = s.GetArtifact(ctx, "art_missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestArtifactsGoWithTheirTask(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 1)
	_, err := s.CreateArtifact(ctx, NewArtifact{
		TaskID: task.ID, Name: "r.txt", ObjectKey: "k",
	})
	require.NoError(t, err)

	_, err = s.pool.Exec(ctx, "delete from tasks where id = $1", task.ID)
	require.NoError(t, err)
	require.Zero(t, countRows(t, s, "select count(*) from artifacts where task_id = $1", task.ID))
}

func TestTasksPendingLogRollUpIsTerminalOnly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	running := mustCreateTask(t, s, 1)
	driveTo(t, s, running.ID, StatusRunning)

	done := mustCreateTask(t, s, 1)
	driveTo(t, s, done.ID, StatusRunning)
	finished := time.Now().UTC()
	code := int32(0)
	_, err := s.TransitionTask(ctx, done.ID, []Status{StatusRunning}, StatusSucceeded,
		Patch{FinishedAt: &finished, ExitCode: &code})
	require.NoError(t, err)

	pending, err := s.TasksPendingLogRollUp(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, []string{done.ID}, pending,
		"only a terminal task may be rolled up: a running one is still writing")

	// The settle window keeps the sweep off the heels of the last event batch.
	pending, err = s.TasksPendingLogRollUp(ctx, time.Now().UTC().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	require.NoError(t, s.MarkLogsRolledUp(ctx, done.ID, LogRollUp{HighSeq: 4}))
	pending, err = s.TasksPendingLogRollUp(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, pending, "a task is only rolled up once")

	up, err := s.TaskLogRollUp(ctx, done.ID)
	require.NoError(t, err)
	require.NotNil(t, up.At)
	require.Equal(t, uint64(4), up.HighSeq)
}
