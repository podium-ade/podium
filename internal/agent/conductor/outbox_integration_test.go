//go:build integration

package conductor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// The delivery outbox (outbox.go): an answer in a conversation that lives somewhere else is
// recorded in the web chat first and then owed to the surface, so a post the surface refuses
// is retried instead of being a log line nobody in the thread ever sees.

// mirroredTurn runs one turn of a mirrored dev conversation that answers "the answer." and
// returns the chat and the answer's row.
func mirroredTurn(
	t *testing.T, st *store.Store, src *fakesource.Source, tweak func(*conductor.Options),
) (*running, store.Chat, store.ChatMessage) {
	t.Helper()
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "the answer.")}
	}
	r := startWith(t, st, fake, src, tweak)
	ctx := context.Background()
	ev := inbound("C1/1.1", "what is it?")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	return r, chat, answerRow(t, st, chat.ID)
}

func answerRow(t *testing.T, st *store.Store, chatID string) store.ChatMessage {
	t.Helper()
	msgs, err := st.ListChatMessages(context.Background(), chatID, 0)
	require.NoError(t, err)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == store.RoleAssistant {
			return msgs[i]
		}
	}
	t.Fatalf("no answer in chat %s: %+v", chatID, msgs)
	return store.ChatMessage{}
}

func atOnce(o *conductor.Options) { o.OutboxRetryDelay = -1 }

func TestAnAnswerTheSurfaceRefusedIsRetriedUntilItLands(t *testing.T) {
	st := newStore(t)
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	src.FailPosts(1)

	r, chat, answer := mirroredTurn(t, st, src, atOnce)

	// The web chat has the answer even though the thread does not, and says why.
	assert.Equal(t, "the answer.", answer.Text, "the record is written before the surface is tried")
	require.Len(t, answer.Deliveries, 1)
	assert.Equal(t, store.OutboxPending, answer.Deliveries[0].Status)
	assert.Equal(t, 1, answer.Deliveries[0].Attempts)
	assert.Contains(t, answer.Deliveries[0].LastError, "refused")
	assert.Empty(t, posts(src.Records(), conductor.OutFinal), "the surface refused the first attempt")

	ctx := context.Background()
	waitFor(t, 10*time.Second, "the retry to land", func() bool {
		r.cond.DeliverDueForTest(ctx)
		a := answerRow(t, st, chat.ID)
		return len(a.Deliveries) == 1 && a.Deliveries[0].Status == store.OutboxSent
	})
	finals := posts(src.Records(), conductor.OutFinal)
	require.Len(t, finals, 1, "the retry posts the answer once")
	assert.Equal(t, "the answer.", finals[0].Text)
	assert.NotEmpty(t, answerRow(t, st, chat.ID).ExternalID, "where it landed is recorded")
}

func TestADeliveryThatKeepsFailingIsShownAsFailedUntilSomebodyRetries(t *testing.T) {
	st := newStore(t)
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	src.FailPosts(1000)

	r, chat, answer := mirroredTurn(t, st, src, atOnce)
	ctx := context.Background()

	waitFor(t, 20*time.Second, "the outbox to give up", func() bool {
		r.cond.DeliverDueForTest(ctx)
		a := answerRow(t, st, chat.ID)
		return len(a.Deliveries) == 1 && a.Deliveries[0].Status == store.OutboxFailed
	})
	failed := answerRow(t, st, chat.ID).Deliveries[0]
	assert.Equal(t, 6, failed.Attempts, "a delivery is given up on after its attempt budget")
	// A failed row is nobody's work any more: the worker leaves it alone.
	assert.Zero(t, r.cond.DeliverDueForTest(ctx), "a failed delivery is not claimed again on its own")

	// Somebody presses Retry once the surface is back.
	src.FailPosts(0)
	require.NoError(t, r.cond.RetryDelivery(ctx, chat.ID, answer.Seq))
	waitFor(t, 10*time.Second, "the retried delivery to land", func() bool {
		r.cond.DeliverDueForTest(ctx)
		a := answerRow(t, st, chat.ID)
		return a.Deliveries[0].Status == store.OutboxSent
	})
	require.Len(t, posts(src.Records(), conductor.OutFinal), 1)

	// Nothing failed is left, so a second Retry has nothing to do and says so.
	assert.ErrorIs(t, r.cond.RetryDelivery(ctx, chat.ID, answer.Seq), conductor.ErrNothingToRetry)
}

