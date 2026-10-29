package ghreview

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (h *harness) filter(f Filters) {
	h.src.filters = func(context.Context) (Filters, error) { return f, nil }
}

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

func TestReplyModeAnyThreadIsTheOldRule(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{ReplyMode: ReplyAnyThread})
	h.stub.threadRoot(8, User{Login: "bob", Type: "User"})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reviewReply("fixed", 8)).Code)
	assert.Empty(t, h.drain(), "no session yet")

	h.session("github:acme/repo#12")
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-2", reviewReply("fixed", 8)).Code)
	assert.Len(t, h.drain(), 1)
	assert.Zero(t, h.stub.threadRootReads())
}

func TestReplyModeAlwaysNeedsTheTriggerInTheBotsThread(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{ReplyMode: ReplyAlways})
	h.session("github:acme/repo#12")
	h.stub.threadRoot(8, User{Login: "podium[bot]", Type: "Bot"})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reviewReply("fixed", 8)).Code)
	assert.Empty(t, h.drain())
}

func TestATriggerPhraseStartsATopLevelComment(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{TriggerPhrases: []string{"/review", "take a look"}})

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", prComment("lgtm")).Code)
	assert.Empty(t, h.drain())
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-2", prComment("see a/review/b")).Code)
	assert.Empty(t, h.drain(), "a phrase only matches as whole words")

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-3", prComment("/REVIEW the auth change")).Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0].Text, "the auth change")
	assert.NotContains(t, evs[0].Text, "REVIEW")

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-4", prComment("please Take  a\nlook, thanks")).Code)
	assert.Len(t, h.drain(), 1)
}

func TestTheMentionStillWorksBesidePhrases(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{TriggerPhrases: []string{"/review"}})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", prComment("@podium review")).Code)
	assert.Len(t, h.drain(), 1)
}

func TestActorFilters(t *testing.T) {
	cases := []struct {
		name   string
		f      Filters
		sender map[string]any
		want   bool
		reason string
	}{
		{"anyone by default", Filters{}, map[string]any{"login": "alice", "type": "User"}, true, ""},
		{"allowed", Filters{AllowedActors: []string{"@Alice"}}, map[string]any{"login": "alice", "type": "User"}, true, ""},
		{"not allowed", Filters{AllowedActors: []string{"bob"}}, map[string]any{"login": "alice", "type": "User"}, false, "actor not allowed"},
		{"ignored", Filters{IgnoredActors: []string{"ALICE"}}, map[string]any{"login": "alice", "type": "User"}, false, "ignored actor"},
		{"ignored beats allowed", Filters{AllowedActors: []string{"alice"}, IgnoredActors: []string{"alice"}}, map[string]any{"login": "alice", "type": "User"}, false, "ignored actor"},
		{"bots off", Filters{IgnoreBots: true}, map[string]any{"login": "renovate[bot]", "type": "Bot"}, false, "bot sender"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			var buf bytes.Buffer
			h.src.logger = slog.New(slog.NewTextHandler(&buf, nil))
			h.filter(tc.f)
			payload := prComment("@podium review")
			payload["sender"] = tc.sender
			payload["comment"].(map[string]any)["user"] = tc.sender
			require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d", payload).Code)
			if tc.want {
				assert.Len(t, h.drain(), 1)
				return
			}
			assert.Empty(t, h.drain())
			assert.Contains(t, buf.String(), `reason="`+tc.reason+`"`)
		})
	}
}

func TestTheActorFilterAppliesToTheAutoReview(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{AllowedActors: []string{"bob"}})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-1", readyForReview()).Code)
	assert.Empty(t, h.drain())
}

func TestAutoReviewOff(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{NoAutoReview: true})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-1", readyForReview()).Code)
	assert.Empty(t, h.drain())
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-2", prComment("@podium review")).Code)
	assert.Len(t, h.drain(), 1, "a mention still starts a review")
}

func TestFiltersThatCannotBeReadStartNothing(t *testing.T) {
	h := newHarness(t)
	h.src.filters = func(context.Context) (Filters, error) { return Filters{}, errors.New("db down") }
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", prComment("@podium review")).Code)
	assert.Empty(t, h.drain())
}

func TestAnExcludedBaseBranchStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.filter(Filters{ExcludedBranches: []string{"main", "feature/*"}})
	pr := readyForReview()
	pr["pull_request"].(map[string]any)["base"] = map[string]any{"ref": "feature/login"}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-1", pr).Code)
	assert.Empty(t, h.drain())

	pr["pull_request"].(map[string]any)["base"] = map[string]any{"ref": "feature/login/ui"}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-2", pr).Code)
	assert.Len(t, h.drain(), 1, "* does not cross a slash")
}

func TestAnExcludedBranchReadsTheBaseForAnIssueComment(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	h.src.logger = slog.New(slog.NewTextHandler(&buf, nil))
	h.filter(Filters{ExcludedBranches: []string{"/^de/"}})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", prComment("@podium review")).Code)
	assert.Empty(t, h.drain(), "the stub's pull request is into dev")
	assert.Contains(t, buf.String(), `reason="excluded branch"`)
	assert.Contains(t, buf.String(), "base=dev")

	h.filter(Filters{ExcludedBranches: []string{"main"}})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-2", prComment("@podium review")).Code)
	assert.Len(t, h.drain(), 1)
}

func TestAnUnreadableBaseStartsNothingWhenBranchesAreExcluded(t *testing.T) {
	h := newHarness(t)
	h.stub.failPull = true
	h.filter(Filters{ExcludedBranches: []string{"main"}})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", prComment("@podium review")).Code)
	assert.Empty(t, h.drain())

	h.filter(Filters{})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-2", prComment("@podium review")).Code)
	assert.Len(t, h.drain(), 1, "with nothing excluded the base is not read")
}

func TestAReviewCommentCarriesItsBase(t *testing.T) {
	h := newHarness(t)
	h.stub.failPull = true
	h.filter(Filters{ExcludedBranches: []string{"main"}})
	reply := reviewReply("@podium why?", 8)
	reply["pull_request"].(map[string]any)["base"] = map[string]any{"ref": "dev"}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-1", reply).Code)
	assert.Len(t, h.drain(), 1, "the payload's base is used without reading the pull request")
}

// A thread a person started is the App's to answer in once the App has replied in it.
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

	h.filter(Filters{ReplyMode: ReplyAlways})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request_review_comment", "d-j3", reviewReply("and this?", 8)).Code)
	assert.Empty(t, h.drain(), "always asks for the trigger")
}

// With conversation replies on, a comment that directly follows the App's own on the
// conversation tab, within half an hour, needs no mention. Anything else still does.
func TestAConversationReplyRightAfterTheAppNeedsNoMention(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	bot := User{Login: "podium[bot]", Type: "Bot"}
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) // prComment's created_at
	h.stub.mu.Lock()
	h.stub.issueComments = []Comment{{ID: 5, Body: "the race is in Close", CreatedAt: at.Add(-10 * time.Minute), User: bot}}
	h.stub.mu.Unlock()

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-c1", prComment("why Close?")).Code)
	assert.Empty(t, h.drain(), "off by default")

	h.filter(Filters{ConversationReplies: true})
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-c2", prComment("why Close?")).Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0].Text, "why Close?")

	h.stub.mu.Lock()
	h.stub.issueComments = append(h.stub.issueComments,
		Comment{ID: 6, Body: "bob, can you look?", CreatedAt: at.Add(-5 * time.Minute), User: User{Login: "alice", Type: "User"}})
	h.stub.mu.Unlock()
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-c3", prComment("sure")).Code)
	assert.Empty(t, h.drain(), "people talking to each other after the App spoke")

	h.stub.mu.Lock()
	h.stub.issueComments = []Comment{{ID: 5, CreatedAt: at.Add(-2 * time.Hour), UpdatedAt: at.Add(-time.Hour), User: bot}}
	h.stub.mu.Unlock()
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-c4", prComment("why Close?")).Code)
	assert.Empty(t, h.drain(), "an hour later is not a reply to it")
}
