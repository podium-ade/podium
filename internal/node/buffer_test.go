package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

func logChunk(n int) *podiumv1.TaskEvent {
	return logEvent("stdout", "", make([]byte, n), int64(n))
}

func TestBufferNumbersOneSequenceSpacePerTask(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)

	provisioning, dropped := b.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING})
	require.Zero(t, dropped)
	require.EqualValues(t, 1, provisioning.GetSeq())
	require.Equal(t, "task_1", provisioning.GetTaskId())
	require.Equal(t, "lease_1", provisioning.GetLeaseId())
	require.NotNil(t, provisioning.GetTs(), "the node stamps every event")

	line, _ := b.append(logChunk(10))
	require.EqualValues(t, 2, line.GetSeq(), "logs and lifecycle events share one space")

	finished, _ := b.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED})
	require.EqualValues(t, 3, finished.GetSeq())
	require.EqualValues(t, 3, b.high())
}

func TestBufferReplaysUnackedEventsAfterAReconnect(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)
	for range 5 {
		b.append(logChunk(4))
	}

	first := b.pending()
	require.Len(t, first, 5)
	b.markSent(5)
	require.Empty(t, b.pending(), "an event already on the wire is not sent twice")

	// The server committed the first batch only; everything after seq 2 is unacked.
	b.ack(2)
	b.rewind()

	replay := b.pending()
	require.Len(t, replay, 3)
	require.EqualValues(t, 3, replay[0].GetSeq())
	require.Same(t, first[2], replay[0], "a replay is byte-identical: it is the same message")

	b.markSent(5)
	b.ack(5)
	require.Empty(t, b.pending())
	b.rewind()
	require.Empty(t, b.pending(), "acked events are gone for good")
}

func TestBufferAckIsMonotonic(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)
	for range 3 {
		b.append(logChunk(4))
	}
	b.markSent(3)
	b.ack(3)
	b.ack(1)
	require.Empty(t, b.pending())
	b.rewind()
	require.Empty(t, b.pending(), "a stale ack must not resurrect events")
}

func TestBufferDropsOldestLogChunksOnOverflow(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)

	// One lifecycle event first: it must survive whatever the log flood does.
	started, _ := b.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED})

	chunk := 256 << 10
	total := 0
	dropped := 0
	for total <= ReplayBufferBytes+4*chunk {
		_, d := b.append(logChunk(chunk))
		dropped += d
		total += chunk
	}
	require.Positive(t, dropped, "the buffer is bounded")
	require.True(t, b.claimOverflow(), "the first overflow is reported")
	require.False(t, b.claimOverflow(), "and only the first")

	b.mu.Lock()
	held := b.bytes
	kept := append([]*podiumv1.TaskEvent(nil), b.events...)
	b.mu.Unlock()
	require.LessOrEqual(t, held, ReplayBufferBytes)

	require.Same(t, started, kept[0], "started is never dropped")
	for i := 1; i < len(kept); i++ {
		require.Greater(t, kept[i].GetSeq(), kept[i-1].GetSeq(), "what is left is still in seq order")
	}

	// exited and finished always fit, whatever the log pressure was.
	exited, _ := b.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED})
	finished, _ := b.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED})
	b.mu.Lock()
	last := b.events[len(b.events)-1]
	secondLast := b.events[len(b.events)-2]
	b.mu.Unlock()
	require.Same(t, finished, last)
	require.Same(t, exited, secondLast)
}

func TestBufferWaitAcked(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)
	b.append(logChunk(4))
	b.append(logChunk(4))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		time.Sleep(20 * time.Millisecond)
		b.ack(1)
		time.Sleep(20 * time.Millisecond)
		b.ack(2)
	}()
	require.NoError(t, b.waitAcked(ctx, 2))

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDeadline()
	require.Error(t, b.waitAcked(deadline, 99), "waiting for an ack that never comes respects the context")
}

func TestBufferStallsWhenNothingIsAcked(t *testing.T) {
	b := newBuffer("task_1", "lease_1", "", 0)
	b.append(logChunk(4))
	require.False(t, b.stalled(time.Millisecond), "nothing has been sent yet")

	b.markSent(1)
	require.False(t, b.stalled(time.Hour))
	time.Sleep(5 * time.Millisecond)
	require.True(t, b.stalled(time.Millisecond), "a send the server never acked is sent again")

	b.ack(1)
	require.False(t, b.stalled(time.Millisecond))
}

// TestBufferBookmarkSurvivesForAdoption is what lets a restarted daemon take a container
// over without reusing sequence numbers the server has already stored.
func TestBufferBookmarkSurvivesForAdoption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks", "task_1")
	b := newBuffer("task_1", "lease_1", dir, 0)
	for range 4 {
		b.append(logChunk(8))
	}
	b.flushState()

	st, err := loadTaskState(dir)
	require.NoError(t, err)
	require.Equal(t, "task_1", st.TaskID)
	require.Equal(t, "lease_1", st.LeaseID)
	require.EqualValues(t, 4, st.HighSeq)

	adopted := newBuffer(st.TaskID, st.LeaseID, dir, st.HighSeq)
	next, _ := adopted.append(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED})
	require.EqualValues(t, 5, next.GetSeq(), "the adopted run carries on where the last one stopped")
}
