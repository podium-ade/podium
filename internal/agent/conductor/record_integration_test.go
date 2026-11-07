//go:build integration

package conductor_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// The record is the dedupe (multiplayer sessions, docs/agent.md): a message that
// arrives twice — a Slack retry, a GitHub redelivery — is one row and one turn.
func TestARetriedMessageIsOneRowAndOneTurn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "once.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)

	ctx := context.Background()
	ev := inbound("C1/1.1", "do it")
	ev.ExternalID = "1.1"
	require.NoError(t, src.Send(ctx, ev))
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	time.Sleep(500 * time.Millisecond) // give a second turn every chance to start

	assert.Len(t, fake.Specs(), 1, "the retry started nothing")
	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	var users int
	for _, m := range msgs {
		if m.Role == store.RoleUser {
			users++
			assert.Equal(t, "1.1", m.ExternalID)
			assert.True(t, m.Addressed)
		}
	}
	assert.Equal(t, 1, users, "one message, one row")
}

// A source that names its conversation names the chat — a pull request's number, repository
// and title rather than "alice opened https://…" — and renames it when the name changes. A
// shorter name the title already starts with (a Slack mention knows no PR title) does not
// undo it, and a title somebody set in Podium is never overwritten.
func TestASourceNamesItsMirroredChat(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "done.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()

	title := func(key string) string {
		c, err := st.ChatBySourceKey(ctx, key)
		if err != nil {
			return ""
		}
		return c.Title
	}
	say := func(ext, name string, record bool) conductor.InboundEvent {
		ev := inbound("C1/1.1", "alice opened https://github.com/acme/api/pull/12 for review")
		ev.ExternalID = ext
		ev.Title = name
		ev.RecordOnly = record
		require.NoError(t, src.Send(ctx, ev))
		return ev
	}

	ev := say("1.1", "#12: Retry uploads", false)
	waitFor(t, 30*time.Second, "the chat", func() bool { return title(ev.SourceKey) == "#12: Retry uploads" })

	say("1.2", "#12", true)
	say("1.3", "#12: Retry uploads with backoff", true)
	waitFor(t, 10*time.Second, "the rename", func() bool {
		return title(ev.SourceKey) == "#12: Retry uploads with backoff"
	})

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	_, err = st.RenameChat(ctx, chat.ID, "alice", "Upload fix", true)
	require.NoError(t, err)
	say("1.4", "#12: Something else", true)
	waitFor(t, 10*time.Second, "the record", func() bool {
		msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
		return err == nil && len(msgs) > 0 && msgs[len(msgs)-1].ExternalID == "1.4"
	})
	assert.Equal(t, "Upload fix", title(ev.SourceKey), "a name somebody chose is theirs")
}

// Chatter nobody put to Podium is recorded into a conversation Podium holds, never into one
// it does not, and never starts a turn.
func TestUnaddressedChatterIsRecordedOnlyWherePodiumIs(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()

	stray := inbound("C9/9.9", "lunch?")
	stray.RecordOnly = true
	stray.ExternalID = "9.9"
	require.NoError(t, src.Send(ctx, stray))

	ask := inbound("C1/1.1", "what is this?")
	ask.ExternalID = "1.1"
	require.NoError(t, src.Send(ctx, ask))
	waitFor(t, 30*time.Second, "the turn", func() bool {
		return turnStatus(st, ask.SourceKey) == store.TurnSucceeded
	})

	chatter := inbound("C1/1.1", "thanks, that helps")
	chatter.Author = "bob"
	chatter.RecordOnly = true
	chatter.ExternalID = "1.2"
	require.NoError(t, src.Send(ctx, chatter))
	thread, err := st.ChatBySourceKey(ctx, ask.SourceKey)
	require.NoError(t, err)
	waitFor(t, 10*time.Second, "the chatter recorded", func() bool {
		msgs, err := st.ListChatMessages(ctx, thread.ID, 0)
		return err == nil && len(msgs) > 0 && msgs[len(msgs)-1].ExternalID == "1.2"
	})
	msgs, err := st.ListChatMessages(ctx, thread.ID, 0)
	require.NoError(t, err)
	last := msgs[len(msgs)-1]
	assert.False(t, last.Addressed)
	assert.Equal(t, "bob", last.Author)

	time.Sleep(300 * time.Millisecond)
	assert.Len(t, fake.Specs(), 1, "chatter started nothing")
	_, err = st.ChatBySourceKey(ctx, stray.SourceKey)
	assert.ErrorIs(t, err, store.ErrNotFound, "a thread Podium is not in is not recorded")
}

