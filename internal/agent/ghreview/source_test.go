package ghreview

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

const testSecret = "webhook-secret"

type harness struct {
	src   *Source
	stub  *stub
	mu    sync.Mutex
	known map[string]bool
	slack map[string]string // slackRef -> sourceKey
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		stub:  newStub(t),
		known: map[string]bool{},
		slack: map[string]string{},
	}
	src, err := NewSource(SourceOptions{
		AppID:         "1",
		PrivateKey:    testKey(t),
		WebhookSecret: testSecret,
		Listen:        "127.0.0.1:0",
		APIURL:        h.stub.url(),
		Session: func(_ context.Context, key string) (time.Time, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return time.Time{}, h.known[key]
		},
		Surfaces: Surfaces{
			LookupSlack: func(_ context.Context, slackRef string) (string, bool, error) {
				h.mu.Lock()
				defer h.mu.Unlock()
				k, ok := h.slack[slackRef]
				return k, ok, nil
			},
			BindSlack: func(_ context.Context, slackRef, sourceKey string) error {
				h.mu.Lock()
				defer h.mu.Unlock()
				if existing, ok := h.slack[slackRef]; ok && existing != sourceKey {
					return ErrBoundToOther
				}
				h.slack[slackRef] = sourceKey
				return nil
			},
			ListSlack: func(_ context.Context, sourceKey string) ([]string, error) {
				h.mu.Lock()
				defer h.mu.Unlock()
				var out []string
				for ref, key := range h.slack {
					if key == sourceKey {
						out = append(out, ref)
					}
				}
				return out, nil
			},
		},
	})
	require.NoError(t, err)
	src.slug = "podium"
	src.mentionRE = compileMention("podium")
	h.src = src
	return h
}

func (h *harness) session(key string) { h.mu.Lock(); h.known[key] = true; h.mu.Unlock() }

