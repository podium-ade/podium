//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The usage queries bucket and sum by start time, so the fixtures need turns at chosen
// instants. CreateTurn always stamps now, which is exactly what these tests cannot use.
func insertTurn(t *testing.T, s *Store, id, sessionID, taskID string, started time.Time, cost *float64, numTurns *int32) {
	t.Helper()
	insertTurnOn(t, s, id, sessionID, taskID, started, cost, numTurns, Backend{})
}

// insertTurnOn is insertTurn with a recorded backend. An empty Backend writes nulls, which
// is what every turn from before the columns existed looks like.
func insertTurnOn(
	t *testing.T, s *Store, id, sessionID, taskID string, started time.Time,
	cost *float64, numTurns *int32, b Backend,
) {
	t.Helper()
	finished := started.Add(2 * time.Minute)
	_, err := s.pool.Exec(context.Background(),
		`insert into turns (id, session_id, task_id, trigger_ref, status, started_at, finished_at,
		                    num_turns, cost_usd, final_text, agent, model, effort, provider)
		 values ($1, $2, nullif($3, ''), 'ref', 'succeeded', $4, $5, $6, $7, 'done',
		         nullif($8, ''), nullif($9, ''), nullif($10, ''), nullif($11, ''))`,
		id, sessionID, taskID, started, finished, numTurns, cost, b.Agent, b.Model, b.Effort, b.Provider)
	require.NoError(t, err)
}

// insertDelegation is the other half of what this bot spends. A conversation is answered by
// the assistant on the host and hands the work to a container, so the delegation row is where
// a chat's money actually is — and the usage queries have to read it beside a turn's.
func insertDelegation(
	t *testing.T, s *Store, id, sessionID, turnID, taskID, playbook string, created time.Time,
	cost *float64, numTurns *int32, b Backend,
) {
	t.Helper()
	finished := created.Add(9 * time.Minute)
	_, err := s.pool.Exec(context.Background(),
		`insert into delegations (id, session_id, turn_id, trigger_ref, playbook, instruction,
		                          task_id, status, created_at, finished_at, num_turns, cost_usd,
		                          agent, model, effort, provider)
		 values ($1, $2, $3, 'ref', $4, 'do it', nullif($5, ''), 'succeeded', $6, $7, $8, $9,
		         nullif($10, ''), nullif($11, ''), nullif($12, ''), nullif($13, ''))`,
		id, sessionID, turnID, playbook, taskID, created, finished, numTurns, cost,
		b.Agent, b.Model, b.Effort, b.Provider)
	require.NoError(t, err)
}

func usd(v float64) *float64 { return &v }

func i32(v int32) *int32 { return &v }

func newUsageSession(t *testing.T, s *Store, key, playbook string) string {
	t.Helper()
	return newUsageSessionOf(t, s, "slack", key, playbook)
}

// newUsageSessionOf names the source, because a conversation and a thread are billed
// differently: a thread's spend is on its turns, and a chat's is on what it delegated.
func newUsageSessionOf(t *testing.T, s *Store, kind, key, playbook string) string {
	t.Helper()
	sess, err := s.UpsertSession(context.Background(), Session{
		SourceKind: kind, SourceKey: key, Profile: "podium", Playbook: playbook,
	})
	require.NoError(t, err)
	return sess.ID
}

