//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// asker is inbound() said by a particular Podium person.
func asker(ref, text, login string) conductor.InboundEvent {
	ev := inbound(ref, text)
	ev.Login = login
	ev.Author = login
	return ev
}

// startedBy is who each of a session's turns ran as, oldest first.
func startedBy(t *testing.T, st *store.Store, sourceKey string) []string {
	t.Helper()
	ctx := context.Background()
	sess, err := st.GetSessionByKey(ctx, sourceKey)
	if err != nil {
		return nil
	}
	turns, err := st.ListTurns(ctx, sess.ID, 50)
	require.NoError(t, err)
	out := make([]string, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		out = append(out, turns[i].StartedBy)
	}
	return out
}

func queued(t *testing.T, st *store.Store, sourceKey string) int {
	t.Helper()
	sess, err := st.GetSessionByKey(context.Background(), sourceKey)
	if err != nil {
		return 0
	}
	n, err := st.CountQueuedTurns(context.Background(), sess.ID)
	require.NoError(t, err)
	return n
}

// TestASecondPersonsMessageIsATurnOfTheirOwn: while alice's turn is up (its task answered and
// is warm), bob tags Podium in the same conversation. Bob's message is NOT injected into
// alice's task — it runs with her credentials and answers her — it waits in the queue and
// becomes a turn of its own, run as bob, once hers has ended.
func TestASecondPersonsMessageIsATurnOfTheirOwn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer from "+taskID)}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, nil)
	key := conductor.KindDev + ":C1:1.1"

	require.NoError(t, src.Send(ctx, asker("C1/1.1", "first", "alice@podium.test")))
	waitFor(t, 30*time.Second, "alice's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	require.NoError(t, src.Send(ctx, asker("C1/1.1", "and mine", "bob@podium.test")))
	waitFor(t, 30*time.Second, "bob's message to be queued", func() bool { return queued(t, st, key) == 1 })
	assert.Empty(t, fake.Injects(), "bob's words never go into alice's task")
	assert.Len(t, fake.Specs(), 1)

	release()
	waitFor(t, 60*time.Second, "bob's own turn", func() bool {
		return len(startedBy(t, st, key)) == 2 && turnStatus(st, key) == store.TurnSucceeded
	})
	assert.Equal(t, []string{"alice@podium.test", "bob@podium.test"}, startedBy(t, st, key),
		"each turn runs as the person who asked it, in the order they asked")
	assert.Len(t, fake.Specs(), 2)
	assert.Zero(t, queued(t, st, key))
}

// TestAQueuedMirroredMessageNamesItsPlaceInTheRecord: a turn queued from a mirrored
// conversation (Slack, GitHub) carries the chat and message it is, so the chat lists it under
// its asker and the asker can take it back. It used to be queued with no chat and seq 0.
func TestAQueuedMirroredMessageNamesItsPlaceInTheRecord(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	defer release()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer from "+taskID)}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, nil)
	key := conductor.KindDev + ":C1:1.1"

	require.NoError(t, src.Send(ctx, asker("C1/1.1", "first", "alice@podium.test")))
	waitFor(t, 30*time.Second, "alice's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	bob := asker("C1/1.1", "and mine", "bob@podium.test")
	bob.ExternalID = "2.2"
	require.NoError(t, src.Send(ctx, bob))
	waitFor(t, 30*time.Second, "bob's message to be queued", func() bool { return queued(t, st, key) == 1 })

	chat, err := st.ChatBySourceKey(ctx, key)
	require.NoError(t, err)
	q, err := st.ListQueuedTurnsForChat(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, q, 1, "the queued turn is listed under its chat")
	msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	var bobSeq uint64
	for _, m := range msgs {
		if m.ExternalID == "2.2" {
			bobSeq = m.Seq
		}
	}
	require.NotZero(t, bobSeq)
	assert.Equal(t, bobSeq, q[0].MessageSeq, "it names bob's message in the record")
}

// TestOnePersonsRunOfMessagesIsStillOneTurn: what one person says while their own turn is
// answering goes into that turn, as it always did, and nothing queues.
func TestOnePersonsRunOfMessagesIsStillOneTurn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, nil)
	key := conductor.KindDev + ":C1:1.1"

	require.NoError(t, src.Send(ctx, asker("C1/1.1", "first", "alice@podium.test")))
	waitFor(t, 30*time.Second, "the answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	for _, text := range []string{"also", "and this"} {
		require.NoError(t, src.Send(ctx, asker("C1/1.1", text, "alice@podium.test")))
	}
	waitFor(t, 30*time.Second, "both follow-ups in her task", func() bool { return len(fake.Injects()) == 2 })
	assert.Zero(t, queued(t, st, key))
	release()
	waitFor(t, 60*time.Second, "the turn to finish", func() bool { return turnStatus(st, key) == store.TurnSucceeded })
	assert.Equal(t, []string{"alice@podium.test"}, startedBy(t, st, key))
}