// drain is the events that could start a turn. What is only recorded — a comment nobody put
// to Podium, an edit, a delete — is the record's business, and drainAll returns it too.
func (h *harness) drain() []conductor.InboundEvent {
	var out []conductor.InboundEvent
	for _, ev := range h.drainAll() {
		if ev.RecordOnly || ev.Change != "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// drainAll is every event offered, recorded-only ones included.
func (h *harness) drainAll() []conductor.InboundEvent {
	var out []conductor.InboundEvent
	for {
		select {
		case ev := <-h.src.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, h *harness, event, delivery string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerEvent, event)
	req.Header.Set(headerDelivery, delivery)
	req.Header.Set(headerSignature, sign(body))
	rec := httptest.NewRecorder()
	h.src.Handler().ServeHTTP(rec, req)
	return rec
}

func prComment(body string) map[string]any {
	return map[string]any{
		"action":       "created",
		"installation": map[string]any{"id": 42},
		"repository": map[string]any{
			"name": "repo", "owner": map[string]any{"login": "acme"},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
		"comment": map[string]any{
			"id": 7, "body": body, "created_at": "2026-09-14T12:00:00Z",
			"user": map[string]any{"login": "alice", "type": "User"},
		},
		"issue": map[string]any{
			"number": 12, "pull_request": map[string]any{"url": "https://api.github.com/repos/acme/repo/pulls/12"},
		},
	}
}

// reviewer is the review playbook speaking: its answer is the review.
var reviewer = conductor.Speaker{Playbook: "review"}

func TestRefRoundTrips(t *testing.T) {
	p := Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7}
	got, err := ParseRef(Ref(p))
	require.NoError(t, err)
	assert.Equal(t, p, got)

	p.Kind = KindSlack
	p.SlackChan, p.SlackThread, p.SlackTS = "C1", "1.1", "2.2"
	got, err = ParseRef(Ref(p))
	require.NoError(t, err)
	assert.Equal(t, p, got)
}

func TestParseRefRefusesAnythingElse(t *testing.T) {
	for _, ref := range []string{"", "a/b", "a/b/c/d/e", "a/b/x/1/issue_comment/1"} {
		_, err := ParseRef(ref)
		assert.Error(t, err, "%q", ref)
	}
}

func TestSourceKeyIsThePullRequest(t *testing.T) {
	assert.Equal(t, "github:acme/repo#12", SourceKey("acme", "repo", 12))
	owner, repo, n, ok := ParseSourceKey("github:acme/repo#12")
	require.True(t, ok)
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "repo", repo)
	assert.Equal(t, 12, n)
	_, _, _, ok = ParseSourceKey("slack:C1:1.1")
	assert.False(t, ok)
}

func TestWebhookRejectsABadSignature(t *testing.T) {
	h := newHarness(t)
	body := []byte(`{"zen":"ok"}`)
	req := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(string(body)))
	req.Header.Set(headerEvent, "ping")
	req.Header.Set(headerSignature, "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.src.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, h.drain())
}

func TestPingIsOKAndSilent(t *testing.T) {
	h := newHarness(t)
	rec := postWebhook(t, h, "ping", "d1", map[string]any{"zen": "ok"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.drain())
}

func TestAMentionOnAPRWithNoSessionStartsAReview(t *testing.T) {
	h := newHarness(t)
	rec := postWebhook(t, h, "issue_comment", "d-start", prComment("@podium review this"))
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Equal(t, conductor.SourceGitHub, evs[0].BriefKind)
	assert.Equal(t, "alice", evs[0].Author)
	assert.Contains(t, evs[0].Text, "https://github.com/acme/repo/pull/12")
	assert.Contains(t, evs[0].Text, "review this")
	assert.NotContains(t, evs[0].Text, "@podium")
	assert.NotContains(t, evs[0].Text, "already reviewed")
	assert.Contains(t, evs[0].Text, "alice commented on")
	assert.False(t, evs[0].Automatic, "what a person wrote is the assistant's to answer")
}

func readyForReview() map[string]any {
	return map[string]any{
		"action":       "ready_for_review",
		"installation": map[string]any{"id": 42},
		"repository": map[string]any{
			"name": "repo", "owner": map[string]any{"login": "acme"},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
		"pull_request": map[string]any{
			"number": 12, "title": "Retry the  flaky upload", "draft": false, "updated_at": "2026-09-14T12:00:00Z",
		},
	}
}

func TestChatTitleNamesThePullRequest(t *testing.T) {
	assert.Equal(t, "#12: Retry the flaky upload", ChatTitle(12, " Retry the\n flaky upload "))
	assert.Equal(t, "#12", ChatTitle(12, ""))
	assert.Equal(t, "", ChatTitle(0, "anything"))
}

// A pull request opened, or marked ready, starts no turn here: reviewing it is the "Review
// pull requests" routine's, which is kept the delivery with its own conditions.
func TestAPullRequestOpenedOrMarkedReadyIsARoutinesToReview(t *testing.T) {
	for _, action := range []string{"opened", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			var buf bytes.Buffer
			h.src.logger = slog.New(slog.NewTextHandler(&buf, nil))
			pr := readyForReview()
			pr["action"] = action
			assert.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-"+action, pr).Code)
			assert.Empty(t, h.drain())
			assert.Contains(t, buf.String(), `reason="a routine's to run on"`)
		})
	}
}

func TestOtherPullRequestActionsAreDropped(t *testing.T) {
	h := newHarness(t)
	requested := readyForReview()
	requested["action"] = "review_requested"
	requested["requested_reviewer"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	assert.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-req", requested).Code)
	synced := readyForReview()
	synced["action"] = "synchronize"
	assert.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-sync", synced).Code)
	assert.Empty(t, h.drain())
}

// A review a routine starts on a pull request reviewed before goes on from where that review
// stopped: only what changed, or the same verdict again when nothing did.
func TestAReviewARoutineStartsGoesOnFromTheLastOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	assert.Empty(t, h.src.ReviewFollowUp(ctx, "github:acme/repo#12"), "never reviewed: a whole review")

	// The last review is the App's latest on GitHub, whoever posted it as the App: a playbook
	// with gh, most often. Anybody else's review is not where the App's next round starts.
	bot := User{Login: "podium[bot]", Type: "Bot"}
	h.stub.listedReviews = []Review{
		{ID: 70, State: "COMMENTED", CommitID: "0000000older", User: bot},
		{ID: 77, State: "CHANGES_REQUESTED", CommitID: "abc1234deadbeef", User: bot},
		{ID: 80, State: "APPROVED", CommitID: "fffffffperson", User: User{Login: "alice", Type: "User"}},
	}
	note := h.src.ReviewFollowUp(ctx, "github:acme/repo#12")
	assert.Contains(t, note, "reviewed before, on commit abc1234deadbeef.")
	assert.Contains(t, note, "The head is now 0123456789abcdef.")
	assert.NotContains(t, note, "Approve", "what to do with it is the playbook's")
	assert.NotContains(t, note, ".podium/review.json")

	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED", CommitID: "0123456789abcdef", User: bot}}
	note = h.src.ReviewFollowUp(ctx, "github:acme/repo#12")
	assert.Contains(t, note, "The head has not moved since.")
	assert.NotContains(t, note, "Do not add inline comments.")

	assert.Empty(t, h.src.ReviewFollowUp(ctx, "chat:abc"), "not a pull request")
}

// A comment is the assistant's, told where the last review stopped so a re-review it
// delegates can say so. It is not told to review: that is its decision.
func TestACommentSaysWhereTheLastReviewStopped(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	bot := User{Login: "podium[bot]", Type: "Bot"}
	h.stub.listedReviews = []Review{{ID: 77, State: "CHANGES_REQUESTED", CommitID: "abc1234deadbeef", User: bot}}
	rec := postWebhook(t, h, "issue_comment", "d-moved", prComment("@podium review the fixes"))
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.False(t, evs[0].Automatic)
	assert.Contains(t, evs[0].Text, "alice commented on https://github.com/acme/repo/pull/12.")
	assert.Contains(t, evs[0].Text, "The last review was on commit abc1234deadbeef.")
	assert.Contains(t, evs[0].Text, "The head is now 0123456789abcdef.")
	assert.Contains(t, evs[0].Text, "review the fixes")
	assert.NotContains(t, evs[0].Text, "@podium")
	assert.NotContains(t, evs[0].Text, "reviewed before", "how to review is the review playbook's business")

	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED", CommitID: "0123456789abcdef", User: bot}}
	rec = postWebhook(t, h, "issue_comment", "d-still", prComment("@podium why?"))
	assert.Equal(t, http.StatusOK, rec.Code)
	evs = h.drain()
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0].Text, "The head has not moved since.")
	assert.NotContains(t, evs[0].Text, "The head is now")
}