func TestUsageBucketsByDayAndSumsTotals(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:1.1", "triage")

	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC) }
	four := int32(4)
	insertTurn(t, s, "turn_a", sess, "task_a", day(3, 10), usd(0.01), &four)
	insertTurn(t, s, "turn_b", sess, "task_b", day(3, 14), usd(0.02), &four)
	insertTurn(t, s, "turn_c", sess, "task_c", day(5, 9), usd(0.50), &four)
	// A turn whose runtime never reported a cost. It is a turn that happened, so it counts.
	insertTurn(t, s, "turn_d", sess, "task_d", day(5, 11), nil, nil)

	u, err := s.Usage(ctx, UsageQuery{From: day(1, 0), To: day(30, 0), Limit: 100})
	require.NoError(t, err)

	require.Len(t, u.Days, 2, "only the days something ran on")
	assert.Equal(t, "2026-09-03", u.Days[0].Date, "days are ascending")
	assert.InDelta(t, 0.03, u.Days[0].CostUSD, 1e-9)
	assert.Equal(t, 2, u.Days[0].Turns)
	assert.Equal(t, 8, u.Days[0].ModelTurns)
	assert.Equal(t, 0, u.Days[0].Unpriced)

	assert.Equal(t, "2026-09-05", u.Days[1].Date)
	assert.InDelta(t, 0.50, u.Days[1].CostUSD, 1e-9)
	assert.Equal(t, 1, u.Days[1].Unpriced, "the turn with no cost is counted, not dropped")

	assert.InDelta(t, 0.53, u.TotalCostUSD, 1e-9)
	assert.Equal(t, 4, u.TotalTurns)
	assert.Equal(t, 12, u.TotalModelTurns)
	assert.Equal(t, 1, u.Unpriced)
}

// The whole point of the offset: a turn late in the evening must land on the day the
// operator ran it, not on the server's next one.
func TestUsageBucketsInTheCallersTimeZone(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:2.2", "triage")

	// 02:00 UTC on the 4th is 22:00 on the 3rd in New York.
	late := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	insertTurn(t, s, "turn_late", sess, "task_late", late, usd(0.10), nil)

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	utc, err := s.Usage(ctx, UsageQuery{From: from, To: to, TZOffsetMinutes: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, utc.Days, 1)
	assert.Equal(t, "2026-09-04", utc.Days[0].Date)

	ny, err := s.Usage(ctx, UsageQuery{From: from, To: to, TZOffsetMinutes: -300, Limit: 10})
	require.NoError(t, err)
	require.Len(t, ny.Days, 1)
	assert.Equal(t, "2026-09-03", ny.Days[0].Date, "an operator in New York ran it on the 3rd")

	// Tokyo is the other direction: 02:00 UTC on the 4th is 11:00 on the 4th.
	tokyo, err := s.Usage(ctx, UsageQuery{From: from, To: to, TZOffsetMinutes: 540, Limit: 10})
	require.NoError(t, err)
	require.Len(t, tokyo.Days, 1)
	assert.Equal(t, "2026-09-04", tokyo.Days[0].Date)
}

func TestUsageRangeIsHalfOpen(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:3.3", "triage")

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	insertTurn(t, s, "turn_before", sess, "task_before", from.Add(-time.Second), usd(1), nil)
	insertTurn(t, s, "turn_at_from", sess, "task_at_from", from, usd(2), nil)
	insertTurn(t, s, "turn_at_to", sess, "task_at_to", to, usd(4), nil)

	u, err := s.Usage(ctx, UsageQuery{From: from, To: to, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, u.TotalTurns, "from is inclusive and to is exclusive")
	assert.InDelta(t, 2.0, u.TotalCostUSD, 1e-9)
}

func TestUsageCostsCarrySessionFieldsAndAreNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	triage := newUsageSession(t, s, "slack:C1:4.4", "triage")
	coder := newUsageSession(t, s, "slack:C2:5.5", "coder")

	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	insertTurn(t, s, "turn_old", triage, "task_old", day(2), usd(0.01), nil)
	insertTurn(t, s, "turn_new", coder, "task_new", day(6), usd(0.02), nil)

	u, err := s.Usage(ctx, UsageQuery{
		From:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, u.Costs, 2)

	assert.Equal(t, "turn_new", u.Costs[0].TurnID, "newest first")
	assert.Equal(t, "task_new", u.Costs[0].TaskID)
	assert.Equal(t, "coder", u.Costs[0].Playbook, "the session's playbook comes back on the turn")
	assert.Equal(t, "slack", u.Costs[0].SourceKind)
	assert.Equal(t, "slack:C2:5.5", u.Costs[0].SourceKey)
	assert.Equal(t, "podium", u.Costs[0].Profile)
	require.NotNil(t, u.Costs[0].CostUSD)
	assert.InDelta(t, 0.02, *u.Costs[0].CostUSD, 1e-9)
	assert.Equal(t, "turn_old", u.Costs[1].TurnID)
}

// The totals are summed from the day rows, not from Costs, so capping the page must not
// change the bill. This is the invariant the UI relies on to show a truthful headline.
func TestUsageTotalsSurviveACappedCostsPage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:6.6", "triage")

	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for i := range 10 {
		insertTurn(t, s, "turn_"+string(rune('a'+i)), sess, "task_"+string(rune('a'+i)),
			base.Add(time.Duration(i)*time.Hour), usd(1), nil)
	}

	u, err := s.Usage(ctx, UsageQuery{
		From:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Limit: 3,
	})
	require.NoError(t, err)
	assert.Len(t, u.Costs, 3, "the page is capped")
	assert.Equal(t, 10, u.TotalTurns, "the total is not")
	assert.InDelta(t, 10.0, u.TotalCostUSD, 1e-9)
}

func TestUsageEmptyRangeReportsNothingRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Now().UTC()

	for _, tc := range []struct {
		name string
		q    UsageQuery
	}{
		{"identical ends", UsageQuery{From: now, To: now}},
		{"backwards", UsageQuery{From: now, To: now.Add(-time.Hour)}},
		{"zero value", UsageQuery{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := s.Usage(ctx, tc.q)
			require.NoError(t, err, "an empty range is a question with the answer 'nothing'")
			assert.Empty(t, u.Days)
			assert.Empty(t, u.Costs)
			assert.Zero(t, u.TotalCostUSD)
		})
	}
}

