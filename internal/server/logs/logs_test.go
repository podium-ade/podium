package logs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// fakeRetries records what the ingest handed back, which is the whole of what this package
// decides about a retryable error: it routes, the scheduler rules.
type fakeRetries struct {
	taskID string
	reason string
	calls  int
}

func (f *fakeRetries) Retry(_ context.Context, taskID, reason string) {
	f.taskID, f.reason, f.calls = taskID, reason, f.calls+1
}

func errorEvent(message string, retryable, abortsRun bool) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
		Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message:   message,
			Retryable: retryable,
			AbortsRun: abortsRun,
		}},
	}
}

// The bug this file exists for: a retryable error used to return without changing anything,
// on the assumption that something else would requeue the task. Nothing did, and the task
// sat in provisioning until somebody noticed. It is now handed to the scheduler, which owns
// the attempt budget and answers with either a new attempt or a terminal status.
//
// The store is deliberately nil: reaching it would mean the routing decision was made in
// the wrong place.
func TestARetryableErrorThatEndedTheRunIsHandedToTheScheduler(t *testing.T) {
	retries := &fakeRetries{}
	s := New(nil, nil)
	s.SetRetries(retries)

	const msg = "pull image podium-agent-runtime:dev: Error response from daemon: registry is down"
	s.applyStatus(context.Background(), "task_1", errorEvent(msg, true, true))

	require.Equal(t, 1, retries.calls, "a retryable error that ended the run must resolve the task")
	require.Equal(t, "task_1", retries.taskID)
	require.Equal(t, msg, retries.reason, "the operator has to be told what the node actually said")
}

// An error the run survived — an artifact too big to store, a log buffer that overflowed —
// says nothing about the task's status. The container is still going and still owes an exit
// code, so neither a transition nor a retry is this event's to ask for.
func TestAnErrorTheRunSurvivedChangesNothing(t *testing.T) {
	for _, retryable := range []bool{true, false} {
		retries := &fakeRetries{}
		s := New(nil, nil)
		s.SetRetries(retries)

		s.applyStatus(context.Background(), "task_1",
			errorEvent(`artifact "huge.bin" was not stored: over the limit`, retryable, false))

		require.Zero(t, retries.calls, "the run is still going; nobody has to take the task back")
	}
}
