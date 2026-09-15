package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	src, err := New(Options{
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
	payload := map[string]any{
		"action":       "created",
		"installation": map[string]any{"id": 42},
		"repository": map[string]any{
			"name": "repo", "owner": map[string]any{"login": "acme"},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
		"comment": map[string]any{
			"id": 9, "body": "fixed", "created_at": "2026-09-14T12:00:00Z",
			"path": "a.go", "line": 4, "in_reply_to_id": 8,
			"user": map[string]any{"login": "alice", "type": "User"},
		},
		"pull_request": map[string]any{"number": 12},
	}
	rec := postWebhook(t, h, "pull_request_review_comment", "d-reply", payload)
	assert.Equal(t, http.StatusOK, rec.Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.Equal(t, "github:acme/repo#12", evs[0].SourceKey)
	assert.Contains(t, evs[0].Text, "a.go:4")
	assert.Contains(t, evs[0].Text, "fixed")
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

func TestPostOnAnIssueCommentTriggerCreatesAnIssueComment(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindIssueComment, CommentID: 7})
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "looks good", TaskID: "task_1"})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	bodies := h.stub.postedIssueBodies()
	require.Len(t, bodies, 1)
	assert.Contains(t, bodies[0], "looks good")
	assert.Contains(t, bodies[0], "task_1")
}

func TestPostOnAReviewCommentTriggerRepliesInThread(t *testing.T) {
	h := newHarness(t)
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindReviewComment, CommentID: 88})
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "agreed"})
	require.NoError(t, err)
	assert.Equal(t, []int64{88}, h.stub.postedReviewInReplyTo())
}

func TestFetchTranscriptDropsScaffoldingAndKeepsFinals(t *testing.T) {
	h := newHarness(t)
	h.stub.mu.Lock()
	h.stub.issueComments = []Comment{
		{ID: 1, Body: conductor.Placeholder, CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
		{ID: 2, Body: "the review", CreatedAt: time.Date(2026, 9, 14, 12, 1, 0, 0, time.UTC), User: User{Login: "podium[bot]", Type: "Bot"}},
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
