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
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// A Linear agent session is a chat (mirror.go): the Linear app is a mirrored source, so its
// prompts are recorded, deduped and answered through the outbox as a Slack thread's are.

// linearSessionKey is the Linear app's MirrorKey: the session a four- or five-part ref names,
// and nothing for the poller's three-part ref.
func linearSessionKey(ref string) (string, bool) {
	parts := strings.Split(ref, "/")
	if len(parts) < 4 || parts[3] == "" {
		return "", false
	}
	return "linear:session:" + parts[3], true
}

func linearAppSource() *fakesource.Source {
	src := fakesource.New(conductor.SourceLinear)
	src.MirrorsAs(linearSessionKey)
	return src
}

// sessionPrompt is linearPrompt as the app hands it over once a session is mirrored: the
// prompt's own id, the issue's name and the link to the session.
func sessionPrompt(session, activity, text string) conductor.InboundEvent {
	ev := linearPrompt(session, text, testSpeaker, "u-1")
	ev.ExternalID = "activity:" + activity
	ev.ExternalURL = "https://linear.app/acme/issue/ENG-1/fix-the-build"
	ev.ThreadURL = "https://linear.app/acme/agent-session/" + session
	ev.URL = ev.ExternalURL
	ev.Title = "ENG-1 Fix the build"
	ev.ChannelPrivacy = conductor.ChannelPrivate
	return ev
}

// The people who spoke in a session are its chat's participants, what they asked and what was
// answered is its record, and the chat links back to the session.
func TestALinearSessionIsAChatOfThePeopleInIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "Fixed.")}
	}
	src := linearAppSource()
	t.Cleanup(src.Close)
	start(t, st, fake, src)

	ctx := context.Background()
	ev := sessionPrompt("s-10", "a-1", "fix the build")
	ev.RecordText = "@podium fix the build"
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(st, ev.SourceKey) == store.TurnSucceeded })

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, store.OriginLinear, chat.Origin)
	assert.Equal(t, "ENG-1 Fix the build", chat.Title)
	assert.Equal(t, ev.ThreadURL, chat.SourceURL, "the surface chip opens the session")
	assert.Equal(t, store.ChannelPrivate, chat.ChannelPrivacy)
	assert.Equal(t, testSpeaker, chat.Login, "the first person in Podium to speak owns it")

	people, err := st.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	require.NotEmpty(t, people)
	assert.Equal(t, testSpeaker, people[0].Login)
	assert.Equal(t, store.JoinedLinear, people[0].Via)

	msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	var asked, answered *store.ChatMessage
	for i := range msgs {
		switch msgs[i].Role {
		case store.RoleUser:
			asked = &msgs[i]
		case store.RoleAssistant:
			answered = &msgs[i]
		}
	}
	require.NotNil(t, asked)
	assert.Equal(t, "@podium fix the build", asked.Text, "the record keeps the person's words")
	assert.Equal(t, store.ViaLinear, asked.Via)
	assert.Equal(t, "activity:a-1", asked.ExternalID)
	assert.Equal(t, testSpeaker, asked.AuthorLogin)
	require.NotNil(t, answered)
	assert.Equal(t, "Fixed.", answered.Text)
	require.Len(t, answered.Deliveries, 1, "the answer was owed to the session")
	assert.Equal(t, conductor.SourceLinear, answered.Deliveries[0].SurfaceKind)

	visible, err := st.ChatVisibleTo(ctx, chat.ID, testSpeaker)
	require.NoError(t, err)
	assert.True(t, visible)
	visible, err = st.ChatVisibleTo(ctx, chat.ID, "bob@acme.com")
	require.NoError(t, err)
	assert.False(t, visible, "nobody who did not speak in the session sees it")
}

// A prompt from somebody who is not in Podium is recorded and refused, and a session nobody in
// Podium spoke in is a chat nobody can open.
func TestARefusedLinearPromptIsRecordedInAChatNobodySees(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := linearAppSource()
	t.Cleanup(src.Close)
	start(t, st, fake, src)

	ctx := context.Background()
	ev := sessionPrompt("s-11", "a-2", "deploy prod")
	ev.Author, ev.Login, ev.SpeakerID = "Mallory", "mallory@acme.com", store.LinearExternalID("org-1", "u-666")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the refusal", func() bool {
		return contains(reactions(src.Records()), string(conductor.ReactionRefused))
	})
	assert.Empty(t, fake.Specs())

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Empty(t, chat.Login, "nobody owns it")
	people, err := st.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	for _, p := range people {
		assert.Empty(t, p.Login, "a speaker who is not in Podium is never a participant")
	}
	visible, err := st.ChatVisibleTo(ctx, chat.ID, testSpeaker)
	require.NoError(t, err)
	assert.False(t, visible)
}

// The poller has no accounts and no person to own or see a chat, so a polled ticket is not
// mirrored: its ref names no session, and the source says so.
func TestAPolledLinearTicketHasNoChat(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := linearAppSource()
	t.Cleanup(src.Close)
	start(t, st, fake, src)

	ev := inbound("issue-9/team-1/ENG-9", "ENG-9 — fix the build")
	ev.SourceKind = conductor.SourceLinear
	ev.SourceKey = "linear:issue-9"
	ev.BriefKind = conductor.SourceLinear
	ev.Channel = ""
	ev.Env = nil
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(st, ev.SourceKey) == store.TurnSucceeded })
	_, err := st.ChatBySourceKey(context.Background(), ev.SourceKey)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// Linear retries a delivery it got no 200 for, and the retry may reach another conductor,
