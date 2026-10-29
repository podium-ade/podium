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
	"strconv"
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

func (h *harness) drain() []conductor.InboundEvent {
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
var reviewer = conductor.Speaker{Playbook: "coder", SourcePlaybook: true}

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
			"number": 12, "draft": false, "updated_at": "2026-09-14T12:00:00Z",
		},
	}
}

func TestReadyForReviewStartsAReview(t *testing.T) {
	h := newHarness(t)
	rec := postWebhook(t, h, "pull_request", "d-ready", readyForReview())
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Equal(t, "alice", evs[0].Author)
	assert.Contains(t, evs[0].Text, "https://github.com/acme/repo/pull/12")
	assert.Contains(t, evs[0].Text, "marked")
	assert.Contains(t, evs[0].Text, "ready for review")
	assert.True(t, evs[0].Automatic, "nobody asked anything: the review playbook runs directly")
	p, err := ParseRef(evs[0].Ref)
	require.NoError(t, err)
	assert.Equal(t, KindPullRequest, p.Kind)
	assert.Equal(t, int64(0), p.CommentID)
}

func TestAnOpenPullRequestStartsAReview(t *testing.T) {
	h := newHarness(t)
	opened := readyForReview()
	opened["action"] = "opened"
	rec := postWebhook(t, h, "pull_request", "d-opened", opened)
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Equal(t, "alice", evs[0].Author)
	assert.Contains(t, evs[0].Text, "https://github.com/acme/repo/pull/12")
	assert.Contains(t, evs[0].Text, "opened")
	assert.Contains(t, evs[0].Text, "for review")
	assert.NotContains(t, evs[0].Text, "ready for review")
	assert.True(t, evs[0].Automatic)
	p, err := ParseRef(evs[0].Ref)
	require.NoError(t, err)
	assert.Equal(t, KindPullRequest, p.Kind)
	assert.Equal(t, int64(0), p.CommentID)
}

func TestADraftOpenWaitsUntilItIsReady(t *testing.T) {
	h := newHarness(t)
	opened := readyForReview()
	opened["action"] = "opened"
	opened["pull_request"].(map[string]any)["draft"] = true
	assert.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-draft", opened).Code)
	assert.Empty(t, h.drain())
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

func TestReadyForReviewAgainContinuesTheSession(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	rec := postWebhook(t, h, "pull_request", "d-ready2", readyForReview())
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Contains(t, evs[0].Text, "ready for review")
	assert.Contains(t, evs[0].Text, "already reviewed")
}

func TestReadyAgainReviewsOnlyTheDelta(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.src.replaceReview("github:acme/repo#12", 77)
	h.stub.listedReviews = []Review{{ID: 77, State: "CHANGES_REQUESTED", CommitID: "abc1234deadbeef"}}
	rec := postWebhook(t, h, "pull_request", "d-again", readyForReview())
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.True(t, evs[0].Automatic)
	assert.Contains(t, evs[0].Text, "already reviewed")
	assert.Contains(t, evs[0].Text, "abc1234deadbeef")
	assert.Contains(t, evs[0].Text, "**Approve**")
	assert.Contains(t, evs[0].Text, "0123456789abcdef")
	assert.Contains(t, evs[0].Text, ".podium/review.json")
	assert.NotContains(t, evs[0].Text, "no new diff")
}

func TestReadyAgainOnAnUnchangedHeadRepeatsTheVerdict(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.src.replaceReview("github:acme/repo#12", 77)
	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED", CommitID: "0123456789abcdef"}}
	rec := postWebhook(t, h, "pull_request", "d-same", readyForReview())
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0].Text, "no new diff")
	assert.Contains(t, evs[0].Text, "Do not add inline comments.")
	assert.NotContains(t, evs[0].Text, "**Approve**")
}

