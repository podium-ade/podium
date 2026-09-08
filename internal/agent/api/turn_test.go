package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// fakeDelegator is the conductor, scripted. Every call records the token it was given,
// because "which turn asked" is the whole of this service's authorisation.
type fakeDelegator struct {
	tokens []string
	dlg    store.Delegation
	list   []store.Delegation
	prog   string
	err    error
}

func (f *fakeDelegator) Delegate(_ context.Context, token, playbook, instruction string) (store.Delegation, error) {
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return store.Delegation{}, f.err
	}
	f.dlg.Playbook = playbook
	f.dlg.Instruction = instruction
	return f.dlg, nil
}

func (f *fakeDelegator) GetDelegation(_ context.Context, token, id string) (store.Delegation, string, error) {
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return store.Delegation{}, "", f.err
	}
	f.dlg.ID = id
	return f.dlg, f.prog, nil
}

func (f *fakeDelegator) Delegations(_ context.Context, token string) ([]store.Delegation, error) {
	f.tokens = append(f.tokens, token)
	return f.list, f.err
}

func (f *fakeDelegator) CancelDelegation(_ context.Context, token, id, _ string) (store.Delegation, error) {
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return store.Delegation{}, f.err
	}
	f.dlg.ID = id
	return f.dlg, nil
}

// withToken is a request carrying a turn's token, as the MCP server sends it.
func withToken[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if token != "" {
		req.Header().Set(TurnTokenHeader, token)
	}
	return req
}

func running() store.Delegation {
	return store.Delegation{
		ID:          "dlg_01",
		SessionID:   "sess_01",
		TurnID:      "turn_01",
		TriggerRef:  "chat_01",
		Playbook:    "podium",
		Instruction: "fix the alignment",
		TaskID:      "task_01",
		Status:      store.TurnRunning,
		CreatedAt:   time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC),
	}
}

func TestDelegateNeedsATurnToken(t *testing.T) {
	svc := NewTurnService(&fakeDelegator{dlg: running()}, quietLogger())
	_, err := svc.Delegate(context.Background(),
		withToken(&agentv1.DelegateRequest{Playbook: "podium", Instruction: "x"}, ""))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Contains(t, err.Error(), TurnTokenHeader)
}

