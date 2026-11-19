//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func i64(v int64) *int64 { return &v }

func intp(v int) *int { return &v }

// An automatic review records a turn only so its delegation has one to hang off: no backend,
// no task, no cost. It is not a unit of spend, and counting it made every review two of them,
// one a $0 "unrecorded" model.
func TestUsageLeavesOutTheTurnARelayRecords(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSessionOf(t, s, "github", "github:acme/api#7", "")
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	insertTurn(t, s, "turn_relay", sess, "", at, usd(0), i32(0))
	insertDelegation(t, s, "dlg_review", sess, "turn_relay", "task_review", "review", at, usd(1.5), i32(30), opus)
	// A turn from before 0009 has no backend either, but it ran a task and spent money.
	insertTurn(t, s, "turn_old", sess, "task_old", at.Add(time.Hour), usd(0.25), i32(3))

	u, err := s.Usage(ctx, UsageQuery{From: at.Add(-time.Hour), To: at.Add(2 * time.Hour), Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, u.Totals.Turns, "the review's task and the old turn, not the relay")
	assert.InDelta(t, 1.75, u.Totals.CostUSD, 1e-9)
	ids := []string{}
	for _, c := range u.Costs {
		ids = append(ids, c.TurnID)
	}
	assert.ElementsMatch(t, []string{"dlg_review", "turn_old"}, ids)
	require.Len(t, u.Sources, 1)
	assert.Equal(t, "github", u.Sources[0].Origin)
}

// What paid, and the tokens, are recorded once known and read back on the row, in the
// credential grouping and in the totals. A subscription's share is reported apart.
func TestUsageReportsWhatPaidAndTheTokens(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSessionOf(t, s, "chat", "chat:c1", "")
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}
	gpt := Backend{Agent: "openai", Model: "gpt-5", Provider: "openai"}

	turn, err := s.CreateTurnFor(ctx, sess, "c1", "ana@acme.com", opus)
	require.NoError(t, err)
	require.NoError(t, s.SetTurnCredential(ctx, turn.ID,
		Credential{AuthKind: AuthSubscription, Owner: "ana@acme.com", Account: "ana@acme.com"}))
	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, intp(5), usd(2), "ok"))
	require.NoError(t, s.SetTurnTokens(ctx, turn.ID, Tokens{
		Input: i64(100), Output: i64(20), Reasoning: i64(5), CacheRead: i64(1000), CacheWrite: i64(50),
	}))

	dlg, err := s.CreateDelegation(ctx, NewDelegation{
		SessionID: sess, TurnID: turn.ID, TriggerRef: "c1", Playbook: "fix", Instruction: "fix it", Backend: gpt,
	})
	require.NoError(t, err)
	require.NoError(t, s.SetDelegationCredential(ctx, dlg.ID, Credential{AuthKind: AuthAPIKey, Account: "sk-…abcd"}))
	require.NoError(t, s.SetDelegationTokens(ctx, dlg.ID, Tokens{Input: i64(7), Output: i64(3)}))
	_, err = s.pool.Exec(ctx, `update delegations set cost_usd = 0.5, num_turns = 2 where id = $1`, dlg.ID)
	require.NoError(t, err)

	now := time.Now()
	u, err := s.Usage(ctx, UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10})
	require.NoError(t, err)

	assert.InDelta(t, 2.5, u.Totals.CostUSD, 1e-9)
	assert.InDelta(t, 2.0, u.Totals.SubscriptionCostUSD, 1e-9)
	assert.EqualValues(t, 107, u.Totals.InputTokens)
	assert.EqualValues(t, 23, u.Totals.OutputTokens)
	assert.EqualValues(t, 1000, u.Totals.CacheReadTokens)

	creds := map[string]UsageCredential{}
	for _, c := range u.Credentials {
		creds[c.Key()] = c
	}
	sub := creds["subscription|anthropic|ana@acme.com"]
	assert.InDelta(t, 2.0, sub.CostUSD, 1e-9)
	assert.Equal(t, "ana@acme.com", sub.Account)
	company := creds["api_key|openai|"]
	assert.InDelta(t, 0.5, company.CostUSD, 1e-9)
	assert.Empty(t, company.Owner, "the company's key")

	byID := map[string]TurnCost{}
	for _, c := range u.Costs {
		byID[c.TurnID] = c
	}
	got := byID[turn.ID]
	assert.Equal(t, UsageKindTurn, got.Kind)
	assert.Equal(t, AuthSubscription, got.Credential.AuthKind)
	require.NotNil(t, got.Tokens.CacheWrite)
	assert.EqualValues(t, 50, *got.Tokens.CacheWrite)
	d := byID[dlg.ID]
	assert.Equal(t, UsageKindDelegation, d.Kind)
	assert.Equal(t, "ana@acme.com", d.StartedBy, "a delegation is its delegating turn's person's")
	assert.Nil(t, d.Tokens.CacheRead, "a field the runtime did not report stays unreported")

	// A filter narrows every part of the answer to one set.
	f, err := s.Usage(ctx, UsageQuery{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10,
		Filter: UsageFilter{Credential: "api_key|openai|"},
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.5, f.Totals.CostUSD, 1e-9)
	require.Len(t, f.Costs, 1)
	assert.Equal(t, dlg.ID, f.Costs[0].TurnID)
	require.Len(t, f.Backends, 1)
	assert.Equal(t, "gpt-5", f.Backends[0].Model)

	none, err := s.Usage(ctx, UsageQuery{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), Filter: UsageFilter{AuthKind: "-"},
	})
	require.NoError(t, err)
	assert.Zero(t, none.Totals.Turns, "every row here recorded what paid")
}