func TestACommentOnANonPRIssueIsDropped(t *testing.T) {
	h := newHarness(t)
	payload := prComment("@podium hi")
	issue := payload["issue"].(map[string]any)
	delete(issue, "pull_request")
	rec := postWebhook(t, h, "issue_comment", "d-issue", payload)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.drain())
}

func TestNoMentionAndNoSessionIsDropped(t *testing.T) {
	h := newHarness(t)
	rec := postWebhook(t, h, "issue_comment", "d-lgtm", prComment("lgtm"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.drain())
}

func TestNoMentionOnASessionPRIsDroppedUnlessItIsAReviewReply(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	rec := postWebhook(t, h, "issue_comment", "d-lgtm2", prComment("lgtm"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.drain(), "a conversation-tab lgtm is not a follow-up")
}

// What nobody put to Podium is still said: it is offered as RecordOnly, with the person's
// own words and the comment's identity, and the conductor keeps it for a PR it holds.
func TestAnUnaddressedCommentIsOfferedForTheRecord(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	payload := prComment("lgtm")
	payload["comment"].(map[string]any)["html_url"] = "https://github.com/acme/repo/pull/12#issuecomment-7"
	rec := postWebhook(t, h, "issue_comment", "d-rec", payload)
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drainAll()
	require.Len(t, evs, 1)
	assert.True(t, evs[0].RecordOnly)
	assert.Equal(t, "issue_comment:7", evs[0].ExternalID)
	assert.Equal(t, "https://github.com/acme/repo/pull/12#issuecomment-7", evs[0].ExternalURL)
	assert.Equal(t, "lgtm", evs[0].Text)
	assert.Equal(t, "lgtm", evs[0].RecordText)
	assert.Equal(t, "alice", evs[0].SpeakerHandle)
}

// An edit and a delete are offered as changes of the recorded comment, and never as a
// turn — not even an edit that now mentions the bot.
func TestEditsAndDeletesAreChangesNotTurns(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	edited := prComment("@podium now do it")
	edited["action"] = "edited"
	rec := postWebhook(t, h, "issue_comment", "d-edit", edited)
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drainAll()
	require.Len(t, evs, 1)
	assert.Equal(t, conductor.ChangeEdited, evs[0].Change)
	assert.Equal(t, "issue_comment:7", evs[0].ExternalID)
	assert.Equal(t, "@podium now do it", evs[0].Text, "an edit's text is the new body, with no instruction")

	deleted := prComment("@podium now do it")
	deleted["action"] = "deleted"
	rec = postWebhook(t, h, "issue_comment", "d-del", deleted)
	assert.Equal(t, http.StatusOK, rec.Code)
	evs = h.drainAll()
	require.Len(t, evs, 1)
	assert.Equal(t, conductor.ChangeDeleted, evs[0].Change)
	assert.Empty(t, h.drain())
}

// A triggered comment carries its identity too, which is what makes a redelivery of it the
// same row in the record and never a second turn.
func TestATriggeredCommentCarriesItsIdentity(t *testing.T) {
	h := newHarness(t)
	rec := postWebhook(t, h, "issue_comment", "d-id", prComment("@podium look"))
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "issue_comment:7", evs[0].ExternalID)
	assert.Equal(t, "@podium look", evs[0].RecordText, "the record keeps the words, not the instruction")
	assert.Contains(t, evs[0].Text, "alice commented on")
}

func TestAReviewThreadReplyOnASessionPRContinues(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.stub.threadRoot(8, User{Login: "podium[bot]", Type: "Bot"})
	rec := postWebhook(t, h, "pull_request_review_comment", "d-reply", reviewReply("fixed", 8))
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Contains(t, evs[0].Text, "a.go:4")
	assert.Contains(t, evs[0].Text, "fixed")
	assert.Contains(t, evs[0].Text, "alice replied in a review thread on")
	assert.False(t, evs[0].Automatic)
}

// reviewReply is alice replying in the inline thread that comment inReplyTo started.
func reviewReply(body string, inReplyTo int64) map[string]any {
	return map[string]any{
		"action":       "created",
		"installation": map[string]any{"id": 42},
		"repository": map[string]any{
			"name": "repo", "owner": map[string]any{"login": "acme"},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
		"comment": map[string]any{
			"id": 9, "body": body, "created_at": "2026-09-14T12:00:00Z",
			"path": "a.go", "line": 4, "in_reply_to_id": inReplyTo,
			"user": map[string]any{"login": "alice", "type": "User"},
		},
		"pull_request": map[string]any{"number": 12},
	}
}

func TestTheAppsOwnCommentIsDropped(t *testing.T) {
	h := newHarness(t)
	payload := prComment("👀 working…")
	payload["sender"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	comment := payload["comment"].(map[string]any)
	comment["user"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	rec := postWebhook(t, h, "issue_comment", "d-self", payload)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.drain())
}

func TestDuplicateDeliveryIsOneEvent(t *testing.T) {
	h := newHarness(t)
	payload := prComment("@podium review")
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "same", payload).Code)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "same", payload).Code)
	assert.Len(t, h.drain(), 1)
}

