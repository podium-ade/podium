package ghreview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryIsWhatPeopleSaidBeforeTheTrigger(t *testing.T) {
	s := &Source{slug: "podium"}
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	items := []historyItem{
		{KindReview, 7, "needs work", "https://gh/r7", t0.Add(3 * time.Minute), User{Login: "carol", ID: 3}},
		{KindIssueComment, 1, "first!", "https://gh/c1", t0, User{Login: "bob", ID: 2, AvatarURL: "a.png"}},
		{KindIssueComment, 2, "the bot's own", "https://gh/c2", t0.Add(time.Minute), User{Login: "podium[bot]", Type: "Bot"}},
		{KindReview, 8, "", "https://gh/r8", t0.Add(4 * time.Minute), User{Login: "dave", ID: 4}},
		{KindReviewComment, 5, "this line", "https://gh/i5", t0.Add(2 * time.Minute), User{Login: "erin", ID: 5}},
		{KindIssueComment, 9, "@podium review", "https://gh/c9", t0.Add(5 * time.Minute), User{Login: "bob", ID: 2}},
		{KindIssueComment, 10, "after the trigger", "https://gh/c10", t0.Add(6 * time.Minute), User{Login: "bob", ID: 2}},
	}
	p := Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 9}

	got := s.historyEvents(p, items)
	require.Len(t, got, 3, "no bot, no empty review, nothing from the trigger on")
	ids := []string{got[0].ExternalID, got[1].ExternalID, got[2].ExternalID}
	assert.Equal(t, []string{
		KindIssueComment + ":1", KindReviewComment + ":5", KindReview + ":7",
	}, ids, "oldest first, named the way the webhook names the same messages")

	first := got[0]
	assert.True(t, first.RecordOnly, "history never starts a turn")
	assert.Equal(t, "github:acme/repo#12", first.SourceKey)
	assert.Equal(t, "2", first.SpeakerID)
	assert.Equal(t, "bob", first.SpeakerHandle)
	assert.Equal(t, "a.png", first.SpeakerAvatar)
	assert.Equal(t, "https://gh/c1", first.ExternalURL)
	assert.Equal(t, "first!", first.RecordText)
	assert.Equal(t, t0, first.TS)
}

func TestHistoryForATriggerThatIsNoCommentIsEverythingSoFar(t *testing.T) {
	s := &Source{slug: "podium"}
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	items := []historyItem{
		{KindIssueComment, 1, "one", "", t0, User{Login: "bob"}},
		{KindIssueComment, 2, "two", "", t0.Add(time.Minute), User{Login: "bob"}},
	}
	got := s.historyEvents(Parsed{Owner: "acme", Repo: "repo", Number: 12, Kind: KindPullRequest}, items)
	assert.Len(t, got, 2)
}