// A range wider than a year is narrowed from the far end, so the days the caller almost
// certainly meant — the recent ones — are the days that survive.
func TestUsageClampsAnOverlongRangeFromTheFarEnd(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:7.7", "triage")

	to := time.Now().UTC()
	recent := to.Add(-24 * time.Hour)
	ancient := to.Add(-2 * MaxUsageRange)
	insertTurn(t, s, "turn_recent", sess, "task_recent", recent, usd(1), nil)
	insertTurn(t, s, "turn_ancient", sess, "task_ancient", ancient, usd(9), nil)

	u, err := s.Usage(ctx, UsageQuery{From: to.Add(-5 * MaxUsageRange), To: to, Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, u.TotalTurns, "only the clamped window is read")
	assert.InDelta(t, 1.0, u.TotalCostUSD, 1e-9)
}

func TestUsageGroupsByWhatActuallyRanTheTurn(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:8.8", "triage")

	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Effort: "high", Provider: "anthropic"}
	sonnet := Backend{Agent: "claude", Model: "claude-sonnet-5", Provider: "anthropic"}
	grok := Backend{Agent: "grok", Model: "grok-4.6", Provider: "xai"}

	insertTurnOn(t, s, "turn_o1", sess, "task_o1", day(2), usd(3), nil, opus)
	insertTurnOn(t, s, "turn_o2", sess, "task_o2", day(3), usd(5), nil, opus)
	insertTurnOn(t, s, "turn_s1", sess, "task_s1", day(4), usd(1), nil, sonnet)
	insertTurnOn(t, s, "turn_g1", sess, "task_g1", day(5), usd(2), nil, grok)
	// A turn from before the columns existed, and one whose runtime reported no cost.
	insertTurn(t, s, "turn_old", sess, "task_old", day(6), usd(9), nil)
	insertTurnOn(t, s, "turn_g2", sess, "task_g2", day(7), nil, nil, grok)

	u, err := s.Usage(ctx, UsageQuery{
		From:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, u.Backends, 4, "opus, sonnet, grok and the unrecorded bucket")

	by := map[string]UsageBackend{}
	for _, b := range u.Backends {
		by[b.Model] = b
	}

	opusRow := by["claude-opus-5"]
	assert.InDelta(t, 8.0, opusRow.CostUSD, 1e-9, "both opus turns sum into one row")
	assert.Equal(t, 2, opusRow.Turns)
	assert.Equal(t, "high", opusRow.Effort)
	assert.Equal(t, "anthropic", opusRow.Provider)

	grokRow := by["grok-4.6"]
	assert.InDelta(t, 2.0, grokRow.CostUSD, 1e-9)
	assert.Equal(t, 2, grokRow.Turns, "the unpriced turn is still a turn")
	assert.Equal(t, 1, grokRow.Unpriced)
	assert.Equal(t, "xai", grokRow.Provider)
	assert.Empty(t, grokRow.Effort, "no effort recorded means the model's own default")

	// The pre-columns turn groups on its own, with every field empty. Its money is real.
	unrecorded := by[""]
	assert.Empty(t, unrecorded.Provider)
	assert.Empty(t, unrecorded.Agent)
	assert.InDelta(t, 9.0, unrecorded.CostUSD, 1e-9)

	// Grouping must not lose or invent money: the rows add up to the range's total.
	var summed float64
	for _, b := range u.Backends {
		summed += b.CostUSD
	}
	assert.InDelta(t, u.TotalCostUSD, summed, 1e-9)
}

// The same invariant the day rows have: grouping is a server-side aggregate over the whole
// range, so capping the costs page cannot change what a model is reported to have cost.
func TestUsageBackendsSurviveACappedCostsPage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:9.9", "triage")
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}

	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for i := range 10 {
		insertTurnOn(t, s, "turn_"+string(rune('a'+i)), sess, "task_"+string(rune('a'+i)),
			base.Add(time.Duration(i)*time.Hour), usd(1), nil, opus)
	}

	u, err := s.Usage(ctx, UsageQuery{
		From:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Limit: 3,
	})
	require.NoError(t, err)
	assert.Len(t, u.Costs, 3, "the page is capped")
	require.Len(t, u.Backends, 1)
	assert.Equal(t, 10, u.Backends[0].Turns, "the grouping is not")
	assert.InDelta(t, 10.0, u.Backends[0].CostUSD, 1e-9)
}