func TestWebhookLogsTheDelivery(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	h.src.logger = slog.New(slog.NewTextHandler(&buf, nil))

	require.Equal(t, http.StatusOK, postWebhook(t, h, "ping", "d-ping", map[string]any{"zen": "keep-body-out"}).Code)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-start", prComment("@podium review this secret-phrase")).Code)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-lgtm", prComment("lgtm secret-phrase")).Code)
	opened := readyForReview()
	opened["action"] = "review_requested"
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-other", opened).Code)
	self := prComment("bot secret-phrase")
	self["sender"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	self["comment"].(map[string]any)["user"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-self", self).Code)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-start", prComment("@podium again secret-phrase")).Code)

	raw := []byte(`{"zen":"sig-secret-phrase"}`)
	req := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(string(raw)))
	req.Header.Set(headerEvent, "ping")
	req.Header.Set(headerDelivery, "d-bad")
	req.Header.Set(headerSignature, "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.src.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	log := buf.String()
	assert.Contains(t, log, "event=ping")
	assert.Contains(t, log, "delivery=d-ping")
	assert.Contains(t, log, "outcome=ok")
	assert.Contains(t, log, "outcome=started")
	assert.Contains(t, log, "action=created")
	assert.Contains(t, log, "repo=acme/repo")
	assert.Contains(t, log, "pr=12")
	assert.Contains(t, log, "sender=alice")
	assert.Contains(t, log, "delivery=d-start")
	assert.Contains(t, log, "reason=\"not mentioned\"")
	assert.Contains(t, log, "reason=\"unsupported action\"")
	assert.Contains(t, log, "reason=\"own delivery\"")
	assert.Contains(t, log, "outcome=duplicate")
	assert.Contains(t, log, "reason=\"invalid signature\"")
	assert.NotContains(t, log, "secret-phrase")
	assert.NotContains(t, log, "deadbeef")
}