// A comment is the assistant's, told where the last review stopped so a re-review it
// delegates can say so. It is not told to review: that is its decision.
func TestACommentSaysWhereTheLastReviewStopped(t *testing.T) {
	h := newHarness(t)
	h.session("github:acme/repo#12")
	h.src.replaceReview("github:acme/repo#12", 77)
	h.stub.listedReviews = []Review{{ID: 77, State: "CHANGES_REQUESTED", CommitID: "abc1234deadbeef"}}
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
	assert.NotContains(t, evs[0].Text, "already reviewed", "how to review is the review playbook's business")

	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED", CommitID: "0123456789abcdef"}}
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

func TestPostOnAnIssueCommentTriggerSubmitsOneReview(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "looks good", TaskID: "task_1"})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	comments := h.stub.postedIssueBodies()
	require.Len(t, comments, 1, "the answer is one status comment and one review")
	assertStatusSummary(t, comments[0])
	assert.Contains(t, comments[0], "**"+statusReviewed+"**")
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "COMMENT", created[0]["event"])
	assert.Contains(t, asString(created[0]["body"]), "looks good")
	assert.NotContains(t, asString(created[0]["body"]), "task_1")
	assert.NotContains(t, asString(created[0]["body"]), "Podium task")

	follow := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	id2, err := h.src.Post(context.Background(), follow, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "still looks good", TaskID: "task_2"})
	require.NoError(t, err)
	assert.Equal(t, id, id2)
	assert.Len(t, h.stub.postedReviews(), 1, "a follow-up updates the review")
	assert.Len(t, h.stub.postedIssueBodies(), 1, "the status comment is the same one")
	assert.Empty(t, h.stub.postedIssueEdits(), "the title is already Reviewed")
	edits := h.stub.postedReviewEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "still looks good")
	assert.NotContains(t, edits[0], "task_2")
}

func TestProgressPostsOneStatusCommentAndTheAnswerRetitlesIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	require.NoError(t, h.src.React(ctx, ref, conductor.ReactionWorking))
	require.NoError(t, h.src.React(ctx, ref, conductor.ReactionDone))
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviews())

	id, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	comments := h.stub.postedIssueBodies()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "**"+statusInProgress+"**")
	assertStatusSummary(t, comments[0])
	assert.Empty(t, h.stub.postedReviews())

	id2, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: "still reading the diff"})
	require.NoError(t, err)
	assert.Equal(t, id, id2)
	require.NoError(t, h.src.Edit(ctx, ref, id, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: "still reading the diff"}))
	assert.Len(t, h.stub.postedIssueBodies(), 1)
	assert.Empty(t, h.stub.postedIssueEdits(), "progress does not rewrite the comment")

	reviewID, err := h.src.Post(ctx, ref, conductor.Outbound{
		Speaker: reviewer,
		Type:    conductor.OutFinal, Text: "**Changes needed**\n\nThe error is dropped.", TaskID: "task_1",
	})
	require.NoError(t, err)
	assert.NotEqual(t, id, reviewID)
	require.Len(t, h.stub.postedReviews(), 1)
	assert.Equal(t, "COMMENT", h.stub.postedReviews()[0]["event"])
	assert.Contains(t, asString(h.stub.postedReviews()[0]["body"]), "The error is dropped.")
	assert.NotContains(t, asString(h.stub.postedReviews()[0]["body"]), "task_1")
	edits := h.stub.postedIssueEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "**"+statusReviewed+"**")
	assertStatusSummary(t, edits[0])
	assert.Len(t, h.stub.postedIssueBodies(), 1)
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

func TestAFailedTurnRetitlesTheStatusComment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	_, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, ref, conductor.ReactionFailed))
	_, err = h.src.Post(ctx, ref, conductor.Outbound{
		Speaker: reviewer,
		Type:    conductor.OutFailure, Text: "I hit the 1h0m0s limit for this playbook. Task `task_01`.", TaskID: "task_01",
	})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedReviews())
	assert.Len(t, h.stub.postedIssueBodies(), 1)
	edits := h.stub.postedIssueEdits()
	require.Len(t, edits, 1, "the failure retitles the comment and does not post itself")
	assert.Contains(t, edits[0], "**"+statusDidNotFinish+"**")
	assertStatusSummary(t, edits[0])
	assert.NotContains(t, edits[0], "task_01")

	id, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "the check holds"})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Len(t, h.stub.postedReviews(), 1)
	assert.Contains(t, asString(h.stub.postedReviews()[0]["body"]), "the check holds")
	edits = h.stub.postedIssueEdits()
	require.Len(t, edits, 2)
	assert.Contains(t, edits[1], "**"+statusReviewed+"**")
	assert.Len(t, h.stub.postedIssueBodies(), 1)
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

