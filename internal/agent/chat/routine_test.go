package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

func TestARoutineRunStartsATurnWithoutWritingTheInstruction(t *testing.T) {
	st := newFakeStore()
	st.add("chat_r", "ana")
	src := newSource(t, st)

	require.NoError(t, src.RunRoutine(context.Background(), RoutineRun{
		ChatID: "chat_r", Login: "ana", Instruction: "Summarise yesterday's merged PRs.",
		RoutineID: "rtn_1", RunID: "rrun_1", Override: profiles.Override{Model: "claude-sonnet-5-5"},
	}))
	ev := drainEvent(t, src)
	assert.Equal(t, conductor.SourceChat, ev.SourceKind)
	assert.Equal(t, "chat:chat_r", ev.SourceKey)
	assert.Equal(t, "ana", ev.Login)
	assert.Equal(t, "Summarise yesterday's merged PRs.", ev.Text)
	require.NotNil(t, ev.Routine)
	assert.Equal(t, conductor.RoutineRef{RoutineID: "rtn_1", RunID: "rrun_1"}, *ev.Routine)
	assert.Equal(t, "claude-sonnet-5-5", ev.Override.Model)
	assert.False(t, ev.Held)
	assert.Empty(t, st.messages["chat_r"], "the instruction is the routine's, never the record's")
	assert.True(t, src.HoldsRun("chat_r"))

	// A second run while the first is going is refused, not queued.
	err := src.RunRoutine(context.Background(), RoutineRun{ChatID: "chat_r", Login: "ana", Instruction: "again"})
	assert.ErrorIs(t, err, ErrChatBusy)
}

func TestARoutineRunIsRefusedForSomebodyElsesChat(t *testing.T) {
	st := newFakeStore()
	st.add("chat_r", "ana")
	src := newSource(t, st)
	err := src.RunRoutine(context.Background(), RoutineRun{ChatID: "chat_r", Login: "bo", Instruction: "x"})
	assert.ErrorContains(t, err, "is not bo's")
}

func TestAnIdleRunLeavesNothingAndADoneOneIsKeptUnderADivider(t *testing.T) {
	ctx := context.Background()
	for _, keep := range []bool{false, true} {
		st := newFakeStore()
		st.add("chat_r", "ana")
		src := newSource(t, st)
		require.NoError(t, src.RunRoutine(ctx, RoutineRun{ChatID: "chat_r", Login: "ana", Instruction: "check"}))
		drainEvent(t, src)
		sub := src.Subscribe(ctx, "chat_r")

		require.NoError(t, src.PostActivity(ctx, "chat_r", conductor.Outbound{Text: `{"tool":"list_pull_requests"}`}))
		_, err := src.Post(ctx, "chat_r", conductor.Outbound{Type: conductor.OutProgress, Text: "Checking the open PRs."})
		require.NoError(t, err)
		assert.Empty(t, st.messages["chat_r"], "a held run writes nothing while it runs")
		// Its thinking is still shown live.
		frame := recv(t, sub)
		assert.Equal(t, FrameProgress, frame.Kind)
		assert.Equal(t, "Checking the open PRs.", frame.Progress)
		sub.Close()

		src.ReleaseRun(ctx, "chat_r", keep, "Routine run · Mon 9 Nov 14:00 UTC")
		assert.False(t, src.HoldsRun("chat_r"))
		if !keep {
			assert.Empty(t, st.messages["chat_r"], "an idle run leaves nothing behind")
			continue
		}
		msgs := st.messages["chat_r"]
		require.Len(t, msgs, 3)
		assert.Equal(t, store.RoleNotice, msgs[0].Role)
		assert.Equal(t, "Routine run · Mon 9 Nov 14:00 UTC", msgs[0].Text)
		assert.Equal(t, store.RoleActivity, msgs[1].Role)
		assert.Equal(t, store.RoleProgress, msgs[2].Role)

		// After the hold, the answer is written as usual.
		_, err = src.Post(ctx, "chat_r", conductor.Outbound{Type: conductor.OutFinal, Text: "2 PRs failed CI."})
		require.NoError(t, err)
		assert.Len(t, st.messages["chat_r"], 4)
	}
}
