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
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// prInbound is githubInbound on a ref the fake source mirrors under the session's own key,
// the way the GitHub source's MirrorKey names a pull request by the pull request.
func prInbound(text, externalID string) conductor.InboundEvent {
	ev := githubInbound(text)
	ev.Ref = "pr1/12"
	ev.SourceKey = "github:pr1:12"
	ev.ExternalID = externalID
	return ev
}

func earlierComment(author, text, externalID string, at time.Time) conductor.InboundEvent {
	return conductor.InboundEvent{
		SourceKind: conductor.SourceGitHub, SourceKey: "github:pr1:12", Ref: "pr1/12",
		Author: author, SpeakerHandle: author, SpeakerProvider: conductor.SpeakerGitHub,
		Text: text, RecordText: text, TS: at, ExternalID: externalID,
		BriefKind: conductor.SourceGitHub, RecordOnly: true,
	}
}

func startPR(t *testing.T) (*store.Store, *fakePodium, *fakesource.Source) {
	t.Helper()
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "reviewed")}
	}
	src := fakesource.New(conductor.SourceGitHub)
	src.Mirrors()
	t.Cleanup(src.Close)
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Profiles = profiles.NewLive(reviewProfile(t)) })
	return st, fake, src
}

// TestAPullRequestsRecordIsSeededOnceAndNotReadAgain: the record of a pull request starts
// where its conversation did — what was said before Podium came in is read from GitHub once,
// when the record opens — and its turns read the record, never GitHub's copy again.
func TestAPullRequestsRecordIsSeededOnceAndNotReadAgain(t *testing.T) {
	st, fake, src := startPR(t)
	ctx := context.Background()
	now := time.Now().UTC()
	src.SetHistory("pr1/12", []conductor.InboundEvent{
		earlierComment("bob", "this looks risky", "issue_comment:1", now.Add(-2*time.Hour)),
		earlierComment("carol", "agreed, the lock is wrong", "issue_comment:2", now.Add(-time.Hour)),
	})

	require.NoError(t, src.Send(ctx, prInbound("please review", "issue_comment:3")))
	waitFor(t, 30*time.Second, "the review", func() bool { return len(fake.Specs()) == 1 })

	chat, err := st.ChatBySourceKey(ctx, "github:pr1:12")
	require.NoError(t, err)
	assert.True(t, chat.HistorySeeded, "the record says it holds the conversation from the start")
	msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	var said []string
	for _, m := range msgs {
		if m.Role == store.RoleUser {
			said = append(said, m.ExternalID)
		}
	}
	assert.Equal(t, []string{"issue_comment:1", "issue_comment:2", "issue_comment:3"}, said,
		"the earlier comments come first, the message that brought Podium in after them")
	assert.Equal(t, 0, src.Fetches(), "a seeded record never reads GitHub's transcript")

	// GitHub redelivering an earlier comment is the row already there, not a second one.
	again := earlierComment("bob", "this looks risky", "issue_comment:1", now.Add(-2*time.Hour))
	require.NoError(t, src.Send(ctx, again))
	time.Sleep(300 * time.Millisecond)
	msgs, err = st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	users := 0
	for _, m := range msgs {
		if m.Role == store.RoleUser {
			users++
		}
	}
	assert.Equal(t, 3, users)
}

// TestAPullRequestWhoseSeedFailedStillReadsItsEarlierHistory: when GitHub could not be read at
// the moment the record opened, the record starts where Podium came in, and its turns go on
// reading GitHub's earlier history in front of it — the same as a pull request recorded
// before seeding existed.
func TestAPullRequestWhoseSeedFailedStillReadsItsEarlierHistory(t *testing.T) {
	st, fake, src := startPR(t)
	ctx := context.Background()
	src.FailHistory()

	require.NoError(t, src.Send(ctx, prInbound("please review", "issue_comment:3")))
	waitFor(t, 30*time.Second, "the review", func() bool { return len(fake.Specs()) == 1 })

	chat, err := st.ChatBySourceKey(ctx, "github:pr1:12")
	require.NoError(t, err)
	assert.False(t, chat.HistorySeeded)
	assert.Positive(t, src.Fetches(), "an unseeded pull request reads its earlier history from GitHub")
}
