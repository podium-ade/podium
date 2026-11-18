//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

// A system routine's run acts as Podium, and gets Linear as the app; a personal routine's is
// its owner's, and a person in a system routine's chat is themselves.
func TestOnlyASystemRoutinesRunActsAsPodium(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	r := startWith(t, st, newFakePodium(t), fakesource.New(conductor.KindDev), nil)
	c := r.cond

	draft := store.RoutineDraft{
		Name: "Investigate", Schedule: "0 9 * * 1-5", Timezone: "UTC", Instruction: "Investigate.",
		NextRunAt: time.Now().Add(time.Hour),
	}
	system, err := st.CreateSystemRoutine(ctx, "root@acme.com", draft)
	require.NoError(t, err)
	personal, err := st.CreateRoutine(ctx, testSpeaker, draft, "")
	require.NoError(t, err)

	assert.True(t, c.RunsAsPodiumForTest(ctx, "", system.ID))
	assert.False(t, c.RunsAsPodiumForTest(ctx, "root@acme.com", system.ID), "a person speaking is themselves")
	assert.False(t, c.RunsAsPodiumForTest(ctx, testSpeaker, personal.ID))
	assert.False(t, c.RunsAsPodiumForTest(ctx, "", personal.ID), "a personal routine is never Podium's")
	assert.False(t, c.RunsAsPodiumForTest(ctx, "", "rtn_gone"))
}