func TestNitsApproveOnOneReview(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	_, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	text := "**Nits**\n\nTwo names.\n\n```inline\ninternal/a.go:12\nRename this.\n```\n"
	id, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text, TaskID: "task_1"})
	require.NoError(t, err)
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "APPROVE", created[0]["event"])
	assert.Equal(t, "0123456789abcdef", created[0]["commit_id"])
	assert.NotContains(t, asString(created[0]["body"]), "task_1")
	notes, ok := created[0]["comments"].([]any)
	require.True(t, ok)
	require.Len(t, notes, 1)
	edits := h.stub.postedIssueEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "**"+statusApproved+"**")
	assertStatusSummary(t, edits[0])
	assert.Len(t, h.stub.postedIssueBodies(), 1)

	n, err := strconv.ParseInt(id, 10, 64)
	require.NoError(t, err)
	h.stub.listedReviews = []Review{{ID: n, State: "APPROVED"}}
	id2, err := h.src.Post(ctx, ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Nits**\n\nStill just the name."})
	require.NoError(t, err)
	assert.Equal(t, id, id2)
	assert.Len(t, h.stub.postedReviews(), 1, "a later nit edits the approval")
	assert.Len(t, h.stub.postedIssueBodies(), 1)
	assert.Len(t, h.stub.postedIssueEdits(), 1, "the title stays Approved")
	reviewEdits := h.stub.postedReviewEdits()
	require.Len(t, reviewEdits, 1)
	assert.Contains(t, reviewEdits[0], "Still just the name.")
}

func TestALaterTurnReusesTheStatusComment(t *testing.T) {
	h := newHarness(t)
	h.stub.issueComments = []Comment{{
		ID:   55,
		Body: statusMarker + "\n**" + statusReviewed + "**\n\nFix the review",
	}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	assert.Equal(t, "55", id)
	assert.Empty(t, h.stub.postedIssueBodies())
	edits := h.stub.postedIssueEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "**"+statusInProgress+"**")
	assertStatusSummary(t, edits[0])
}

func TestAReviewQuestionIsNotAPullRequestComment(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutQuestion, Text: "Which file?"})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviews())

	_, err = h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutQuestion, Text: "Which file?"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Which file?"}, h.stub.postedIssueBodies(), "the assistant's question is asked where it was asked")
}

func TestStatusCommentOmitsTheSummaryWhenThePullCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.stub.failPull = true
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	comments := h.stub.postedIssueBodies()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "> [!NOTE]")
	assert.Contains(t, comments[0], "**"+statusInProgress+"**")
	assert.NotContains(t, comments[0], "Files")
}

func TestStatusCommentUsesSingularCounts(t *testing.T) {
	body := statusComment(statusInProgress, "acme", "repo", pullInfo{
		OK: true, Title: "One file", Head: "fix", Base: "dev", SHA: "abcdef0",
		Files: 1, Additions: 1331, Deletions: 0, Commits: 1,
	})
	assert.Contains(t, body, "> [!NOTE]")
	assert.Contains(t, body, "**"+statusInProgress+"**")
	assert.Contains(t, body, "Podium is reading this pull request.")
	assert.Contains(t, body, "### One file")
	assert.Contains(t, body, "[`fix`](https://github.com/acme/repo/tree/fix)")
	assert.Contains(t, body, "[`dev`](https://github.com/acme/repo/tree/dev)")
	assert.Contains(t, body, "[`abcdef0`](https://github.com/acme/repo/commit/abcdef0)")
	assert.Contains(t, body, "| 1 | +1,331 | −0 | 1 |")
	assert.Contains(t, body, "[View the diff](https://github.com/acme/repo/compare/dev...fix)")
	assert.NotContains(t, body, "1 files")

	approved := statusComment(statusApproved, "acme", "repo", pullInfo{OK: true, Title: "One file", Head: "fix", Base: "dev"})
	assert.Contains(t, approved, "> [!NOTE]")
	assert.Contains(t, approved, "**"+statusApproved+"**")
	assert.Contains(t, approved, "Approved. The full review is below.")
	assert.NotContains(t, approved, "[!TIP]")

	changes := statusComment(statusChanges, "", "", pullInfo{OK: true, Head: "fix/name", Base: "dev"})
	assert.Contains(t, changes, "> [!WARNING]")
	assert.Contains(t, changes, "`fix/name` → `dev`")
	assert.NotContains(t, changes, "](")

	none := statusComment(statusDidNotFinish, "acme", "repo", pullInfo{})
	assert.Contains(t, none, "> [!CAUTION]")
	assert.Contains(t, none, "**"+statusDidNotFinish+"**")
	assert.Contains(t, none, "This review stopped before it was posted.")
	assert.NotContains(t, none, "| Files |")
}