// The assistant's progress is not posted anywhere on the pull request. The comment it is
// answering gets 👀 instead, so the person who asked sees they were heard.
func TestTheAssistantShowsItIsWorkingWithAReactionAndNothingElse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inline := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88})
	tab := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	review := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReview, CommentID: 5})
	for _, ref := range []string{inline, tab, review} {
		require.NoError(t, h.src.React(ctx, ref, conductor.ReactionWorking))
		id, err := h.src.Post(ctx, ref, conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
		require.NoError(t, err)
		assert.Empty(t, id)
		require.NoError(t, h.src.React(ctx, ref, conductor.ReactionFailed))
	}
	assert.Equal(t, []string{
		"/pulls/comments/88/reactions eyes",
		"/issues/comments/7/reactions eyes",
	}, h.stub.postedReactions(), "a review body has no comment to react on")
	assert.Empty(t, h.stub.postedReviews())
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedIssueEdits(), "a failed reply does not touch the review's status")
}

// The review playbook's failure retitles the status comment. Anybody else's says it did not
// answer, where the question was asked, and leaves the task id to the operator's log.
func TestAReplyThatFailsSaysSoWithoutTheTaskID(t *testing.T) {
	h := newHarness(t)
	text := "I hit the 1h0m0s limit for this playbook. Task `task_01`."
	for _, ref := range []string{
		Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7}),
		Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88}),
	} {
		_, err := h.src.Post(context.Background(), ref, conductor.Outbound{
			Type: conductor.OutFailure, Text: text, TaskID: "task_01",
		})
		require.NoError(t, err)
	}
	require.Equal(t, []string{replyFailedText}, h.stub.postedIssueBodies())
	assert.Equal(t, []int64{88}, h.stub.postedReviewInReplyTo())
	assert.Empty(t, h.stub.postedIssueEdits())
	assert.Empty(t, h.stub.postedReviews())
}

// Nobody's answer is a review, the review playbook's included: what it says is a reply, asked
// or answered where the question was. A review is what that playbook posts itself.
func TestAReviewPlaybooksWordsAreRepliesNotAReview(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutQuestion, Text: "Which file?"})
	require.NoError(t, err)
	_, err = h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: "reading the diff"})
	require.NoError(t, err)
	_, err = h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Approve**\n\nPosted the review."})
	require.NoError(t, err)
	assert.Equal(t, []string{"Which file?", "**Approve**\n\nPosted the review."}, h.stub.postedIssueBodies(),
		"a question and an answer, as anybody's; progress is not posted")
	assert.Empty(t, h.stub.postedReviews(), "the conductor submits no review: a first line of Approve is only words")
	assert.Empty(t, h.stub.postedIssueEdits(), "and there is no status comment to retitle")
}