// TestAQueuedTurnCrossesConductors: two conductors drain one source. Whichever hears bob's
// message while the other serves alice's turn queues it in the database, and the owner runs
// it after hers — exactly once, as bob.
func TestAQueuedTurnCrossesConductors(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer from "+taskID)}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, replica("a", time.Minute))
	startWith(t, st, fake, src, replica("b", time.Minute))
	key := conductor.KindDev + ":C1:1.1"

	require.NoError(t, src.Send(ctx, asker("C1/1.1", "first", "alice@podium.test")))
	waitFor(t, 30*time.Second, "alice's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	// One after the other: two conductors hearing two messages at the same instant queue them
	// in whichever order their writes land, and that order is the queue's.
	for i, who := range []string{"bob@podium.test", "carol@podium.test"} {
		require.NoError(t, src.Send(ctx, asker("C1/1.1", "from "+who, who)))
		waitFor(t, 30*time.Second, who+" to be queued", func() bool { return queued(t, st, key) == i+1 })
	}

	release()
	waitFor(t, 90*time.Second, "every queued turn to run", func() bool {
		return len(startedBy(t, st, key)) == 3 && turnStatus(st, key) == store.TurnSucceeded &&
			queued(t, st, key) == 0
	})
	assert.Equal(t, []string{"alice@podium.test", "bob@podium.test", "carol@podium.test"}, startedBy(t, st, key))
	assert.Len(t, fake.Specs(), 3, "one task per turn, whichever conductor ran it")
	assert.Empty(t, fake.Injects())
}

// TestAQueueOutlivesItsConductor: the conductor serving a conversation dies with a turn
// queued behind the one it was running. The queue is in the database, so the conductor that
// adopts the conversation runs it too.
func TestAQueueOutlivesItsConductor(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer from "+taskID)}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	key := conductor.KindDev + ":C1:1.1"

	dying := startWith(t, st, fake, src, replica("dying", 600*time.Millisecond))
	require.NoError(t, src.Send(ctx, asker("C1/1.1", "first", "alice@podium.test")))
	waitFor(t, 30*time.Second, "alice's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	require.NoError(t, src.Send(ctx, asker("C1/1.1", "mine", "bob@podium.test")))
	waitFor(t, 30*time.Second, "bob's turn to be queued", func() bool { return queued(t, st, key) == 1 })
	dying.stop()

	survivor := fakesource.New(conductor.KindDev)
	t.Cleanup(survivor.Close)
	startWith(t, st, fake, survivor, replica("survivor", 600*time.Millisecond))
	release()
	waitFor(t, 60*time.Second, "bob's turn on the survivor", func() bool {
		by := startedBy(t, st, key)
		return len(by) == 2 && by[1] == "bob@podium.test" && queued(t, st, key) == 0
	})
}

// TestTakingBackAQueuedTurnIsOnlyForTheAsker: the store refuses anybody else's.
func TestTakingBackAQueuedTurnIsOnlyForTheAsker(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	c, err := st.CreateChat(ctx, "alice@podium.test", "t")
	require.NoError(t, err)
	sess, err := st.UpsertSession(ctx, store.Session{SourceKind: "chat", SourceKey: c.SourceKey, Profile: "podium"})
	require.NoError(t, err)
	_, err = st.EnqueueTurn(ctx, store.QueuedTurn{
		SessionID: sess.ID, SourceKind: "chat", Login: "bob@podium.test", ChatID: c.ID, MessageSeq: 4, Event: []byte(`{}`),
	})
	require.NoError(t, err)

	err = st.RemoveQueuedTurn(ctx, c.ID, 4, "alice@podium.test")
	assert.ErrorIs(t, err, store.ErrNotFound, "not even the chat's owner takes back somebody else's turn")
	q, err := st.ListQueuedTurnsForChat(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, q, 1)

	require.NoError(t, st.RemoveQueuedTurn(ctx, c.ID, 4, "bob@podium.test"))
	q, err = st.ListQueuedTurnsForChat(ctx, c.ID)
	require.NoError(t, err)
	assert.Empty(t, q)
}

// TestAWebMessageInAMirroredThreadStaysInTheWeb: somebody writes a Slack thread's chat from
// the web and tags Podium. Whatever answers is the web chat's — nothing is posted to the
// Slack thread, and nothing is owed to it in the outbox (decision 18).
func TestAWebMessageInAMirroredThreadStaysInTheWeb(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	slack := fakesource.New(conductor.SourceSlack)
	t.Cleanup(slack.Close)
	web, err := chat.New(chat.Options{Store: st, DisplayName: "Podium"})
	require.NoError(t, err)
	ctx := context.Background()

	c, err := st.CreateMirrorChat(ctx, "slack:C1:1.1", store.OriginSlack, "alice", "in slack")
	require.NoError(t, err)
	_, err = st.AddChatParticipant(ctx, c.ID, testSpeaker, store.JoinedSlack, time.Now())
	require.NoError(t, err)
	_, err = st.ClaimChatOwner(ctx, c.ID, testSpeaker)
	require.NoError(t, err)

	startWith(t, st, fake, slack, func(o *conductor.Options) {
		o.Sources = append(o.Sources, web)
		o.Mirror = web
	})

	res, err := web.SendMessage(ctx, chat.SendRequest{ChatID: c.ID, Login: testSpeaker, Text: "@Podium what did we decide?"})
	require.NoError(t, err)
	require.True(t, res.Addressed)

	// This conductor has no host runtime, so the web chat's turn is refused — in the web
	// chat, which is the point: whatever the outcome, it is said there.
	waitFor(t, 30*time.Second, "the answer in the web chat", func() bool {
		msgs, err := st.ListChatMessages(ctx, c.ID, 0)
		require.NoError(t, err)
		for _, m := range msgs {
			if m.Role == store.RoleAssistant {
				return true
			}
		}
		return false
	})
	assert.Empty(t, posts(slack.Records(), conductor.OutFailure), "nothing reaches the Slack thread")
	assert.Empty(t, posts(slack.Records(), conductor.OutFinal))
	msgs, err := st.ListChatMessages(ctx, c.ID, 0)
	require.NoError(t, err)
	for _, m := range msgs {
		assert.Empty(t, m.Deliveries, "nothing is owed to Slack for seq %d", m.Seq)
	}
}

// TestTheConductorSaysExactlyWhereAMessageWent: an event that asks (InboundEvent.Placed) is
// told how many turns run before it, read back from the queue: behind a running turn the next
// person is one back and the one after two. The web chat's send answers with this.
func TestTheConductorSaysExactlyWhereAMessageWent(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })
	key := conductor.KindDev + ":C1:1.1"

	hold := asker("C1/1.1", "a long one", "alice@podium.test")
	hold.Env = hostEnv(map[string]string{hostFakeHangEnv: "1"})
	require.NoError(t, src.Send(ctx, hold))
	waitFor(t, 30*time.Second, "alice's turn to be running", func() bool {
		return turnStatus(st, key) == store.TurnRunning
	})

	where := make(chan int, 2)
	for i, who := range []string{"bob@podium.test", "carol@podium.test"} {
		ev := asker("C1/1.1", "mine", who)
		ev.Env = hostEnv(nil)
		ev.Placed = func(n int) { where <- n }
		require.NoError(t, src.Send(ctx, ev))
		select {
		case n := <-where:
			assert.Equal(t, i+1, n, "%s: the running turn and everybody queued before them", who)
		case <-time.After(10 * time.Second):
			t.Fatalf("the conductor never said where %s's message went", who)
		}
	}
}
