//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateTaskStoresSpecVerbatim(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	in := testSpec()
	task, err := s.CreateTask(ctx, NewTask{Spec: in, Priority: 7, RequestedBy: "alvaro"})
	require.NoError(t, err)
	require.NotEmpty(t, task.ID)
	require.Equal(t, StatusQueued, task.Status)
	require.Equal(t, int32(7), task.Priority)
	require.Equal(t, "alvaro", task.RequestedBy)
	require.Equal(t, int32(0), task.Attempts)
	require.Equal(t, int32(in.MaxAttempts), task.MaxAttempts)
	require.Equal(t, in, task.Spec)
	require.Empty(t, task.NodeID)
	require.Nil(t, task.ExitCode)
	require.Nil(t, task.Usage)

	// The spec column is exactly pkg/spec.TaskSpec JSON, no exploded columns.
	want, err := json.Marshal(in)
	require.NoError(t, err)
	var got []byte
	require.NoError(t, s.pool.QueryRow(ctx, "select spec from tasks where id = $1", task.ID).Scan(&got))
	require.JSONEq(t, string(want), string(got))

	round, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task, round)
}

func TestGetTaskNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.GetTask(context.Background(), "task_nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestListTasksFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", []string{"linux/arm64"})

	var all []Task
	for i := 0; i < 6; i++ {
		task, err := s.CreateTask(ctx, NewTask{
			Spec:        testSpec(),
			RequestedBy: []string{"alvaro", "bot"}[i%2],
			MaxAttempts: 3,
		})
		require.NoError(t, err)
		all = append(all, task)
	}
	// Push the first two out of `queued` and onto a node.
	for _, task := range all[:2] {
		require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_"+task.ID, time.Now().Add(time.Minute)))
	}

	got, next, err := s.ListTasks(ctx, Filter{}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 6)
	require.Empty(t, next)
	// Newest first.
	require.Equal(t, all[5].ID, got[0].ID)
	require.Equal(t, all[0].ID, got[5].ID)

	got, _, err = s.ListTasks(ctx, Filter{Status: []Status{StatusQueued}}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 4)

	got, _, err = s.ListTasks(ctx, Filter{Status: []Status{StatusQueued, StatusScheduled}}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 6)

	got, _, err = s.ListTasks(ctx, Filter{NodeID: node.ID}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 2)

	got, _, err = s.ListTasks(ctx, Filter{RequestedBy: "bot"}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 3)

	got, _, err = s.ListTasks(ctx, Filter{RequestedBy: "bot", Status: []Status{StatusQueued}}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Search is an ID prefix or a case-insensitive image substring, and it composes with
	// the other filters rather than replacing them.
	got, _, err = s.ListTasks(ctx, Filter{Search: "ALPINE"}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 6)

	// ULIDs minted in the same millisecond share their whole timestamp prefix, so a short
	// prefix legitimately matches every task here; take one long enough to be unique.
	prefix := all[3].ID[:len(all[3].ID)-2]
	got, _, err = s.ListTasks(ctx, Filter{Search: prefix}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, all[3].ID, got[0].ID)

	// It is a prefix match on the ID, not a substring one.
	got, _, err = s.ListTasks(ctx, Filter{Search: all[3].ID[8:]}, Page{})
	require.NoError(t, err)
	require.Empty(t, got)

	got, _, err = s.ListTasks(ctx, Filter{Search: "alpine", RequestedBy: "bot"}, Page{})
	require.NoError(t, err)
	require.Len(t, got, 3)

	// A % is a character to search for, not a wildcard.
	got, _, err = s.ListTasks(ctx, Filter{Search: "%"}, Page{})
	require.NoError(t, err)
	require.Empty(t, got)

	got, _, err = s.ListTasks(ctx, Filter{Search: "redis"}, Page{})
	require.NoError(t, err)
	require.Empty(t, got)

	// Cursor pagination walks the whole set exactly once.
	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListTasks(ctx, Filter{}, Page{Limit: 2, Cursor: cursor})
		require.NoError(t, err)
		for _, task := range page {
			seen = append(seen, task.ID)
		}
		if next == "" {
			break
		}
		cursor = next
		require.LessOrEqual(t, len(seen), 6)
	}
	require.Len(t, seen, 6)
	require.Equal(t, all[5].ID, seen[0])
	require.Equal(t, all[0].ID, seen[5])
}