func TestFetchTranscriptDropsTheInProgressReview(t *testing.T) {
	h := newHarness(t)
	h.stub.listedReviews = []Review{{
		ID: 9, Body: reviewingText, SubmittedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		User: User{Login: "podium[bot]", Type: "Bot"},
	}}
	h.stub.issueComments = []Comment{{
		ID: 3, Body: "thanks", CreatedAt: time.Date(2026, 9, 14, 12, 2, 0, 0, time.UTC),
		User: User{Login: "alice", Type: "User"},
	}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 3})
	entries, err := h.src.FetchTranscript(context.Background(), ref)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "thanks", entries[0].Text)
}

func TestTheAssistantRepliesWhereItWasAsked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inline := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88})
	_, err := h.src.Post(ctx, inline, conductor.Outbound{Type: conductor.OutFinal, Text: "agreed"})
	require.NoError(t, err)
	assert.Equal(t, []int64{88}, h.stub.postedReviewInReplyTo())

	tab := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	_, err = h.src.Post(ctx, tab, conductor.Outbound{Type: conductor.OutFinal, Text: "**Approve**\n\nthe nil check is the fix"})
	require.NoError(t, err)
	assert.Equal(t, []string{"**Approve**\n\nthe nil check is the fix"}, h.stub.postedIssueBodies())

	other := conductor.Speaker{Playbook: "fixer"}
	_, err = h.src.Post(ctx, tab, conductor.Outbound{Speaker: other, Type: conductor.OutFinal, Text: "pushed abc123"})
	require.NoError(t, err)
	assert.Len(t, h.stub.postedIssueBodies(), 2, "another playbook replies too")
	assert.Empty(t, h.stub.postedReviews(), "only the review playbook's answer is a review")
	assert.Empty(t, h.stub.postedIssueEdits())
}

func TestFetchTranscriptDropsScaffoldingAndKeepsFinals(t *testing.T) {
	h := newHarness(t)
	h.stub.mu.Lock()
	h.stub.issueComments = []Comment{
		{ID: 1, Body: conductor.Placeholder, CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
		{ID: 2, Body: statusMarker + "\n**Review in progress**", CreatedAt: time.Date(2026, 9, 14, 12, 0, 30, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
		{ID: 4, Body: "the review", CreatedAt: time.Date(2026, 9, 14, 12, 1, 0, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
		{ID: 3, Body: "thanks", CreatedAt: time.Date(2026, 9, 14, 12, 2, 0, 0, time.UTC), User: User{Login: "alice", Type: "User"}},
	}
	h.stub.mu.Unlock()
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 3})
	entries, err := h.src.FetchTranscript(context.Background(), ref)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, conductor.RoleAssistant, entries[0].Role)
	assert.Equal(t, "the review", entries[0].Text)
	assert.Equal(t, conductor.RoleUser, entries[1].Role)
	assert.Equal(t, "thanks", entries[1].Text)
}

func TestIngestSlackStartsTheSameSessionAGitHubMentionWould(t *testing.T) {
	h := newHarness(t)
	err := h.src.IngestSlack(context.Background(), SlackMention{
		PR:      conductor.PullRequest{Owner: "acme", Repo: "repo", Number: 12, URL: "https://github.com/acme/repo/pull/12"},
		Channel: "C1", Thread: "1.1", Trigger: "2.2",
		Author: "bob", Text: "review this", TS: time.Now(),
	})
	require.NoError(t, err)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Contains(t, evs[0].Text, "https://github.com/acme/repo/pull/12")
	p, err := ParseRef(evs[0].Ref)
	require.NoError(t, err)
	assert.Equal(t, KindSlack, p.Kind)
	assert.Equal(t, "C1", p.SlackChan)
}

func TestMirrorKeyIsThePullRequest(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindSlack, SlackChan: "C1", SlackThread: "1.1", SlackTS: "2.2"})
	key, ok := h.src.MirrorKey(ref)
	require.True(t, ok)
	assert.Equal(t, "github:acme/repo#12", key)
}

