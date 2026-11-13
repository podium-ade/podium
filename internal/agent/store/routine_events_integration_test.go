//go:build integration

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func linearDelivery(id, event, team string, at time.Time) LinearDelivery {
	payload, _ := json.Marshal(map[string]any{
		"type": "Issue", "action": "create", "url": "https://linear.example/acme/issue/ENG-1",
		"data": map[string]any{"id": "issue-" + id, "identifier": "ENG-1", "teamId": "team-1"},
	})
	return LinearDelivery{DeliveryID: id, Event: event, TeamKey: team, Payload: payload, ReceivedAt: at}
}

func TestALinearRoutineIsNeverDueAndKeepsItsEventsAndTeams(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateSystemRoutine(ctx, "admin@acme.com", linearDraft())
	require.NoError(t, err)
	assert.Equal(t, RoutineKindLinear, r.Kind)
	assert.Equal(t, []string{"Issue.create", "Comment.create"}, r.Events)
	assert.Equal(t, []string{"ENG", "WEB"}, r.Teams)
	assert.Empty(t, r.Schedule)

	due, err := s.DueRoutines(ctx, time.Now().Add(24*time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "a routine on Linear events has no schedule to fall due")

	revs, err := s.ListRoutineRevisions(ctx, r.ID, 10)
	require.NoError(t, err)
	require.Len(t, revs, 1)
	assert.Equal(t, "On Linear: issue created, comment created (ENG, WEB)", revs[0].Schedule)

	// Moved to a schedule, it names no events.
	d := routineDraft(time.Now().Add(time.Hour))
	updated, err := s.UpdateRoutine(ctx, r.ID, "admin@acme.com", d)
	require.NoError(t, err)
	assert.Equal(t, RoutineKindSchedule, updated.Kind)
	assert.Empty(t, updated.Events)
	assert.Empty(t, updated.Teams)
}

func TestALinearDeliveryIsKeptOnceForEachRoutineThatAsksForIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	eng, err := s.CreateSystemRoutine(ctx, "admin@acme.com", linearDraft()) // ENG, WEB
	require.NoError(t, err)
	every := linearDraft()
	every.Teams = nil
	anyTeam, err := s.CreateSystemRoutine(ctx, "admin@acme.com", every)
	require.NoError(t, err)
	updates := linearDraft()
	updates.Events, updates.Teams = []string{"Issue.update"}, nil
	_, err = s.CreateSystemRoutine(ctx, "admin@acme.com", updates)
	require.NoError(t, err)
	paused, err := s.CreateSystemRoutine(ctx, "admin@acme.com", every)
	require.NoError(t, err)
	_, err = s.SetRoutineEnabled(ctx, paused.ID, false, "", paused.NextRunAt)
	require.NoError(t, err)
	scheduled, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(now), "")
	require.NoError(t, err)

	n, err := s.StoreRoutineEvents(ctx, linearDelivery("d-1", "Issue.create", "eng", now))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the ENG routine and the every-team one; not the one on updates, the paused one or a schedule")
	n, err = s.StoreRoutineEvents(ctx, linearDelivery("d-1", "Issue.create", "ENG", now))
	require.NoError(t, err)
	assert.Zero(t, n, "a retried delivery is kept once")
	n, err = s.StoreRoutineEvents(ctx, linearDelivery("d-2", "Issue.create", "", now))
	require.NoError(t, err)
	assert.Equal(t, 1, n, "an event that names no team reaches only the routine that names none")
	n, err = s.StoreRoutineEvents(ctx, linearDelivery("d-3", "Reaction.create", "ENG", now))
	require.NoError(t, err)
	assert.Zero(t, n, "nothing runs on an entity a routine cannot name")

	pending, err := s.PendingRoutineEvents(ctx, []string{eng.ID, anyTeam.ID, paused.ID, scheduled.ID})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{eng.ID: 1, anyTeam.ID: 2}, pending)

	waiting, err := s.RoutinesWithPendingEvents(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, waiting, 2)
}

func TestARunIsGivenTheOldestEventsOnceAndGivesThemBackIfItNeverStarts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateSystemRoutine(ctx, "admin@acme.com", linearDraft())
	require.NoError(t, err)
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"d-1", "d-2", "d-3"} {
		_, err := s.StoreRoutineEvents(ctx, linearDelivery(id, "Issue.create", "ENG", base.Add(time.Duration(i)*time.Minute)))
		require.NoError(t, err)
	}

	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now(), RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	n, err := s.AttachRoutineEvents(ctx, run.ID, r.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	given, err := s.RoutineEventsForRun(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, given, 2)
	assert.Equal(t, "d-1", given[0].DeliveryID, "oldest first")
	assert.Equal(t, "d-2", given[1].DeliveryID)
	assert.Equal(t, "Issue.create", given[0].Event)

	other, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now().Add(time.Second), RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	n, err = s.AttachRoutineEvents(ctx, other.ID, r.ID, 20)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "an event given to one run is not given to another")

	back, err := s.ReleaseRoutineEvents(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, back)
	pending, err := s.PendingRoutineEvents(ctx, []string{r.ID})
	require.NoError(t, err)
	assert.Equal(t, 2, pending[r.ID])

	count, err := s.CountRoutineRunsSince(ctx, r.ID, RoutineTriggerEvent, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	empty, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now().Add(2*time.Second), RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.DeleteUnstartedRoutineRun(ctx, empty.ID))
	_, err = s.GetRoutineRun(ctx, empty.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestWaitingEventsAreCappedAndPruned(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.CreateSystemRoutine(ctx, "admin@acme.com", linearDraft())
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = s.StoreRoutineEvents(ctx, linearDelivery("old", "Issue.create", "ENG", now.Add(-25*time.Hour)))
	require.NoError(t, err)
	for i, id := range []string{"a", "b", "c"} {
		_, err := s.StoreRoutineEvents(ctx, linearDelivery(id, "Issue.create", "ENG", now.Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
	}

	pruned, err := s.PruneRoutineEvents(ctx, now.Add(-24*time.Hour), now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, pruned, "a day-old event nobody ran is dropped")

	dropped, err := s.CapPendingRoutineEvents(ctx, r.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, dropped, "the oldest beyond the cap")
	run, _, err := s.ClaimRoutineRun(ctx, r.ID, now, RoutineTriggerEvent, RoutineRunRunning)
	require.NoError(t, err)
	_, err = s.AttachRoutineEvents(ctx, run.ID, r.ID, 20)
	require.NoError(t, err)
	given, err := s.RoutineEventsForRun(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, given, 2)
	assert.Equal(t, "b", given[0].DeliveryID)
	assert.Equal(t, "c", given[1].DeliveryID)
}