// The bug this pins: the screen needs the window before the range to compute a "vs" figure,
// and widening From to fetch it folded that window into the backend grouping and the totals
// — a week of models reported as a fortnight's. CompareFrom widens the day rows only.
func TestUsageCompareFromWidensTheDayRowsAndNothingElse(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:10.10", "triage")
	opus := Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}

	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	// Two turns inside the range, one in the window before it.
	insertTurnOn(t, s, "turn_in1", sess, "task_in1", day(10), usd(2), nil, opus)
	insertTurnOn(t, s, "turn_in2", sess, "task_in2", day(11), usd(3), nil, opus)
	insertTurnOn(t, s, "turn_before", sess, "task_before", day(4), usd(50), nil, opus)

	u, err := s.Usage(ctx, UsageQuery{
		From:        day(8),
		To:          day(14),
		CompareFrom: day(1),
		Limit:       100,
	})
	require.NoError(t, err)

	require.Len(t, u.Backends, 1)
	assert.InDelta(t, 5.0, u.Backends[0].CostUSD, 1e-9, "the grouping is the range, not the comparison")
	assert.Equal(t, 2, u.Backends[0].Turns)
	assert.InDelta(t, 5.0, u.TotalCostUSD, 1e-9, "and so are the totals")
	assert.Equal(t, 2, u.TotalTurns)
	assert.Len(t, u.Costs, 2, "and so is the costs page")

	// The day rows alone reach back, which is what the comparison is computed from.
	assert.Len(t, u.Days, 3, "two days in the range and one before it")
	var earliest string
	for _, d := range u.Days {
		if earliest == "" || d.Date < earliest {
			earliest = d.Date
		}
	}
	assert.Equal(t, "2026-09-04", earliest)
}