func TestMentionRegexDoesNotEatALongerLogin(t *testing.T) {
	h := newHarness(t)
	assert.False(t, h.src.mentioned("@podium-extra look"))
	assert.True(t, h.src.mentioned("@podium look"))
	assert.True(t, h.src.mentioned("please @podium[bot] review"))
}

func TestBindSlackRefusesASecondPullRequest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	require.NoError(t, h.src.BindSlack(ctx, "C1/1.1", "github:acme/repo#12"))
	err := h.src.BindSlack(ctx, "C1/1.1", "github:acme/repo#13")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBoundToOther))
}

// A question asked in Slack is answered in Slack. The pull request does not get a comment
// nobody on it asked for, from the review playbook either: a review it posts, it posts itself.
func TestAnAnswerToSlackStaysInSlack(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindSlack, SlackChan: "C1", SlackThread: "1.1", SlackTS: "2.2"})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "it adds a retry"})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviews())

	_, err = h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Approve**\n\nFine."})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviews())
}

// The commenter's role decides what the assistant is offered, and it is read once per person
// per repository for a while. A fork nobody here can push to caps it below write, and the
// turn is told why.
func TestTheCommentersRoleRidesOnTheEvent(t *testing.T) {
	h := newHarness(t)
	h.stub.roles["alice"] = "write"
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-r1", prComment("@podium fix it")).Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "write", evs[0].Role)
	assert.NotContains(t, evs[0].Text, "fork")

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-r2", prComment("@podium again")).Code)
	require.Len(t, h.drain(), 1)
	assert.Equal(t, 1, h.stub.roleReads, "the role is remembered")

	h.stub.fork = true
	h.src.roles = map[string]knownRole{}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-r3", prComment("@podium fix it")).Code)
	evs = h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "triage", evs[0].Role, "nothing here can push to this fork")
	assert.Contains(t, evs[0].Text, "comes from a fork that does not let maintainers push to it")

	h.stub.canModify = true
	h.src.roles = map[string]knownRole{}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-r4", prComment("@podium fix it")).Code)
	evs = h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "write", evs[0].Role, "a fork that lets maintainers push is pushable")

	delete(h.stub.roles, "alice")
	h.src.roles = map[string]knownRole{}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-r5", prComment("@podium fix it")).Code)
	evs = h.drain()
	require.Len(t, evs, 1)
	assert.Empty(t, evs[0].Role, "no role on the repository is no role")
}

// A playbook that fixed the review says so under each finding: a ```reply or ```resolved
// fence naming the finding's path:line lands in that finding's thread, and resolved also
// resolves it. A finding with no thread is kept in the answer, which is posted as usual.
func TestAnAnswerRepliesUnderTheFindingsItFixed(t *testing.T) {
	h := newHarness(t)
	bot := User{Login: "podium[bot]", Type: "Bot"}
	h.stub.mu.Lock()
	h.stub.reviewComments = []Comment{
		{ID: 31, Path: "a.go", Line: 0, OriginalLine: 12, User: bot},
		{ID: 32, Path: "a.go", Line: 12, OriginalLine: 12, InReplyTo: 31, User: User{Login: "alice", Type: "User"}},
		{ID: 33, Path: "b.go", Line: 7, OriginalLine: 7, User: User{Login: "alice", Type: "User"}},
		{ID: 34, Path: "c.go", Line: 3, OriginalLine: 3, User: bot},
	}
	h.stub.threads = []stubThread{{ID: "T_31", Root: 31}, {ID: "T_34", Root: 34}}
	h.stub.mu.Unlock()

	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	fixer := conductor.Speaker{Playbook: "fixer"}
	text := "Pushed abc123.\n\n```resolved\na.go:12\nFixed in abc123: the nil check is back.\n```\n\n" +
		"```reply\nc.go:3\nLeft as is: the caller already checks.\n```\n\n" +
		"```resolved\nb.go:7\nNot ours, but fixed.\n```\n"
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: fixer, Type: conductor.OutFinal, Text: text})
	require.NoError(t, err)

	assert.Equal(t, []int64{31, 34}, h.stub.postedReviewInReplyTo(),
		"each reply goes under the App's own finding, found where it was made even after the line moved")
	h.stub.mu.Lock()
	resolved := append([]string(nil), h.stub.resolved...)
	h.stub.mu.Unlock()
	assert.Equal(t, []string{"T_31"}, resolved, "only a resolved fence resolves its thread")

	comments := h.stub.postedIssueBodies()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "Pushed abc123.")
	assert.Contains(t, comments[0], "**b.go:7**", "a thread a person started is not the App's to answer under")
	assert.NotContains(t, comments[0], "nil check is back")
	assert.Empty(t, h.stub.postedReviews())
}