// A source may move a record into another conversation — a Slack thread bound to a pull
// request records its chatter, edits and deletes in the PR's chat — but never a message that
// can start a turn.
func TestARecordMovesToTheConversationItBelongsTo(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()

	review := inbound("C1/4.1", "what is this?")
	review.ExternalID = "4.1"
	require.NoError(t, src.Send(ctx, review))
	waitFor(t, 30*time.Second, "the review turn", func() bool {
		return turnStatus(st, review.SourceKey) == store.TurnSucceeded
	})
	pr, err := st.ChatBySourceKey(ctx, review.SourceKey)
	require.NoError(t, err)

	chatter := inbound("C1/5.1", "the bound thread says hi")
	chatter.RecordOnly = true
	chatter.ExternalID = "5.2"
	chatter.MirrorKey = review.SourceKey
	require.NoError(t, src.Send(ctx, chatter))
	waitFor(t, 10*time.Second, "the bound chatter in the PR's chat", func() bool {
		msgs, err := st.ListChatMessages(ctx, pr.ID, 0)
		return err == nil && len(msgs) > 0 && msgs[len(msgs)-1].ExternalID == "5.2"
	})

	edit := inbound("C1/5.1", "the bound thread says hello")
	edit.Change = conductor.ChangeEdited
	edit.ExternalID = "5.2"
	edit.MirrorKey = review.SourceKey
	require.NoError(t, src.Send(ctx, edit))
	waitFor(t, 10*time.Second, "the edit in the PR's chat", func() bool {
		msgs, err := st.ListChatMessages(ctx, pr.ID, 0)
		if err != nil || len(msgs) == 0 {
			return false
		}
		last := msgs[len(msgs)-1]
		return last.EditedAt != nil && last.Text == "the bound thread says hello"
	})
	_, err = st.ChatBySourceKey(ctx, chatter.SourceKey)
	assert.ErrorIs(t, err, store.ErrNotFound, "the bound thread has no chat of its own")

	// A message that can start a turn ignores the move and is its own conversation.
	ask := inbound("C1/6.1", "a question of its own")
	ask.ExternalID = "6.1"
	ask.MirrorKey = review.SourceKey
	require.NoError(t, src.Send(ctx, ask))
	waitFor(t, 30*time.Second, "its own turn", func() bool {
		return turnStatus(st, ask.SourceKey) == store.TurnSucceeded
	})
	_, err = st.ChatBySourceKey(ctx, ask.SourceKey)
	require.NoError(t, err)
	msgs, err := st.ListChatMessages(ctx, pr.ID, 0)
	require.NoError(t, err)
	for _, m := range msgs {
		assert.NotEqual(t, "6.1", m.ExternalID, "a turn's message is never moved")
	}
}

// A mirrored chat its owner deleted is not opened again by the conversation's next message:
// that message, and every one after it, is recorded into the deleted chat (so a turn's
// transcript stays whole), and turns from the thread still run.
func TestADeletedMirroredChatStaysDeleted(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()

	first := inbound("C1/7.1", "the first question")
	first.ExternalID = "7.1"
	first.ChannelPrivacy = conductor.ChannelPrivate
	first.ThreadURL = "https://acme.slack.com/archives/C1/p71"
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the first turn", func() bool {
		return turnStatus(st, first.SourceKey) == store.TurnSucceeded
	})
	chat, err := st.ChatBySourceKey(ctx, first.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, store.ChannelPrivate, chat.ChannelPrivacy, "the mirror records what the source says about the channel")
	assert.Equal(t, "https://acme.slack.com/archives/C1/p71", chat.SourceURL, "and the link to the thread")
	owner, shared := chat.Login, false
	if owner == "" {
		owner, shared = "dev", true
	}
	require.NoError(t, st.DeleteChat(ctx, chat.ID, owner, shared))

	second := inbound("C1/7.1", "and a second question")
	second.ExternalID = "7.2"
	require.NoError(t, src.Send(ctx, second))
	waitFor(t, 30*time.Second, "the second turn", func() bool {
		return len(fake.Specs()) == 2 && turnStatus(st, first.SourceKey) == store.TurnSucceeded
	})
	again, err := st.ChatBySourceKey(ctx, first.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, chat.ID, again.ID, "no new chat was opened for the thread")
	require.NotNil(t, again.DeletedAt, "and the chat is still deleted")
	waitFor(t, 10*time.Second, "the second message in the deleted chat's record", func() bool {
		msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.ExternalID == "7.2" {
				return true
			}
		}
		return false
	})
}

