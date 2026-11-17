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
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// A question the assistant asks in a Linear ticket's conversation is in the inbox of the person
// whose turn asked (docs/linear-agent-plan.md, "Questions in the inbox").

const questionIssueKey = "linear:issue-q"

// issueAppSource is the Linear app with every agent session on one issue's conversation.
func issueAppSource() *fakesource.Source {
	src := fakesource.New(conductor.SourceLinear)
	src.MirrorsAs(func(ref string) (string, bool) {
		if _, ok := linearSessionKey(ref); !ok {
			return "", false
		}
		return questionIssueKey, true
	})
	return src
}

// issuePrompt is a prompt in agent session session on the question test's issue.
func issuePrompt(session, activity, text string) conductor.InboundEvent {
	ev := sessionPrompt(session, activity, text)
	ev.SourceKey = questionIssueKey
	return ev
}

// openQuestionsOf is login's unread question items.
func openQuestionsOf(t *testing.T, st *store.Store, login string) []store.Notification {
	t.Helper()
	rows, err := st.ListNotifications(context.Background(), login, true, 50)
	require.NoError(t, err)
	var out []store.Notification
	for _, n := range rows {
		if n.Kind == store.NotifyQuestion {
			out = append(out, n)
		}
	}
	return out
}

// The question lands once in the prompting person's inbox — the second question of the same
// turn replaces the first — and the answer given in the agent session closes it.
func TestALinearQuestionIsInTheAskedPersonsInboxUntilAnsweredInLinear(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.MsgQuestion, "Which region is slow?"),
			messageEvent(taskID, 2, conductor.MsgQuestion, "And since when?"),
		}
	}
	src := issueAppSource()
	t.Cleanup(src.Close)
	start(t, st, fake, src)
	ctx := context.Background()
	require.NoError(t, st.UpsertLinearIssue(ctx, store.LinearIssue{
		SourceKey: questionIssueKey, Identifier: "ENG-1", Title: "Fix the build",
	}))

	require.NoError(t, src.Send(ctx, issuePrompt("s-q1", "a-q1", "the export is slow")))
	waitFor(t, 30*time.Second, "both questions", func() bool {
		return len(posts(src.Records(), conductor.OutQuestion)) == 2
	})
	var got []store.Notification
	waitFor(t, 30*time.Second, "the inbox item", func() bool {
		got = openQuestionsOf(t, st, testSpeaker)
		return len(got) == 1 && got[0].Detail == "And since when?"
	})
	assert.Equal(t, "ENG-1", got[0].LinearIdentifier)
	assert.Equal(t, "Fix the build", got[0].LinearTitle)
	li, err := st.GetLinearIssue(ctx, questionIssueKey)
	require.NoError(t, err)
	assert.Equal(t, testSpeaker, li.AskedOf)
	assert.Empty(t, openQuestionsOf(t, st, "bob@acme.com"), "nobody else is asked")

	require.NoError(t, src.Send(ctx, issuePrompt("s-q1", "a-q2", "eu-west, since Monday")))
	waitFor(t, 30*time.Second, "the answer into the task", func() bool { return len(fake.Injects()) == 1 })
	waitFor(t, 30*time.Second, "the item closed", func() bool { return len(openQuestionsOf(t, st, testSpeaker)) == 0 })
}

// The same item is closed by an answer from the web chat, which is also where the inbox's reply
// box sends.
func TestALinearQuestionIsClosedByAnAnswerFromTheWeb(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	t.Cleanup(release)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.MsgQuestion, "Which region?")}
	}
	src := issueAppSource()
	t.Cleanup(src.Close)
	web := fakesource.New(conductor.SourceChat)
	t.Cleanup(web.Close)
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Sources = append(o.Sources, web) })
	ctx := context.Background()

	require.NoError(t, src.Send(ctx, issuePrompt("s-q2", "a-q3", "the export is slow")))
	waitFor(t, 30*time.Second, "the inbox item", func() bool { return len(openQuestionsOf(t, st, testSpeaker)) == 1 })
	chat, err := st.ChatBySourceKey(ctx, questionIssueKey)
	require.NoError(t, err)

	require.NoError(t, web.Send(ctx, conductor.InboundEvent{
		SourceKind: conductor.SourceChat, SourceKey: questionIssueKey, Ref: chat.ID, ChatID: chat.ID,
		Author: testSpeaker, Login: testSpeaker, Text: "eu-west", TS: time.Now().UTC(), Mentioned: true,
		BriefKind: conductor.SourceChat,
	}))
	waitFor(t, 30*time.Second, "the item closed", func() bool { return len(openQuestionsOf(t, st, testSpeaker)) == 0 })
	li, err := st.GetLinearIssue(ctx, questionIssueKey)
	require.NoError(t, err)
	assert.Nil(t, li.AskedAt)
}