func TestAssignTaskIsTheExclusivityGate(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", nil)
	task := mustCreateTask(t, s, 3)

	expires := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Millisecond)
	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_1", expires))

	got, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, StatusScheduled, got.Status)
	require.Equal(t, node.ID, got.NodeID)
	require.Equal(t, "lease_1", got.LeaseID)
	require.Equal(t, int32(1), got.Attempts)
	require.NotNil(t, got.ScheduledAt)
	require.NotNil(t, got.LeaseExpiresAt)
	require.WithinDuration(t, expires, *got.LeaseExpiresAt, time.Millisecond)

	// A second assignment of the same task loses.
	err = s.AssignTask(ctx, task.ID, node.ID, "lease_2", expires)
	require.ErrorIs(t, err, ErrInvalidTransition)

	require.ErrorIs(t, s.AssignTask(ctx, "task_nope", node.ID, "lease_3", expires), ErrNotFound)
}

func TestTransitionEveryLegalEdge(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	edges := []struct{ from, to Status }{
		{StatusQueued, StatusScheduled},
		{StatusQueued, StatusFailed},
		{StatusQueued, StatusCancelled},
		{StatusScheduled, StatusProvisioning},
		{StatusScheduled, StatusFailed},
		{StatusScheduled, StatusCancelled},
		{StatusScheduled, StatusLost},
		{StatusScheduled, StatusQueued},
		{StatusProvisioning, StatusRunning},
		{StatusProvisioning, StatusFailed},
		{StatusProvisioning, StatusCancelled},
		{StatusProvisioning, StatusLost},
		{StatusProvisioning, StatusQueued},
		{StatusRunning, StatusSucceeded},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusCancelled},
		{StatusRunning, StatusLost},
		{StatusRunning, StatusQueued},
	}
	for _, e := range edges {
		t.Run(fmt.Sprintf("%s_to_%s", e.from, e.to), func(t *testing.T) {
			task := mustCreateTask(t, s, 5)
			driveTo(t, s, task.ID, e.from)
			got, err := s.TransitionTask(ctx, task.ID, []Status{e.from}, e.to, Patch{})
			require.NoError(t, err)
			require.Equal(t, e.to, got.Status)
		})
	}
	// Every edge in the graph must be covered by the table above.
	var count int
	for _, tos := range legalTransitions {
		count += len(tos)
	}
	require.Equal(t, count, len(edges), "the edge table has drifted from legalTransitions")
}

func TestTransitionRejectsIllegalEdges(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	illegal := []struct{ from, to Status }{
		{StatusQueued, StatusRunning},         // cannot skip scheduling
		{StatusQueued, StatusProvisioning},    // cannot skip scheduling
		{StatusQueued, StatusSucceeded},       // a task that never ran cannot succeed
		{StatusRunning, StatusScheduled},      // no going backwards
		{StatusSucceeded, StatusRunning},      // terminal
		{StatusFailed, StatusQueued},          // terminal: retries are new attempts of a live task
		{StatusCancelled, StatusSucceeded},    // terminal
		{StatusLost, StatusRunning},           // terminal
		{StatusScheduled, StatusSucceeded},    // must run first
		{StatusProvisioning, StatusSucceeded}, // must run first
	}
	for _, e := range illegal {
		t.Run(fmt.Sprintf("%s_to_%s", e.from, e.to), func(t *testing.T) {
			task := mustCreateTask(t, s, 5)
			driveTo(t, s, task.ID, e.from)
			_, err := s.TransitionTask(ctx, task.ID, nil, e.to, Patch{})
			require.ErrorIs(t, err, ErrInvalidTransition)

			after, err := s.GetTask(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, e.from, after.Status, "a rejected transition must not change the row")
		})
	}
}