// A summary is spend no turn held. It is counted, under its own kind and origin.
func TestUsageCountsHousekeeping(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSessionOf(t, s, "slack", "slack:C1:1.1", "")
	now := time.Now()
	require.NoError(t, s.RecordHousekeeping(ctx, Housekeeping{
		SessionID: sess, Purpose: UsageKindSummary, Status: TurnSucceeded,
		StartedAt: now.Add(-time.Minute), FinishedAt: now, NumTurns: intp(1), CostUSD: usd(0.03),
		Backend:    Backend{Agent: "claude", Model: "claude-haiku-5", Provider: "anthropic"},
		Credential: Credential{AuthKind: AuthAPIKey},
		Tokens:     Tokens{Input: i64(4000), Output: i64(200)},
	}))

	u, err := s.Usage(ctx, UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10})
	require.NoError(t, err)
	assert.InDelta(t, 0.03, u.Totals.CostUSD, 1e-9)
	require.Len(t, u.Costs, 1)
	assert.Equal(t, UsageKindSummary, u.Costs[0].Kind)
	assert.Equal(t, "housekeeping", u.Costs[0].Origin)
	assert.Empty(t, u.Costs[0].StartedBy, "nobody asked for it")
}

// A routine's run is grouped under the routine, whichever conversation it ran in, and so are
// the tasks it delegated. The routine's spend cap counts both.
func TestUsageAttributesARoutinesRunAndItsTasks(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	r, err := s.CreateRoutine(ctx, "ana@acme.com", routineDraft(time.Now()), "")
	require.NoError(t, err)
	run, ok, err := s.ClaimRoutineRun(ctx, r.ID, time.Now(), RoutineTriggerSchedule, RoutineRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	sess, err := s.UpsertSession(ctx, Session{SourceKind: "chat", SourceKey: ChatSourceKey(r.ChatID), Profile: "podium"})
	require.NoError(t, err)
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}
	turn, err := s.CreateTurnFor(ctx, sess.ID, r.ChatID, "ana@acme.com", opus)
	require.NoError(t, err)
	require.NoError(t, s.LinkRoutineRunTurn(ctx, run.ID, turn.ID))
	require.NoError(t, s.FinishTurn(ctx, turn.ID, TurnSucceeded, intp(2), usd(0.2), "ok"))
	dlg, err := s.CreateDelegation(ctx, NewDelegation{
		SessionID: sess.ID, TurnID: turn.ID, TriggerRef: r.ChatID, Playbook: "fix", Instruction: "x", Backend: opus,
	})
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx, `update delegations set cost_usd = 1.3 where id = $1`, dlg.ID)
	require.NoError(t, err)

	now := time.Now()
	u, err := s.Usage(ctx, UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10})
	require.NoError(t, err)
	require.Len(t, u.Routines, 1)
	assert.Equal(t, r.ID, u.Routines[0].RoutineID)
	assert.Equal(t, r.Name, u.Routines[0].Name)
	assert.Equal(t, 2, u.Routines[0].Turns)
	assert.InDelta(t, 1.5, u.Routines[0].CostUSD, 1e-9)
	require.Len(t, u.Sources, 1)
	assert.Equal(t, "routine", u.Sources[0].Origin)
	require.Len(t, u.Conversations, 1)
	assert.Equal(t, r.ChatID, u.Conversations[0].ChatID)

	spent, err := s.RoutineSpendSince(ctx, r.ID, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.InDelta(t, 1.5, spent, 1e-9, "the cap counts the tasks the run delegated")
	mine, err := s.PersonRoutineSpendSince(ctx, "ana@acme.com", now.Add(-time.Hour))
	require.NoError(t, err)
	assert.InDelta(t, 1.5, mine, 1e-9)
}

// The rows page by a cursor on (started_at, id), so rows that share a start are neither
// repeated nor skipped, and a cursor this server did not hand out is refused.
func TestUsageRowsPageWithoutRepeatsOrGaps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:2.2", "triage")
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for i := range 7 {
		// Pairs share a start.
		insertTurnOn(t, s, "turn_"+string(rune('a'+i)), sess, "task_"+string(rune('a'+i)),
			at.Add(time.Duration(i/2)*time.Minute), usd(1), nil, opus)
	}
	q := UsageRowsQuery{From: at.Add(-time.Hour), To: at.Add(time.Hour), Limit: 3}
	seen := map[string]bool{}
	pages := 0
	for {
		rows, next, err := s.UsageRows(ctx, q)
		require.NoError(t, err)
		pages++
		for _, r := range rows {
			assert.False(t, seen[r.TurnID], "row %s repeated", r.TurnID)
			seen[r.TurnID] = true
		}
		if next == "" {
			break
		}
		q.Cursor = next
	}
	assert.Len(t, seen, 7)
	assert.Equal(t, 3, pages)

	_, _, err := s.UsageRows(ctx, UsageRowsQuery{From: at, To: at.Add(time.Hour), Cursor: "nonsense"})
	assert.ErrorIs(t, err, ErrBadCursor)
}