// A CompareFrom inside the range, or absent, changes nothing.
func TestUsageIgnoresACompareFromThatDoesNotReachBack(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := newUsageSession(t, s, "slack:C1:11.11", "triage")
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	insertTurn(t, s, "turn_x", sess, "task_x", day(10), usd(1), nil)

	base := UsageQuery{From: day(8), To: day(14), Limit: 10}
	for _, tc := range []struct {
		name string
		q    UsageQuery
	}{
		{"unset", base},
		{"inside the range", func() UsageQuery { q := base; q.CompareFrom = day(9); return q }()},
		{"after the range", func() UsageQuery { q := base; q.CompareFrom = day(20); return q }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := s.Usage(ctx, tc.q)
			require.NoError(t, err)
			assert.Len(t, u.Days, 1)
			assert.InDelta(t, 1.0, u.TotalCostUSD, 1e-9)
		})
	}
}

// TestUsageCountsWhatADelegatedTaskSpent is the hole this closed. The conductor received each
// delegated task's accounting message and dropped it, at Debug level, which is off — so the
// expensive half of a conversation was not merely unattributed, it was gone. Every usage read
// has to see both halves.
func TestUsageCountsWhatADelegatedTaskSpent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// A CONVERSATION: its session records no playbook, because the assistant answers it.
	chat := newUsageSessionOf(t, s, "chat", "chat:chat_1", "")
	day := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	// What the assistant cost to relay, and what the container it started actually cost.
	insertTurnOn(t, s, "turn_1", chat, "", day, usd(0.02), i32(1),
		Backend{Agent: "grok", Model: "grok-4.6", Effort: "low", Provider: "xai"})
	// A delegation belongs to a real turn — the row has a foreign key to it — which is the
	// turn the assistant was running when it handed the work over.
	insertDelegation(t, s, "dlg_1", chat, "turn_1", "task_1", "podium", day.Add(time.Minute), usd(4.50), i32(180),
		Backend{Agent: "grok", Model: "grok-4.6", Effort: "high", Provider: "xai"})

	u, err := s.Usage(ctx, UsageQuery{From: day.Add(-time.Hour), To: day.Add(time.Hour), Limit: 50})
	require.NoError(t, err)

	// The total is both, which is the whole point: reading turns alone reported 0.02.
	assert.InDelta(t, 4.52, u.TotalCostUSD, 1e-9)
	assert.Equal(t, 2, u.TotalTurns)
	assert.Equal(t, 181, u.TotalModelTurns)

	// One day row, summing both.
	require.Len(t, u.Days, 1)
	assert.InDelta(t, 4.52, u.Days[0].CostUSD, 1e-9)

	// One cost row each, and the delegated one is credited to its OWN playbook rather than to
	// the conversation's session, which runs none.
	require.Len(t, u.Costs, 2)
	byID := map[string]TurnCost{}
	for _, c := range u.Costs {
		byID[c.TurnID] = c
	}
	assert.Empty(t, byID["turn_1"].Playbook, "the assistant is not a playbook")
	assert.Equal(t, "podium", byID["dlg_1"].Playbook)
	assert.Equal(t, "task_1", byID["dlg_1"].TaskID)
	assert.Equal(t, "chat", byID["dlg_1"].SourceKind, "it belongs to the conversation that asked")

	// And the breakdown groups them apart by effort, because that is what each ran on.
	byEffort := map[string]UsageBackend{}
	for _, b := range u.Backends {
		byEffort[b.Effort] = b
	}
	assert.InDelta(t, 0.02, byEffort["low"].CostUSD, 1e-9)
	assert.InDelta(t, 4.50, byEffort["high"].CostUSD, 1e-9)
}