// An edit changes the recorded row in place and starts nothing — even one that now names the
// bot. A delete marks the row and keeps its text.
func TestAnEditOrADeleteChangesTheRecordAndStartsNothing(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered.")}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()

	ask := inbound("C1/1.1", "teh question")
	ask.ExternalID = "1.1"
	require.NoError(t, src.Send(ctx, ask))
	waitFor(t, 30*time.Second, "the turn", func() bool {
		return turnStatus(st, ask.SourceKey) == store.TurnSucceeded
	})
	thread, err := st.ChatBySourceKey(ctx, ask.SourceKey)
	require.NoError(t, err)

	edit := inbound("C1/1.1", "@podium the question, again")
	edit.ExternalID = "1.1"
	edit.Change = conductor.ChangeEdited
	require.NoError(t, src.Send(ctx, edit))
	waitFor(t, 10*time.Second, "the edit", func() bool {
		msgs, err := st.ListChatMessages(ctx, thread.ID, 0)
		return err == nil && len(msgs) > 0 && msgs[0].EditedAt != nil
	})

	del := inbound("C1/1.1", "")
	del.ExternalID = "1.1"
	del.Change = conductor.ChangeDeleted
	require.NoError(t, src.Send(ctx, del))
	waitFor(t, 10*time.Second, "the delete", func() bool {
		msgs, err := st.ListChatMessages(ctx, thread.ID, 0)
		return err == nil && len(msgs) > 0 && msgs[0].DeletedAt != nil
	})
	msgs, err := st.ListChatMessages(ctx, thread.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, "@podium the question, again", msgs[0].Text, "the record keeps a deleted message's text")

	time.Sleep(300 * time.Millisecond)
	assert.Len(t, fake.Specs(), 1, "neither the edit nor the delete started a turn")
}

// Two people watching one chat may be connected to two conductors. A frame published on one
// reaches a watcher on the other, through LISTEN/NOTIFY and a re-read of the row.
func TestAFrameReachesAWatcherOnAnotherConductor(t *testing.T) {
	stA, stB := newStorePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := chat.New(chat.Options{Store: stA, Bus: stA})
	require.NoError(t, err)
	b, err := chat.New(chat.Options{Store: stB, Bus: stB})
	require.NoError(t, err)
	go func() { _ = a.RunRelay(ctx) }()
	go func() { _ = b.RunRelay(ctx) }()

	c, err := stA.CreateMirrorChat(ctx, "slack:C1:1.1", store.OriginSlack, "alice", "t")
	require.NoError(t, err)
	row, err := stA.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: c.ID, Role: store.RoleUser, Author: "alice", Text: "hello from A", Via: store.ViaSlack,
	})
	require.NoError(t, err)

	sub := b.Subscribe(ctx, c.ID)
	defer sub.Close()
	local := a.Subscribe(ctx, c.ID)
	defer local.Close()

	// The relays connect asynchronously; announce until B has heard. A duplicate is harmless
	// by design — a watcher keys messages by seq.
	got := make(chan chat.Frame, 1)
	go func() {
		for {
			select {
			case f := <-sub.Frames():
				if f.Kind == chat.FrameMessage {
					got <- f
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(15 * time.Second)
	for done := false; !done; {
		select {
		case f := <-got:
			assert.Equal(t, row.Seq, f.Message.Seq)
			assert.Equal(t, "hello from A", f.Message.Text, "re-read from the row, not carried")
			assert.Equal(t, store.ViaSlack, f.Message.Via)
			done = true
		case <-tick.C:
			a.Mirrored(c.ID, row)
		case <-deadline:
			t.Fatal("a watcher on conductor B never heard conductor A's message")
		}
	}

	// A's own watcher got it from A directly, and A never re-publishes what it announced.
	select {
	case f := <-local.Frames():
		assert.Equal(t, row.Seq, f.Message.Seq)
	case <-time.After(time.Second):
		t.Fatal("the local watcher missed the frame")
	}
}

// newStorePair is two conductors' stores on one database.
func newStorePair(t *testing.T) (*store.Store, *store.Store) {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_agent_conductor_%d_%d", os.Getpid(), dbSeq.Add(1))
	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name

	a, err := store.New(ctx, u.String())
	require.NoError(t, err)
	t.Cleanup(a.Close)
	require.NoError(t, a.Migrate(ctx))
	b, err := store.New(ctx, u.String())
	require.NoError(t, err)
	t.Cleanup(b.Close)
	return a, b
}