// whose source has never seen the prompt. The record is the dedupe: only the arrival that wrote
// the prompt's row starts a turn.
func TestALinearPromptRetriedOnAnotherConductorIsOneTurn(t *testing.T) {
	a, b := newStorePair(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "once.")}
	}
	srcA, srcB := linearAppSource(), linearAppSource()
	t.Cleanup(srcA.Close)
	t.Cleanup(srcB.Close)
	startWith(t, a, fake, srcA, func(o *conductor.Options) { o.ReplicaID = "conductor-a" })
	startWith(t, b, fake, srcB, func(o *conductor.Options) { o.ReplicaID = "conductor-b" })

	ctx := context.Background()
	ev := sessionPrompt("s-12", "a-3", "fix the build")
	require.NoError(t, srcA.Send(ctx, ev))
	require.NoError(t, srcB.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(a, ev.SourceKey) == store.TurnSucceeded })
	time.Sleep(time.Second) // give a second turn every chance to start

	assert.Len(t, fake.Specs(), 1, "the retry started nothing")
	assert.Len(t, startedBy(t, a, ev.SourceKey), 1)
	chat, err := a.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	msgs, err := a.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	users := 0
	for _, m := range msgs {
		if m.Role == store.RoleUser {
			users++
		}
	}
	assert.Equal(t, 1, users, "one prompt, one row")
	finals := len(posts(srcA.Records(), conductor.OutFinal)) + len(posts(srcB.Records(), conductor.OutFinal))
	assert.Equal(t, 1, finals, "one answer in the session")
}

// An answer Linear refused is not lost: it is owed through the outbox and retried until it
// lands, once.
func TestALinearAnswerThatFailedToPostIsRetried(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "the answer.")}
	}
	src := linearAppSource()
	t.Cleanup(src.Close)
	src.FailPosts(1)
	r := startWith(t, st, fake, src, atOnce)

	ctx := context.Background()
	ev := sessionPrompt("s-13", "a-4", "what is it?")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn", func() bool { return turnStatus(st, ev.SourceKey) == store.TurnSucceeded })
	assert.Empty(t, posts(src.Records(), conductor.OutFinal), "Linear refused the first attempt")

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	waitFor(t, 10*time.Second, "the retry to land", func() bool {
		r.cond.DeliverDueForTest(ctx)
		a := answerRow(t, st, chat.ID)
		return len(a.Deliveries) == 1 && a.Deliveries[0].Status == store.OutboxSent
	})
	finals := posts(src.Records(), conductor.OutFinal)
	require.Len(t, finals, 1, "the retry posts the answer once")
	assert.Equal(t, "the answer.", finals[0].Text)
}

// A Stop pressed in Linear reaches one conductor and the turn may run on another. The other
// conductor's source is told before the task is cancelled, and the cancellation its run posts
// carries the stop's reason, which the Linear source recognises as its own stop wherever and
// whenever it is posted.
func TestAStopOnOneConductorIsHeardByTheConductorRunningTheTurn(t *testing.T) {
	a, b := newStorePair(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	fake.terminal = podiumv1.TaskStatus_TASK_STATUS_CANCELLED
	srcA, srcB := linearAppSource(), linearAppSource()
	t.Cleanup(srcA.Close)
	t.Cleanup(srcB.Close)
	startWith(t, a, fake, srcA, func(o *conductor.Options) { o.ReplicaID = "conductor-a" })
	rb := startWith(t, b, fake, srcB, func(o *conductor.Options) { o.ReplicaID = "conductor-b" })

	ctx := context.Background()
	ev := sessionPrompt("s-14", "a-5", "a long job")
	require.NoError(t, srcA.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the task", func() bool { return len(fake.TaskIDs()) == 1 })
	taskID := fake.TaskIDs()[0]

	stopped, err := rb.cond.CancelSessionTurn(ctx, ev.SourceKey, stopReason)
	require.NoError(t, err)
	require.True(t, stopped)

	waitFor(t, 10*time.Second, "the running conductor's source to hear the stop", func() bool {
		for _, r := range srcA.Records() {
			if r.Action == fakesource.ActionStoppedElsewhere && r.Ref == ev.SourceKey && r.Text == taskID {
				return true
			}
		}
		return false
	})
	for _, r := range srcB.Records() {
		assert.NotEqual(t, fakesource.ActionStoppedElsewhere, r.Action,
			"the conductor that stopped it does not tell its own source again")
	}
	waitFor(t, 30*time.Second, "the turn to be recorded cancelled", func() bool {
		return turnStatus(a, ev.SourceKey) == store.TurnCancelled
	})
	waitFor(t, 10*time.Second, "the cancellation", func() bool { return len(posts(srcA.Records(), conductor.OutFailure)) == 1 })
	assert.Equal(t, stopReason, posts(srcA.Records(), conductor.OutFailure)[0].CancelReason)
}
