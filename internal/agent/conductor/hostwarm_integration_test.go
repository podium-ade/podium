//go:build integration

package conductor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
)

func warmHostRuntime(t *testing.T) *conductor.HostRuntime {
	h := hostRuntime(t, "sk-test")
	h.Warm = time.Minute
	return h
}

func finalsSaying(records []fakesource.Record, prefix string) int {
	n := 0
	for _, r := range posts(records, conductor.OutFinal) {
		if strings.HasPrefix(r.Text, prefix) {
			n++
		}
	}
	return n
}

// TestAWarmHostTurnAnswersTheNextMessageWithoutANewProcess: the conversation's second message
// is written into the turn that answered the first, which is still up.
func TestAWarmHostTurnAnswersTheNextMessageWithoutANewProcess(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = warmHostRuntime(t) })
	ctx := context.Background()

	first := inbound("C1/1.1", "why does it 500?")
	first.Env = hostEnv(map[string]string{hostFakeWarmEnv: "1"})
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the first answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	assert.Equal(t, store.TurnRunning, turnStatus(st, first.SourceKey), "answered, and still up")

	second := inbound("C1/1.1", "and the 404?")
	second.Env = first.Env
	require.NoError(t, src.Send(ctx, second))
	waitFor(t, 30*time.Second, "the warm turn's answer", func() bool {
		return finalsSaying(src.Records(), "echo: and the 404?") == 1
	})
	assert.Empty(t, fake.Specs(), "no task either")

	turns, err := st.ListTurns(ctx, turnOf(t, st, first.SourceKey).SessionID, 10)
	require.NoError(t, err)
	assert.Len(t, turns, 1, "one process answered both, so one turn")
}

// TestAWarmHostTurnGivesItsSlotToAConversationWithWork: with every slot held by a turn that is
// only waiting, a new conversation does not queue behind it.
func TestAWarmHostTurnGivesItsSlotToAConversationWithWork(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = warmHostRuntime(t)
		o.HostMaxTurns = 1
	})
	ctx := context.Background()
	env := hostEnv(map[string]string{hostFakeWarmEnv: "1"})

	idle := inbound("C1/1.1", "first conversation")
	idle.Env = env
	require.NoError(t, src.Send(ctx, idle))
	waitFor(t, 30*time.Second, "the first conversation's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})

	busy := inbound("C2/2.1", "second conversation")
	busy.Env = env
	require.NoError(t, src.Send(ctx, busy))
	waitFor(t, 30*time.Second, "the idle turn to give its slot up and finish", func() bool {
		return turnStatus(st, idle.SourceKey) == store.TurnSucceeded
	})
	waitFor(t, 30*time.Second, "the second conversation's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 2
	})
}
