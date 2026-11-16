package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each row of the bot's is placed on the turn that said it: by its task when it has one, and
// otherwise on the turn that was running when it was said.
func TestATurnIndexPlacesRowsOnTheTurnThatSaidThem(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	ix := NewTurnIndex([]TurnRunner{
		{TurnID: "t2", StartedAt: t0.Add(10 * time.Minute), Login: "bob", Tasks: []string{"task-b"}},
		{TurnID: "t1", StartedAt: t0, Login: "alice", Tasks: []string{"task-a1", "task-a2"}},
		{TurnID: "t3", StartedAt: t0.Add(20 * time.Minute), Routine: "Triage", RoutineSystem: true},
	})

	assert.Nil(t, ix.TurnOf("", t0.Add(-time.Second)), "before the first turn is nobody's")
	assert.Equal(t, "t1", ix.TurnOf("", t0).TurnID)
	assert.Equal(t, "t1", ix.TurnOf("", t0.Add(9*time.Minute)).TurnID)
	assert.Equal(t, "t2", ix.TurnOf("", t0.Add(15*time.Minute)).TurnID)
	assert.Equal(t, "t3", ix.TurnOf("", t0.Add(time.Hour)).TurnID)

	// A delegated task's rows are its turn's, however late they come.
	assert.Equal(t, "t1", ix.TurnOf("task-a2", t0.Add(time.Hour)).TurnID)
	assert.Equal(t, "t2", ix.TurnOf("task-b", t0.Add(time.Hour)).TurnID)
	assert.Nil(t, ix.TurnOf("task-unknown", t0.Add(time.Hour)), "a task no turn ran is nobody's")

	require.NotNil(t, ix.Latest())
	assert.Equal(t, "t3", ix.Latest().TurnID)
	assert.Nil(t, NewTurnIndex(nil).Latest())
}

func TestATurnRunnerNamesAPersonOrNobody(t *testing.T) {
	assert.Equal(t, "alice@acme.com", runnerLogin("alice@acme.com"))
	assert.Empty(t, runnerLogin(TurnStartedByApp))
	assert.Empty(t, runnerLogin(TurnStartedByRoutine("r-1")))
	assert.Empty(t, runnerLogin(""))
}
