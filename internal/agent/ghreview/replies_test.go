package ghreview

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reply in an inline review thread needs no mention in a thread the App started or has
// replied in, and does in any other. A top-level comment always needs the mention.

func TestAReplyInAPersonsThreadNeedsTheTrigger(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.stub.threadRoot(8, User{Login: "bob", Type: "User"})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reviewReply("agreed", 8)).Code)
	assert.Empty(t, h.drain(), "two people talking in their own thread do not wake the bot")

	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-2", reviewReply("@podium what do you think?", 8)).Code)
	assert.Len(t, h.drain(), 1)
}

func TestAReplyInTheBotsThreadNeedsNoSession(t *testing.T) {
	h := newHarness(t)
	h.stub.threadRoot(8, User{Login: "podium[bot]", Type: "Bot"})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reviewReply("why?", 8)).Code)
	require.Len(t, h.drain(), 1)

	// The thread's author is remembered, so a second reply does not read it again.
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-2", reviewReply("and?", 8)).Code)
	require.Len(t, h.drain(), 1)
	assert.Equal(t, 1, h.stub.threadRootReads())
}

func TestAThreadThatCannotBeReadNeedsTheTrigger(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reviewReply("fixed", 404)).Code)
	assert.Empty(t, h.drain())
}

func TestABotThreadIsOneTheAppStartedOrHasRepliedIn(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.stub.threadRoot(8, User{Login: "alice", Type: "User"})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-j1", reviewReply("and this?", 8)).Code)
	assert.Empty(t, h.drain(), "a person's thread the App is not in needs the trigger")

	h.stub.mu.Lock()
	h.stub.reviewComments = []Comment{
		{ID: 8, Path: "a.go", User: User{Login: "alice", Type: "User"}},
		{ID: 21, Path: "a.go", InReplyTo: 8, User: User{Login: "podium[bot]", Type: "Bot"}},
	}
	h.stub.mu.Unlock()
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-j2", reviewReply("and this?", 8)).Code)
	assert.Len(t, h.drain(), 1, "the App replied in it, so it is the App's thread too")
}