// Two conductors working the same outbox take disjoint rows: SKIP LOCKED is what makes a
// delivery attempted by one of them, never both.
func TestTwoConductorsClaimDisjointDeliveries(t *testing.T) {
	a, b := newStorePair(t)
	ctx := context.Background()
	chat, err := a.CreateMirrorChat(ctx, "slack:C1:2.2", store.OriginSlack, "alice", "t")
	require.NoError(t, err)
	msg, err := a.AppendChatMessage(ctx, store.ChatMessage{ChatID: chat.ID, Role: store.RoleAssistant, Text: "x", Via: store.ViaBot})
	require.NoError(t, err)
	const n = 40
	for range n {
		_, err := a.EnqueueOutbox(ctx, chat.ID, msg.Seq, "slack", "C1/2.2", store.OutboxPayload{Type: conductor.OutFinal, Text: "x"},
			time.Now().Add(-time.Minute))
		require.NoError(t, err)
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for _, st := range []*store.Store{a, b, a, b} {
		wg.Add(1)
		go func(st *store.Store) {
			defer wg.Done()
			for {
				items, err := st.ClaimOutbox(ctx, 3, time.Minute)
				if !assert.NoError(t, err) || len(items) == 0 {
					return
				}
				mu.Lock()
				for _, it := range items {
					seen[it.ID]++
				}
				mu.Unlock()
			}
		}(st)
	}
	wg.Wait()
	require.Len(t, seen, n, "every due delivery was claimed")
	for id, times := range seen {
		assert.Equal(t, 1, times, "delivery %s was claimed by two conductors", id)
	}
	// A claimed row is held for its lease: claiming again finds nothing due.
	again, err := b.ClaimOutbox(ctx, n, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again)
}

// A turn records whose it was, and usage reads it back per person. A delegated task's spend
// belongs to whoever started the turn that delegated it.
func TestUsageIsBrokenDownByWhoseTurnItWas(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sess, err := st.UpsertSession(ctx, store.Session{SourceKind: "slack", SourceKey: "slack:C1:3.3", Profile: "default"})
	require.NoError(t, err)
	cost := func(v float64) *float64 { return &v }
	one := 1
	mine, err := st.CreateTurnFor(ctx, sess.ID, "C1/3.3/3.3", "dana@acme.com", store.Backend{})
	require.NoError(t, err)
	require.NoError(t, st.FinishTurn(ctx, mine.ID, store.TurnSucceeded, &one, cost(0.5), ""))
	app, err := st.CreateTurnFor(ctx, sess.ID, "C1/3.3/3.4", store.TurnStartedByApp, store.Backend{})
	require.NoError(t, err)
	require.NoError(t, st.FinishTurn(ctx, app.ID, store.TurnSucceeded, &one, cost(0.25), ""))

	u, err := st.Usage(ctx, store.UsageQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	byWho := map[string]float64{}
	for _, p := range u.People {
		byWho[p.StartedBy] = p.CostUSD
	}
	assert.InDelta(t, 0.5, byWho["dana@acme.com"], 1e-9)
	assert.InDelta(t, 0.25, byWho[store.TurnStartedByApp], 1e-9)
	for _, c := range u.Costs {
		if c.TurnID == mine.ID {
			assert.Equal(t, "dana@acme.com", c.StartedBy)
		}
	}
	got, err := st.GetTurn(ctx, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, "dana@acme.com", got.StartedBy)
}

// A Slack turn by a resolved speaker records that speaker as whose turn it was.
func TestASlackTurnRecordsWhoStartedIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()
	ev := asSlackSpeaker(inbound("C1/4.4", "hi"))
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		s := turnStatus(st, ev.SourceKey)
		return s == store.TurnSucceeded || s == store.TurnFailed
	})
	turn := turnOf(t, st, ev.SourceKey)
	assert.Equal(t, ev.Login, turn.StartedBy)
}

// A turn reads the record, not the source: what was said in Podium beside the thread is in
// the transcript, labelled, and what was deleted on the surface is not.
func TestATurnReadsTheRecordWithEveryoneLabelled(t *testing.T) {
	st := newStore(t)
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	r, chat, _ := mirroredTurn(t, st, src, nil)
	ctx := context.Background()

	_, err := st.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chat.ID, Role: store.RoleUser, Text: "said in podium", Author: "Alvaro",
		AuthorLogin: "alvaro@acme.com", Via: store.ViaWeb, Addressed: false,
	})
	require.NoError(t, err)
	_, _, err = st.RecordChatMessage(ctx, store.ChatMessage{
		ChatID: chat.ID, Role: store.RoleUser, Text: "said in the thread", Author: "Dana",
		Via: store.ViaSlack, ExternalID: "9.1",
	})
	require.NoError(t, err)
	_, _, err = st.RecordChatMessage(ctx, store.ChatMessage{
		ChatID: chat.ID, Role: store.RoleUser, Text: "regretted", Author: "Dana",
		Via: store.ViaSlack, ExternalID: "9.2",
	})
	require.NoError(t, err)
	_, err = st.DeleteChatMessage(ctx, chat.ID, store.ViaSlack, "9.2", time.Now())
	require.NoError(t, err)
	// The source's own transcript says something else; the record wins.
	src.Heard("C1/1.1", conductor.BriefEntry{Role: conductor.RoleUser, Author: "x", Text: "only in the source"})

	sess, err := st.GetSessionByKey(ctx, chat.SourceKey)
	require.NoError(t, err)
	entries, err := r.cond.TurnTranscriptForTest(ctx, src, "C1/1.1", sess)
	require.NoError(t, err)
	var got []string
	for _, e := range entries {
		got = append(got, e.Author+": "+e.Text)
	}
	assert.Equal(t, []string{
		"alice (dev, not in Podium): what is it?",
		"Podium: the answer.",
		"Alvaro: said in podium",
		"Dana (slack, not in Podium): said in the thread",
	}, got)
}