func TestEveryTurnRPCNeedsAToken(t *testing.T) {
	svc := NewTurnService(&fakeDelegator{dlg: running()}, quietLogger())
	ctx := context.Background()

	_, err := svc.GetDelegation(ctx, withToken(&agentv1.GetDelegationRequest{Id: "dlg_01"}, ""))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = svc.ListDelegations(ctx, withToken(&agentv1.ListDelegationsRequest{}, ""))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = svc.CancelDelegation(ctx, withToken(&agentv1.CancelDelegationRequest{Id: "dlg_01"}, ""))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAConductorWithNoHostTurnsSaysSoRatherThanCrashing(t *testing.T) {
	// Nil delegator: PODIUM_AGENT_HOST_RUNTIME is unset, so there are no host turns and
	// nothing to delegate from. It is a supported configuration, not a bug.
	svc := NewTurnService(nil, quietLogger())
	_, err := svc.Delegate(context.Background(),
		withToken(&agentv1.DelegateRequest{Playbook: "podium", Instruction: "x"}, "tok"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no host turns")
}

func TestDelegatePassesTheTokenAndTheWordsThrough(t *testing.T) {
	fake := &fakeDelegator{dlg: running()}
	svc := NewTurnService(fake, quietLogger())

	res, err := svc.Delegate(context.Background(), withToken(&agentv1.DelegateRequest{
		Playbook:    "  podium  ",
		Instruction: "  fix the alignment  ",
	}, "tok_abc"))
	require.NoError(t, err)

	assert.Equal(t, []string{"tok_abc"}, fake.tokens)
	// Trimmed, because a model's arguments arrive with whatever whitespace it wrote.
	assert.Equal(t, "podium", fake.dlg.Playbook)
	assert.Equal(t, "fix the alignment", fake.dlg.Instruction)

	got := res.Msg.GetDelegation()
	assert.Equal(t, "dlg_01", got.GetId())
	assert.Equal(t, "task_01", got.GetTaskId())
	assert.Equal(t, store.TurnRunning, got.GetStatus())
	assert.Equal(t, "2026-09-08T03:00:00Z", got.GetCreatedAt().AsTime().Format(time.RFC3339))
	assert.Nil(t, got.GetFinishedAt(), "a running delegation has not finished")
}

func TestDelegateRefusesAnEmptyPlaybookOrInstruction(t *testing.T) {
	svc := NewTurnService(&fakeDelegator{dlg: running()}, quietLogger())
	ctx := context.Background()

	_, err := svc.Delegate(ctx, withToken(&agentv1.DelegateRequest{Instruction: "x"}, "tok"))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.Delegate(ctx, withToken(&agentv1.DelegateRequest{Playbook: "podium", Instruction: "   "}, "tok"))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "the whole brief", "the message says why an empty one is useless")
}

func TestGetDelegationCarriesTheProgressAndTheAnswer(t *testing.T) {
	finished := time.Date(2026, 9, 8, 3, 5, 0, 0, time.UTC)
	done := running()
	done.Status = store.TurnSucceeded
	done.FinalText = "opened #51"
	done.FinishedAt = &finished

	svc := NewTurnService(&fakeDelegator{dlg: done, prog: "running the tests"}, quietLogger())
	res, err := svc.GetDelegation(context.Background(),
		withToken(&agentv1.GetDelegationRequest{Id: "dlg_01"}, "tok"))
	require.NoError(t, err)
	assert.Equal(t, "running the tests", res.Msg.GetProgress())
	assert.Equal(t, "opened #51", res.Msg.GetDelegation().GetFinalText())
	assert.Equal(t, store.TurnSucceeded, res.Msg.GetDelegation().GetStatus())
	assert.Equal(t, finished, res.Msg.GetDelegation().GetFinishedAt().AsTime())
}

func TestGetAndCancelNeedAnId(t *testing.T) {
	svc := NewTurnService(&fakeDelegator{dlg: running()}, quietLogger())
	ctx := context.Background()

	_, err := svc.GetDelegation(ctx, withToken(&agentv1.GetDelegationRequest{}, "tok"))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = svc.CancelDelegation(ctx, withToken(&agentv1.CancelDelegationRequest{}, "tok"))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListDelegationsReturnsEveryOneOfThisTurns(t *testing.T) {
	second := running()
	second.ID = "dlg_02"
	second.Status = store.TurnFailed
	svc := NewTurnService(&fakeDelegator{list: []store.Delegation{running(), second}}, quietLogger())

	res, err := svc.ListDelegations(context.Background(),
		withToken(&agentv1.ListDelegationsRequest{}, "tok"))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetDelegations(), 2)
	assert.Equal(t, "dlg_01", res.Msg.GetDelegations()[0].GetId())
	assert.Equal(t, store.TurnFailed, res.Msg.GetDelegations()[1].GetStatus())
}

func TestTheConductorsRefusalsBecomeConnectCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
		says string
	}{{
		name: "a token whose turn is over",
		err:  conductor.ErrNoTurn,
		want: connect.CodeUnauthenticated,
		says: "no turn is running",
	}, {
		name: "a playbook that was not offered",
		err:  conductor.ErrPlaybookNotOffered,
		want: connect.CodeInvalidArgument,
		says: "not offered to this turn",
	}, {
		name: "somebody else's delegation",
		err:  conductor.ErrDelegationNotFound,
		want: connect.CodeNotFound,
		says: "no such delegation",
	}, {
		name: "one that already finished",
		err:  conductor.ErrDelegationOver,
		want: connect.CodeFailedPrecondition,
		says: "already finished",
	}, {
		name: "anything else",
		err:  errors.New("the database is on fire"),
		want: connect.CodeInternal,
		says: "on fire",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewTurnService(&fakeDelegator{err: tc.err}, quietLogger())
			_, err := svc.GetDelegation(context.Background(),
				withToken(&agentv1.GetDelegationRequest{Id: "dlg_01"}, "tok"))
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err))
			// The conductor's own sentence reaches the caller: a model reading "that
			// playbook was not offered to this turn" can pick another one.
			assert.Contains(t, err.Error(), tc.says)
		})
	}
}

func TestTheTokenIsReadFromTheHeaderAndTrimmed(t *testing.T) {
	fake := &fakeDelegator{dlg: running()}
	svc := NewTurnService(fake, quietLogger())
	req := connect.NewRequest(&agentv1.ListDelegationsRequest{})
	req.Header().Set(TurnTokenHeader, "  tok_padded  ")
	_, err := svc.ListDelegations(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"tok_padded"}, fake.tokens)
}

func TestTheTurnHeaderIsTheOneTheRuntimeSends(t *testing.T) {
	// Both halves of one contract: agent/runtime/src/delegate.ts sends this name, and it
	// must be a valid HTTP header the Go side reads back unchanged.
	assert.Equal(t, "X-Podium-Turn", TurnTokenHeader)
	h := http.Header{}
	h.Set(TurnTokenHeader, "tok")
	assert.Equal(t, "tok", h.Get("x-podium-turn"), "header names are case-insensitive")
}