func assertStatusSummary(t *testing.T, body string) {
	t.Helper()
	assert.Contains(t, body, statusMarker)
	assert.Contains(t, body, "### Fix the review")
	assert.Contains(t, body, "[`agent/review`](https://github.com/acme/repo/tree/agent/review)")
	assert.Contains(t, body, "[`dev`](https://github.com/acme/repo/tree/dev)")
	assert.Contains(t, body, "[`0123456`](https://github.com/acme/repo/commit/0123456789abcdef)")
	assert.Contains(t, body, "| 3 | +40 | −5 | 2 |")
	assert.Contains(t, body, "[View the diff](https://github.com/acme/repo/compare/dev...agent/review)")
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

func TestANitsReviewApprovesAndPinsTheNotes(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	text := "**Nits**\n\nTwo names.\n\n```inline\ninternal/a.go:12\nRename this.\n```\n"
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text, TaskID: "task_1"})
	require.NoError(t, err)
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "APPROVE", created[0]["event"])
	assert.Equal(t, "0123456789abcdef", created[0]["commit_id"])
	assert.NotContains(t, asString(created[0]["body"]), "```inline")
	assert.NotContains(t, asString(created[0]["body"]), "task_1")
	assert.Contains(t, asString(created[0]["body"]), "Two names.")
	notes, ok := created[0]["comments"].([]any)
	require.True(t, ok)
	require.Len(t, notes, 1)
	note := notes[0].(map[string]any)
	assert.Equal(t, "internal/a.go", note["path"])
	assert.EqualValues(t, 12, note["line"])
	assert.Equal(t, "RIGHT", note["side"])
	assert.Equal(t, "Rename this.", note["body"])
}

func TestChangesNeededStayACommentWithTheNotesOnTheReview(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	text := "**Changes needed**\n\nThe error is dropped.\n\n```inline\ninternal/a.go:4\nThis returns nil on failure.\n```\n"
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text})
	require.NoError(t, err)
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "COMMENT", created[0]["event"])
	notes := created[0]["comments"].([]any)
	require.Len(t, notes, 1)
}

func TestAFollowUpApprovalIsANewReviewWhenTheOldOneWasAComment(t *testing.T) {
	h := newHarness(t)
	h.src.reviews = ReviewMemory{
		Get: func(context.Context, string) (int64, error) { return 77, nil },
	}
	h.stub.listedReviews = []Review{{ID: 77, State: "COMMENTED"}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Approve**\n\nThe fix holds."})
	require.NoError(t, err)
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "APPROVE", created[0]["event"])
	assert.Empty(t, h.stub.postedReviewEdits())
}

func TestAnApprovalFollowUpEditsTheSameReview(t *testing.T) {
	h := newHarness(t)
	h.src.reviews = ReviewMemory{
		Get: func(context.Context, string) (int64, error) { return 77, nil },
	}
	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED"}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Nits**\n\nStill just the name."})
	require.NoError(t, err)
	assert.Equal(t, "77", id)
	assert.Empty(t, h.stub.postedReviews())
	edits := h.stub.postedReviewEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "Still just the name.")
}

func TestChangesNeededAfterAnApprovalRequestsChanges(t *testing.T) {
	h := newHarness(t)
	h.src.reviews = ReviewMemory{
		Get: func(context.Context, string) (int64, error) { return 77, nil },
	}
	h.stub.listedReviews = []Review{{ID: 77, State: "APPROVED"}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	text := "**Changes needed**\n\nThe check is gone.\n\n```inline\ninternal/a.go:4\nThis returns nil on failure.\n```\n"
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text})
	require.NoError(t, err)
	assert.NotEqual(t, "77", id)
	assert.Empty(t, h.stub.postedReviewEdits())
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "REQUEST_CHANGES", created[0]["event"])
	comments := h.stub.postedIssueBodies()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "**"+statusChanges+"**")
	notes, ok := created[0]["comments"].([]any)
	require.True(t, ok)
	require.Len(t, notes, 1)
}

func TestARefusedInlineNoteIsRetriedWithoutIt(t *testing.T) {
	h := newHarness(t)
	h.stub.rejectComments = true
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	text := "**Changes needed**\n\nThe line is gone.\n\n```inline\ninternal/a.go:4\nNot on the diff.\n```\n"
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text})
	require.NoError(t, err)
	created := h.stub.postedReviews()
	require.Len(t, created, 2)
	_, pinned := created[0]["comments"]
	assert.True(t, pinned)
	_, pinned = created[1]["comments"]
	assert.False(t, pinned)
	assert.Contains(t, asString(created[1]["body"]), "internal/a.go:4")
	assert.Equal(t, "COMMENT", created[1]["event"])
}