// With Serve set, Run binds nothing itself: it hands the webhook handler to whoever owns the
// listener, on the configured address and the GitHub path, and stops with it.
func TestRunHandsTheWebhookHandlerToTheListenersOwner(t *testing.T) {
	h := newHarness(t)
	type mount struct {
		listen, path string
		h            http.Handler
	}
	mounted := make(chan mount, 1)
	h.src.serve = func(ctx context.Context, listen, path string, handler http.Handler) error {
		mounted <- mount{listen, path, handler}
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.src.Run(ctx) }()

	var m mount
	select {
	case m = <-mounted:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never mounted its handler")
	}
	assert.Equal(t, "127.0.0.1:0", m.listen)
	assert.Equal(t, WebhookPath, m.path)
	rec := httptest.NewRecorder()
	m.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader("{}")))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "it is the signed handler, refusing an unsigned delivery")

	cancel()
	require.NoError(t, <-done)
	_, open := <-h.src.Events()
	assert.False(t, open, "the source's events close when it stops")
}

// A bot's comment is part of the record and is flagged as a bot's, so the conductor does not
// count it as a human (EME-8). The App's bot user is told by its type or by its [bot] login;
// a person is neither. The author's account is what is judged, not the sender's.
func TestABotsCommentIsOfferedForTheRecordFlaggedAsABot(t *testing.T) {
	for _, tc := range []struct {
		name, login, typ string
		want             bool
	}{
		{"a [bot] login", "dependabot[bot]", "User", true},
		{"a Bot-type account without the suffix", "ci-helper", "Bot", true},
		{"a person", "alice", "User", false},
		{"a machine user is type User and passes for a person", "release-robot", "User", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.session("github:acme/repo#12")
			payload := prComment("bump foo to 1.2")
			payload["sender"] = map[string]any{"login": tc.login, "type": tc.typ}
			payload["comment"].(map[string]any)["user"] = map[string]any{"login": tc.login, "type": tc.typ}
			postWebhook(t, h, "issue_comment", "d-bot", payload)
			evs := h.drainAll()
			require.Len(t, evs, 1)
			assert.True(t, evs[0].RecordOnly)
			assert.Equal(t, tc.login, evs[0].Author)
			assert.Equal(t, tc.want, evs[0].AuthorBot)
		})
	}
}

// A turn of the pull request's conversation nobody on GitHub started — a routine's run, or a
// message in its Podium chat — is answered in Podium. The pull request gets what a playbook
// posts there itself, not a comment nobody on it asked for.
func TestAnAnswerToAPodiumStartedTurnStaysInPodium(t *testing.T) {
	h := newHarness(t)
	ref, ok := h.src.RefForKey("github:acme/repo#12")
	require.True(t, ok)
	for _, out := range []conductor.Outbound{
		{Speaker: reviewer, Type: conductor.OutFinal, Text: "Posted the review.\n```resolved\na.go:3\nfixed\n```"},
		{Speaker: reviewer, Type: conductor.OutFailure, Text: "task_1 failed"},
		{Type: conductor.OutQuestion, Text: "Which branch?"},
	} {
		_, err := h.src.Post(context.Background(), ref, out)
		require.NoError(t, err)
	}
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviewInReplyTo(), "no thread replies either")
	assert.Empty(t, h.stub.postedReviews())
}