func TestTransitionRejectsWrongFromState(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	task := mustCreateTask(t, s, 5)
	driveTo(t, s, task.ID, StatusRunning)

	// running -> succeeded is a legal edge, but the caller asserted the task was provisioning.
	_, err := s.TransitionTask(ctx, task.ID, []Status{StatusProvisioning}, StatusSucceeded, Patch{})
	require.ErrorIs(t, err, ErrInvalidTransition)

	_, err = s.TransitionTask(ctx, "task_nope", nil, StatusCancelled, Patch{})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.TransitionTask(ctx, task.ID, nil, Status("bogus"), Patch{})
	require.ErrorIs(t, err, ErrInvalidTransition)
}

func TestTransitionRequeueRespectsAttemptBudget(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", nil)

	// max_attempts 2: the first attempt may be requeued.
	task := mustCreateTask(t, s, 2)
	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_1", time.Now().Add(time.Minute)))
	got, err := s.TransitionTask(ctx, task.ID, []Status{StatusScheduled}, StatusQueued, Patch{})
	require.NoError(t, err)
	require.Equal(t, StatusQueued, got.Status)
	require.Equal(t, int32(1), got.Attempts)
	require.Empty(t, got.NodeID, "requeue must release the node")
	require.Empty(t, got.LeaseID, "requeue must release the lease")
	require.Nil(t, got.LeaseExpiresAt)

	// Second attempt exhausts the budget, so the next requeue is refused.
	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_2", time.Now().Add(time.Minute)))
	_, err = s.TransitionTask(ctx, task.ID, []Status{StatusScheduled}, StatusQueued, Patch{})
	require.ErrorIs(t, err, ErrInvalidTransition)

	after, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, StatusScheduled, after.Status)
	require.Equal(t, int32(2), after.Attempts)
}