func TestADroppedReviewIsNotSubmittedAgain(t *testing.T) {
	h := newHarness(t)
	h.stub.dropReview = true
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	text := "**Changes needed**\n\n```inline\ninternal/a.go:4\nNot on the diff.\n```\n"
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: text})
	require.Error(t, err)
	assert.Empty(t, h.stub.postedReviews())
}

func TestAStoredReviewIsUpdatedRatherThanSubmittedAgain(t *testing.T) {
	h := newHarness(t)
	h.src.reviews = ReviewMemory{
		Get: func(context.Context, string) (int64, error) { return 77, nil },
	}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindPullRequest})
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "continued"})
	require.NoError(t, err)
	assert.Equal(t, "77", id)
	assert.Empty(t, h.stub.postedReviews())
	edits := h.stub.postedReviewEdits()
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0], "continued")
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

// A review the assistant delegated from an inline thread is still the review.
func TestAReviewAskedForInAThreadIsTheReview(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Changes needed**\n\nStill racy."})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedReviewInReplyTo())
	require.Len(t, h.stub.postedReviews(), 1)
	assert.Contains(t, asString(h.stub.postedReviews()[0]["body"]), "Still racy.")
}

func TestAnInlineApprovalStillApprovesThePullRequest(t *testing.T) {
	h := newHarness(t)
	h.src.reviews = ReviewMemory{
		Get: func(context.Context, string) (int64, error) { return 77, nil },
	}
	h.stub.listedReviews = []Review{{ID: 77, State: "COMMENTED"}}
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Approve**\n\nEvery finding is closed."})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedReviewInReplyTo())
	created := h.stub.postedReviews()
	require.Len(t, created, 1)
	assert.Equal(t, "APPROVE", created[0]["event"])
}

func TestFetchTranscriptDropsScaffoldingAndKeepsFinals(t *testing.T) {
	h := newHarness(t)
	h.stub.mu.Lock()
	h.stub.issueComments = []Comment{
		{ID: 1, Body: conductor.Placeholder, CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
		{ID: 2, Body: statusMarker + "\n**" + statusInProgress + "**", CreatedAt: time.Date(2026, 9, 14, 12, 0, 30, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
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
// nobody on it asked for. The review is still the review, wherever it was asked for.
func TestAnAnswerToSlackStaysInSlack(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindSlack, SlackChan: "C1", SlackThread: "1.1", SlackTS: "2.2"})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "it adds a retry"})
	require.NoError(t, err)
	assert.Empty(t, h.stub.postedIssueBodies())
	assert.Empty(t, h.stub.postedReviews())

	_, err = h.src.Post(context.Background(), ref, conductor.Outbound{Speaker: reviewer, Type: conductor.OutFinal, Text: "**Approve**\n\nFine."})
	require.NoError(t, err)
	assert.Len(t, h.stub.postedReviews(), 1)
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

	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-r6", readyForReview()).Code)
	evs = h.drain()
	require.Len(t, evs, 1)
	assert.Empty(t, evs[0].Role, "nobody spoke, so nobody's role is asked for")
}

// What the App pushes to a pull request it reviewed — a playbook fixing the review — is
// reviewed again, from where the last review stopped. Anybody else's push is not.
func TestTheAppsOwnPushIsReviewedAgain(t *testing.T) {
	h := newHarness(t)
	push := readyForReview()
	push["action"] = "synchronize"
	push["sender"] = map[string]any{"login": "podium[bot]", "type": "Bot"}

	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-p1", push).Code)
	assert.Empty(t, h.drain(), "a pull request that was never reviewed has nothing to review again")

	h.session("github:acme/repo#12")
	h.src.replaceReview("github:acme/repo#12", 77)
	h.stub.listedReviews = []Review{{ID: 77, State: "CHANGES_REQUESTED", CommitID: "abc1234deadbeef"}}
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-p2", push).Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.True(t, evs[0].Automatic)
	assert.Contains(t, evs[0].Text, "Podium pushed new commits")
	assert.Contains(t, evs[0].Text, "Review only the commits and files changed since that review")
	assert.Contains(t, evs[0].Text, "abc1234deadbeef")
	assert.Contains(t, evs[0].Text, "0123456789abcdef")

	theirs := readyForReview()
	theirs["action"] = "synchronize"
	require.Equal(t, http.StatusOK, postWebhook(t, h, "pull_request", "d-p3", theirs).Code)
	assert.Empty(t, h.drain(), "a person's push is not reviewed: that would be a review per commit")
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