func TestTransitionAppliesPatch(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	node := mustCreateNode(t, s, "n1", nil)
	task := mustCreateTask(t, s, 1)

	require.NoError(t, s.AssignTask(ctx, task.ID, node.ID, "lease_1", time.Now().Add(time.Minute)))
	_, err := s.TransitionTask(ctx, task.ID, []Status{StatusScheduled}, StatusProvisioning, Patch{})
	require.NoError(t, err)

	started := time.Now().UTC().Truncate(time.Millisecond)
	running, err := s.TransitionTask(ctx, task.ID, []Status{StatusProvisioning}, StatusRunning, Patch{
		StartedAt: ptrTime(started),
	})
	require.NoError(t, err)
	require.NotNil(t, running.StartedAt)
	require.WithinDuration(t, started, *running.StartedAt, time.Millisecond)

	finished := started.Add(3 * time.Second)
	done, err := s.TransitionTask(ctx, task.ID, []Status{StatusRunning}, StatusFailed, Patch{
		FinishedAt:    ptrTime(finished),
		ExitCode:      ptrInt32(3),
		Usage:         &Usage{CPUSeconds: 1.5, PeakMemoryMB: 128, WallMS: 3000},
		FailureReason: ptrString("exit 3"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusFailed, done.Status)
	require.NotNil(t, done.FinishedAt)
	require.WithinDuration(t, finished, *done.FinishedAt, time.Millisecond)
	require.NotNil(t, done.ExitCode)
	require.Equal(t, int32(3), *done.ExitCode)
	require.Equal(t, &Usage{CPUSeconds: 1.5, PeakMemoryMB: 128, WallMS: 3000}, done.Usage)
	require.Equal(t, "exit 3", done.FailureReason)
	// Columns the patch did not mention survive.
	require.NotNil(t, done.StartedAt)
	require.Equal(t, node.ID, done.NodeID)
	require.Equal(t, "lease_1", done.LeaseID)
}

func TestClaimQueuedTasksOrdersByPriorityThenAge(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	low, err := s.CreateTask(ctx, NewTask{Spec: testSpec(), RequestedBy: "dev", Priority: 0})
	require.NoError(t, err)
	high, err := s.CreateTask(ctx, NewTask{Spec: testSpec(), RequestedBy: "dev", Priority: 10})
	require.NoError(t, err)
	mid, err := s.CreateTask(ctx, NewTask{Spec: testSpec(), RequestedBy: "dev", Priority: 5})
	require.NoError(t, err)

	got, err := s.ClaimQueuedTasks(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, []string{high.ID, mid.ID, low.ID}, []string{got[0].ID, got[1].ID, got[2].ID})

	// Claiming does not transition anything.
	still, err := s.GetTask(ctx, high.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, still.Status)
	require.Equal(t, int32(0), still.Attempts)

	got, err = s.ClaimQueuedTasks(ctx, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

// TestClaimQueuedTasksSkipsLockedRows proves the SKIP LOCKED half deterministically: a
// transaction outside the store holds the first rows, and the claim steps over them instead of
// blocking.
func TestClaimQueuedTasksSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	var ids []string
	for i := 0; i < 5; i++ {
		task := mustCreateTask(t, s, 1)
		ids = append(ids, task.ID)
	}

	holder, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	var locked []string
	rows, err := holder.Query(ctx,
		"select id from tasks where id = any($1::text[]) order by id for update", ids[:3])
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		locked = append(locked, id)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	require.Len(t, locked, 3)

	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	got, err := s.ClaimQueuedTasks(claimCtx, 5)
	require.NoError(t, err)
	require.Len(t, got, 2, "the three locked rows must be skipped, not waited on")
	for _, task := range got {
		require.NotContains(t, locked, task.ID)
	}
}

// TestClaimQueuedTasksTwoConcurrentClaimers is the acceptance case: two schedulers racing over
// the same queue must never both act on the same task.
func TestClaimQueuedTasksTwoConcurrentClaimers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	nodeA := mustCreateNode(t, s, "a", nil)
	nodeB := mustCreateNode(t, s, "b", nil)

	const total = 60
	for i := 0; i < total; i++ {
		mustCreateTask(t, s, 1)
	}

	claim := func(nodeID string) []string {
		var won []string
		for {
			batch, err := s.ClaimQueuedTasks(ctx, 5)
			if err != nil {
				t.Error(err)
				return won
			}
			if len(batch) == 0 {
				return won
			}
			for _, task := range batch {
				err := s.AssignTask(ctx, task.ID, nodeID, "lease_"+task.ID, time.Now().Add(time.Minute))
				switch {
				case err == nil:
					won = append(won, task.ID)
				case errors.Is(err, ErrInvalidTransition):
					// The other claimer got there first; that is the whole point.
				default:
					t.Error(err)
					return won
				}
			}
		}
	}

	var wg sync.WaitGroup
	results := make([][]string, 2)
	nodes := []string{nodeA.ID, nodeB.ID}
	wg.Add(2)
	for i := range results {
		go func(i int) {
			defer wg.Done()
			results[i] = claim(nodes[i])
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for _, won := range results {
		for _, id := range won {
			seen[id]++
		}
	}
	require.Len(t, seen, total, "every task must be assigned exactly once")
	for id, n := range seen {
		require.Equal(t, 1, n, "task %s was assigned twice", id)
	}

	tasks, _, err := s.ListTasks(ctx, Filter{}, Page{Limit: MaxPageLimit})
	require.NoError(t, err)
	require.Len(t, tasks, total)
	for _, task := range tasks {
		require.Equal(t, StatusScheduled, task.Status)
		require.Equal(t, int32(1), task.Attempts, "task %s ran more than one attempt", task.ID)
	}
}
