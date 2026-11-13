package linear

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// The workspace and app user the fixtures in testdata/ were captured in.
const (
	fixtureOrg     = "08ae6d87-5ac4-40ac-93d0-540733439b25"
	fixtureAppUser = "41609a0d-3393-47b0-b78c-c4fc06cafa1c"
	// The fixtures' signatures are not valid for any secret: every test signs its own.
	fakeWebhookSecret = "lin_wh_not-a-real-secret"

	delegationSession = "1c306b38-90b6-43f4-b3c3-d1ac88f9229a"
	mentionSession    = "1ae62bd3-e89b-462c-8f33-36eb67ec4a2a"
	fixtureIssue      = "52966e51-9ffc-456c-a1f3-59e477a288d8"
	fixtureTeam       = "336c8e32-1c03-45fd-aecd-c5ca28f2b4ee"
)

// appHarness is an AppSource against the GraphQL stub, with a clock, a log and metrics a
// test can read.
type appHarness struct {
	src     *AppSource
	stub    *stub
	clock   *fakeClock
	metrics *Metrics
	reg     *prometheus.Registry
	log     *lockedBuffer
	revokes atomic.Int32

	mu sync.Mutex
	// pending is how many events were already waiting for the conductor each time an
	// activity was posted, in order. It is how a test sees the acknowledgement go first.
	pending []int
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newAppHarness(t *testing.T, mutate ...func(*AppOptions)) *appHarness {
	t.Helper()
	h := &appHarness{
		stub:  newStub(t),
		clock: &fakeClock{now: time.UnixMilli(1794516370634).UTC()},
		reg:   prometheus.NewRegistry(),
		log:   &lockedBuffer{},
	}
	h.metrics = NewMetrics(h.reg)
	ts, err := NewTokenSource(TokenSourceOptions{
		TokenURL: h.stub.tokenURL(), ClientID: fakeClientID, ClientSecret: fakeClientSecret,
	})
	require.NoError(t, err)
	client, err := NewClient(ClientOptions{Endpoint: h.stub.endpoint(), Tokens: ts})
	require.NoError(t, err)
	opts := AppOptions{
		Client:         client,
		WebhookSecret:  fakeWebhookSecret,
		Listen:         "127.0.0.1:0",
		Serve:          func(ctx context.Context, _, _ string, _ http.Handler) error { <-ctx.Done(); return nil },
		AppUserID:      fixtureAppUser,
		OrganizationID: fixtureOrg,
		AppName:        "Podium",
		Playbook:       func() string { return "coder" },
		TaskURL:        func(id string) string { return "https://podium.example/tasks/" + id },
		OnRevoked:      func(context.Context) { h.revokes.Add(1) },
		Metrics:        h.metrics,
		Logger:         slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:          h.clock.Now,
	}
	for _, m := range mutate {
		m(&opts)
	}
	h.src, err = NewApp(opts)
	require.NoError(t, err)
	t.Cleanup(h.src.stop)

	n := 0
	h.stub.on("PodiumAgentActivity", func(map[string]any) stubReply {
		h.mu.Lock()
		n++
		h.pending = append(h.pending, len(h.src.events))
		id := "activity-out-" + string(rune('0'+n))
		h.mu.Unlock()
		return stubReply{Data: map[string]any{"agentActivityCreate": map[string]any{
			"success": true, "agentActivity": map[string]any{"id": id},
		}}}
	})
	return h
}

// fixture is a captured payload with its timestamp moved to the harness's now, so it is
// fresh, and edit applied to it.
func (h *appHarness) fixture(t *testing.T, name string, edit ...func(map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["webhookTimestamp"] = h.clock.Now().UnixMilli()
	for _, e := range edit {
		e(doc)
	}
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return out
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// deliver posts a body to the handler the way Linear does, signed with the app's secret.
func (h *appHarness) deliver(body []byte, delivery string) *httptest.ResponseRecorder {
	return h.deliverSigned(body, delivery, sign(fakeWebhookSecret, body))
}

func (h *appHarness) deliverSigned(body []byte, delivery, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerEvent, "AgentSessionEvent")
	if delivery != "" {
		req.Header.Set(headerDelivery, delivery)
	}
	if signature != "" {
		req.Header.Set(headerSignature, signature)
	}
	rec := httptest.NewRecorder()
	h.src.Handler().ServeHTTP(rec, req)
	return rec
}

// settle waits for every queued piece of work to finish.
func (h *appHarness) settle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		h.src.mu.Lock()
		defer h.src.mu.Unlock()
		return len(h.src.queues) == 0
	}, 5*time.Second, time.Millisecond)
}

func (h *appHarness) event(t *testing.T) conductor.InboundEvent {
	t.Helper()
	select {
	case ev := <-h.src.events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event reached the conductor")
		return conductor.InboundEvent{}
	}
}

func (h *appHarness) noEvent(t *testing.T) {
	t.Helper()
	h.settle(t)
	select {
	case ev := <-h.src.events:
		t.Fatalf("an event reached the conductor: %+v", ev)
	default:
	}
}

func (h *appHarness) deliveries(t *testing.T, typ, outcome string) float64 {
	t.Helper()
	return h.counter(t, "podium_agent_linear_webhook_deliveries_total", map[string]string{"type": typ, "outcome": outcome})
}

func (h *appHarness) events(t *testing.T, kind string) float64 {
	t.Helper()
	return h.counter(t, "podium_agent_linear_events_total", map[string]string{"kind": kind})
}

// counter reads one counter back off the registry. Going through Gather rather than through
// testutil keeps this test from adding a dependency, as the conductor's tests do.
func (h *appHarness) counter(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := h.reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metrics:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if labels[l.GetName()] != l.GetValue() {
					continue metrics
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// activities is every agentActivityCreate input the stub saw.
func (h *appHarness) activities() []map[string]any {
	var out []map[string]any
	for _, c := range h.stub.callsOf("PodiumAgentActivity") {
		out = append(out, c.Vars["input"].(map[string]any))
	}
	return out
}

// ---------------------------------------------------------------------------
// the checks a delivery passes before anything is done with it
// ---------------------------------------------------------------------------

func TestALinearDeliveryIsCheckedBeforeAnythingIsDone(t *testing.T) {
	tests := []struct {
		name string
		// edit changes the payload before it is signed.
		edit func(map[string]any)
		// signature replaces the right one when set.
		signature string
		method    string
		status    int
		started   bool
	}{
		{name: "signed and fresh", status: http.StatusOK, started: true},
		{name: "no signature", signature: "-", status: http.StatusUnauthorized},
		{name: "signed with another secret", signature: "other", status: http.StatusUnauthorized},
		{name: "signature not hex", signature: "sha256=zz", status: http.StatusUnauthorized},
		{name: "a minute and a second old", edit: shiftTimestamp(-61 * time.Second), status: http.StatusUnauthorized},
		{name: "a minute and a second ahead", edit: shiftTimestamp(61 * time.Second), status: http.StatusUnauthorized},
		{name: "fifty-nine seconds old", edit: shiftTimestamp(-59 * time.Second), status: http.StatusOK, started: true},
		{name: "no timestamp", edit: func(d map[string]any) { delete(d, "webhookTimestamp") }, status: http.StatusUnauthorized},
		{name: "not a POST", method: http.MethodGet, status: http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAppHarness(t)
			var edits []func(map[string]any)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			body := h.fixture(t, "session_created_delegation", edits...)
			sig := sign(fakeWebhookSecret, body)
			switch tc.signature {
			case "":
			case "-":
				sig = ""
			case "other":
				sig = sign("lin_wh_somebody-else", body)
			default:
				sig = tc.signature
			}
			var rec *httptest.ResponseRecorder
			if tc.method != "" {
				req := httptest.NewRequest(tc.method, WebhookPath, nil)
				rec = httptest.NewRecorder()
				h.src.Handler().ServeHTTP(rec, req)
			} else {
				rec = h.deliverSigned(body, "delivery-1", sig)
			}
			assert.Equal(t, tc.status, rec.Code)
			if tc.started {
				ev := h.event(t)
				assert.Equal(t, SessionKey(delegationSession), ev.SourceKey)
				return
			}
			h.noEvent(t)
			assert.Empty(t, h.activities(), "nothing is said for a delivery that was refused")
			assert.NotContains(t, h.log.String(), fakeWebhookSecret)
			if sig != "" {
				assert.NotContains(t, h.log.String(), sig, "the signature is never logged")
			}
		})
	}
}

func shiftTimestamp(d time.Duration) func(map[string]any) {
	return func(doc map[string]any) {
		doc["webhookTimestamp"] = doc["webhookTimestamp"].(int64) + d.Milliseconds()
	}
}

func TestARefusedDeliveryIsLoggedAndCountedWithoutItsSecrets(t *testing.T) {
	h := newAppHarness(t)
	body := h.fixture(t, "session_created_delegation")
	bad := sign("lin_wh_somebody-else", body)
	rec := h.deliverSigned(body, "delivery-9", bad)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	log := h.log.String()
	assert.Contains(t, log, "linear: webhook")
	assert.Contains(t, log, "outcome=rejected")
	assert.Contains(t, log, "delivery=delivery-9")
	assert.Contains(t, log, `reason="invalid signature"`)
	assert.NotContains(t, log, bad)
	assert.NotContains(t, log, fakeWebhookSecret)
	assert.NotContains(t, log, "Test issue", "an unverified body is not read into the log")
	assert.Equal(t, 1.0, h.deliveries(t, "unknown", DeliveryRejected))

	big := bytes.Repeat([]byte("x"), MaxWebhookBytes+1)
	rec = h.deliver(big, "delivery-10")
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// A delivery Linear sends again under the same id, and a retry of the same prompt under a
// new one, each start one turn.
func TestARepeatedDeliveryStartsOneTurn(t *testing.T) {
	tests := []struct {
		name        string
		second      string
		wantOutcome string
	}{
		{name: "the same delivery id", second: "delivery-1", wantOutcome: DeliveryDuplicate},
		{name: "the same prompt under a new delivery id", second: "delivery-2", wantOutcome: DeliveryDuplicate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAppHarness(t)
			body := h.fixture(t, "session_created_delegation")
			require.Equal(t, http.StatusOK, h.deliver(body, "delivery-1").Code)
			h.event(t)
			h.clock.advance(30 * time.Second)
			body = h.fixture(t, "session_created_delegation")
			require.Equal(t, http.StatusOK, h.deliver(body, tc.second).Code, "a repeat is answered 200, or Linear retries it")
			h.noEvent(t)
			assert.Len(t, h.activities(), 1, "one acknowledgement")
			assert.Equal(t, 1.0, h.deliveries(t, TypeAgentSession, tc.wantOutcome))
		})
	}
}

// What is remembered is forgotten after a while, but not before Linear's last retry.
func TestAPromptIsRememberedPastLinearsLastRetry(t *testing.T) {
	h := newAppHarness(t)
	require.True(t, h.src.firstSighting("activity:a"))
	h.clock.advance(6*time.Hour + time.Minute)
	assert.False(t, h.src.firstSighting("activity:a"), "the six-hour retry is still a repeat")
	h.clock.advance(time.Hour)
	assert.True(t, h.src.firstSighting("activity:a"), "and then it is forgotten")
}

func TestADeliveryForAnotherWorkspaceOrAppUserIsIgnored(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(map[string]any)
		reason string
	}{
		{name: "another workspace", edit: func(d map[string]any) { d["organizationId"] = "org-somebody-else" }, reason: "another workspace"},
		{name: "another app user", edit: func(d map[string]any) { d["appUserId"] = "app-somebody-else" }, reason: "another app user"},
		{name: "no workspace", edit: func(d map[string]any) { delete(d, "organizationId") }, reason: "no workspace or app user"},
		{name: "no app user", edit: func(d map[string]any) { delete(d, "appUserId") }, reason: "no workspace or app user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAppHarness(t)
			rec := h.deliver(h.fixture(t, "session_created_delegation", tc.edit), "delivery-1")
			assert.Equal(t, http.StatusOK, rec.Code, "it is Linear's, so it is answered, and nothing more")
			h.noEvent(t)
			assert.Empty(t, h.activities(), "nothing is said into another workspace's session")
			assert.Contains(t, h.log.String(), "outcome=ignored")
			assert.Contains(t, h.log.String(), tc.reason)
		})
	}
}

// ---------------------------------------------------------------------------
// payload → InboundEvent
// ---------------------------------------------------------------------------

// A delegation says something in the session at once, and only then reaches the conductor.
func TestADelegationIsAcknowledgedAndThenStartsATurn(t *testing.T) {
	h := newAppHarness(t)
	rec := h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	require.Equal(t, http.StatusOK, rec.Code)
	ev := h.event(t)

	acts := h.activities()
	require.Len(t, acts, 1)
	assert.Equal(t, delegationSession, acts[0]["agentSessionId"])
	assert.Equal(t, map[string]any{"type": "thought", "body": acknowledgement}, acts[0]["content"])
	assert.Equal(t, true, acts[0]["ephemeral"])
	h.mu.Lock()
	assert.Equal(t, []int{0}, h.pending, "the thought was posted before the event was handed over")
	h.mu.Unlock()

	assert.Equal(t, conductor.InboundEvent{
		SourceKind:  Kind,
		SourceKey:   "linear:session:" + delegationSession,
		Ref:         fixtureIssue + "/" + fixtureTeam + "/EME-5/" + delegationSession,
		Author:      "Ada Lovelace",
		ExternalID:  "activity:4f5f35e1-86e4-4dc7-9239-414bacd832a5",
		ExternalURL: "https://linear.app/acme/agent-session/" + delegationSession,
		URL:         "https://linear.app/acme/issue/EME-5/test-issue",
		Title:       "EME-5 Test issue",
		TS:          time.Date(2026, 11, 12, 20, 46, 10, 167000000, time.UTC),
		Mentioned:   true,
		// Worded as the poller words an assignment: the ticket.
		Text:       "EME-5 — Test issue",
		RecordText: "Work on [EME-5](https://linear.app/acme/issue/EME-5/test-issue)",
		BriefKind:  conductor.SourceLinear,
		Playbook:   "coder",
	}, ev)
	assert.Empty(t, ev.Login, "nobody is resolved yet")
	assert.Empty(t, ev.SpeakerProvider)
	assert.Equal(t, 1.0, h.events(t, EventDelegation))
	assert.Equal(t, 1.0, h.deliveries(t, TypeAgentSession, DeliveryAccepted))
	assert.Contains(t, h.log.String(), "session="+delegationSession)
	assert.Contains(t, h.log.String(), "issue=EME-5")
}

func TestADelegationCarriesTheIssuesDescription(t *testing.T) {
	h := newAppHarness(t)
	body := h.fixture(t, "session_created_delegation", func(d map[string]any) {
		d["agentSession"].(map[string]any)["issue"].(map[string]any)["description"] = "Make the build green."
	})
	h.deliver(body, "delivery-1")
	assert.Equal(t, "EME-5 — Test issue\n\nMake the build green.", h.event(t).Text)
}

// A mention's text is the comment, without the mention of the app in front of it.
func TestAMentionStartsATurnWithTheComment(t *testing.T) {
	h := newAppHarness(t)
	h.deliver(h.fixture(t, "session_created_mention"), "delivery-1")
	ev := h.event(t)
	assert.Equal(t, "what is the time?", ev.Text)
	assert.Equal(t, "@podium what is the time?", ev.RecordText)
	assert.Equal(t, "linear:session:"+mentionSession, ev.SourceKey)
	assert.Equal(t, fixtureIssue+"/"+fixtureTeam+"/EME-5/"+mentionSession, ev.Ref)
	assert.Equal(t, "activity:5e1c605c-1cf6-427c-a132-8ea2f36daef7", ev.ExternalID)
	assert.True(t, ev.Mentioned)
	assert.NotContains(t, ev.Text, "primary-directive-thread", "Linear's prompt context is not the instruction")
	assert.Len(t, h.activities(), 1, "a mention opens a session, so it is acknowledged too")
	assert.Equal(t, 1.0, h.events(t, EventMention))
}

func TestAMentionOfSomebodyElseIsKept(t *testing.T) {
	h := newAppHarness(t)
	assert.Equal(t, "@ada can you look", h.src.stripMention("@ada can you look"))
	assert.Equal(t, "can you look", h.src.stripMention("@Podium can you look"))
	bare := h.fixture(t, "session_created_mention", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["content"].(map[string]any)["body"] = "@podium"
	})
	h.deliver(bare, "delivery-1")
	assert.Equal(t, "EME-5 — Test issue", h.event(t).Text, "a bare mention asks for the ticket")
}

// A follow-up is what the person said in the session, and needs no acknowledgement: the
// session is already answered.
func TestAFollowUpIsThePromptsBody(t *testing.T) {
	h := newAppHarness(t)
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-1")
	ev := h.event(t)
	assert.Equal(t, "What is the time?", ev.Text)
	assert.Empty(t, ev.RecordText)
	assert.Equal(t, "linear:session:"+delegationSession, ev.SourceKey)
	assert.Equal(t, "activity:663ba1da-7fac-4d15-a5e0-1556d70ccb3b", ev.ExternalID)
	assert.Equal(t, "Ada Lovelace", ev.Author)
	assert.Equal(t, "coder", ev.Playbook)
	assert.Empty(t, h.activities())
	assert.Equal(t, 1.0, h.events(t, EventFollowup))
}

// Two prompts in one session reach the conductor in the order Linear sent them.
func TestPromptsInOneSessionKeepTheirOrder(t *testing.T) {
	h := newAppHarness(t)
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	for i, text := range []string{"first", "second", "third"} {
		body := h.fixture(t, "session_prompted_reply", func(d map[string]any) {
			act := d["agentActivity"].(map[string]any)
			act["id"] = "prompt-" + text
			act["content"].(map[string]any)["body"] = text
		})
		require.Equal(t, http.StatusOK, h.deliver(body, "delivery-"+string(rune('2'+i))).Code)
	}
	var texts []string
	for range 4 {
		texts = append(texts, h.event(t).Text)
	}
	assert.Equal(t, []string{"EME-5 — Test issue", "first", "second", "third"}, texts)
}

// A stop asks for a turn to be cancelled, which this source cannot do yet. It must not start
// a turn of its own.
func TestAStopStartsNothing(t *testing.T) {
	h := newAppHarness(t)
	rec := h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	assert.Equal(t, http.StatusOK, rec.Code)
	h.noEvent(t)
	assert.Empty(t, h.activities())
	assert.Contains(t, h.log.String(), "level=INFO")
	assert.Contains(t, h.log.String(), `reason="stop signal"`)
	assert.Contains(t, h.log.String(), "signal=stop")
}

// Every delegation and mention also arrives as a notification, which would start it twice.
func TestANotificationStartsNothing(t *testing.T) {
	for _, name := range []string{"notification_assigned", "notification_comment_mention"} {
		t.Run(name, func(t *testing.T) {
			h := newAppHarness(t)
			assert.Equal(t, http.StatusOK, h.deliver(h.fixture(t, name), "delivery-1").Code)
			h.noEvent(t)
			assert.Equal(t, 1.0, h.deliveries(t, TypeAppNotification, DeliveryIgnored))
		})
	}
}

func TestAPermissionChangeIsLogged(t *testing.T) {
	h := newAppHarness(t)
	body, err := json.Marshal(map[string]any{
		"type": TypePermissionChange, "action": "teamAccessChanged",
		"organizationId": fixtureOrg, "appUserId": fixtureAppUser,
		"webhookTimestamp": h.clock.Now().UnixMilli(),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, h.deliver(body, "delivery-1").Code)
	h.noEvent(t)
	assert.Contains(t, h.log.String(), "level=INFO")
	assert.Contains(t, h.log.String(), "teamAccessChanged")
	assert.Equal(t, 1.0, h.deliveries(t, TypePermissionChange, DeliveryLogged))
}

// Linear revoking the app is said loudly and recorded, and the source starts nothing after it.
func TestARevokedAppStartsNothingMore(t *testing.T) {
	h := newAppHarness(t)
	revoke := func(org string) []byte {
		body, err := json.Marshal(map[string]any{
			"type": TypeOAuthApp, "action": ActionRevoked,
			"organizationId": org, "oauthClientId": fakeClientID,
			"webhookTimestamp": h.clock.Now().UnixMilli(),
		})
		require.NoError(t, err)
		return body
	}
	h.deliver(revoke("org-somebody-else"), "delivery-0")
	h.settle(t)
	assert.False(t, h.src.Revoked(), "another workspace revoking the app is not this one's business")

	assert.Equal(t, http.StatusOK, h.deliver(revoke(fixtureOrg), "delivery-1").Code)
	h.settle(t)
	assert.True(t, h.src.Revoked())
	assert.Equal(t, int32(1), h.revokes.Load(), "the saved connection is told")
	assert.Contains(t, h.log.String(), "level=ERROR")
	assert.Contains(t, h.log.String(), "outcome=revoked")

	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-2")
	h.noEvent(t)
	assert.Empty(t, h.activities())
	assert.Equal(t, 1.0, h.deliveries(t, TypeAgentSession, DeliveryIgnored))
}

func TestAnAppSavedRevokedStartsRevoked(t *testing.T) {
	h := newAppHarness(t, func(o *AppOptions) { o.Revoked = true })
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	h.noEvent(t)
	assert.True(t, h.src.Revoked())
}

// ---------------------------------------------------------------------------
// what a turn says goes into the session
// ---------------------------------------------------------------------------

func sessionRef() string { return SessionRef(fixtureIssue, fixtureTeam, "EME-5", delegationSession) }

func TestEachKindOfPostIsTheMatchingActivity(t *testing.T) {
	tests := []struct {
		name      string
		out       conductor.Outbound
		typ       string
		body      string
		ephemeral bool
	}{
		{"the placeholder", conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder},
			"thought", "Working on it.", true},
		{"progress", conductor.Outbound{Type: conductor.OutProgress, Text: conductor.ProgressPrefix + "reading the tests"},
			"thought", "reading the tests", true},
		{"an answer", conductor.Outbound{Type: conductor.OutFinal, Text: "It is noon.", TaskID: "task-1"},
			"response", "It is noon.\n\n_Podium task task-1_", false},
		{"a failure", conductor.Outbound{Type: conductor.OutFailure, Text: "The work never started."},
			"error", "The work never started.", false},
		{"a question", conductor.Outbound{Type: conductor.OutQuestion, Text: "Which branch?"},
			"elicitation", "Which branch?", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAppHarness(t)
			id, err := h.src.Post(context.Background(), sessionRef(), tc.out)
			require.NoError(t, err)
			assert.Equal(t, "activity-out-1", id, "the activity's id is what the conductor edits by")
			acts := h.activities()
			require.Len(t, acts, 1)
			assert.Equal(t, delegationSession, acts[0]["agentSessionId"])
			assert.Equal(t, map[string]any{"type": tc.typ, "body": tc.body}, acts[0]["content"])
			if tc.ephemeral {
				assert.Equal(t, true, acts[0]["ephemeral"])
			} else {
				assert.NotContains(t, acts[0], "ephemeral", "only a thought or an action may be ephemeral")
			}
			assert.Empty(t, h.stub.callsOf("PodiumAddComment"), "a session is answered with activities, not comments")
		})
	}
}

// A progress edit is a new ephemeral thought, which replaces the last one on screen, at most
// one every EditThrottle.
func TestAProgressEditIsANewThoughtThrottled(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	id, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)

	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ one"}))
	assert.Len(t, h.activities(), 1, "inside the throttle the edit is superseded")
	h.clock.advance(EditThrottle)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ two"}))
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "thought", "body": "two"}, acts[1]["content"])
	assert.Equal(t, true, acts[1]["ephemeral"])
}

// The session's state is Linear's to derive from the last activity: a reaction sends nothing.
func TestAReactionSendsNothingIntoASession(t *testing.T) {
	h := newAppHarness(t)
	for _, r := range []conductor.Reaction{
		conductor.ReactionWorking, conductor.ReactionDone, conductor.ReactionFailed,
		conductor.ReactionAwaiting, conductor.ReactionRefused,
	} {
		require.NoError(t, h.src.React(context.Background(), sessionRef(), r))
	}
	assert.Empty(t, h.stub.seen())
	assert.Error(t, h.src.React(context.Background(), sessionRef(), "sparkles"))
}

// A turn the poller started before the app took over has a three-part ref. It is answered on
// the issue, as a comment, rather than lost.
func TestARefWithNoSessionIsAnsweredWithAComment(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-1"}},
	})
	ref := Ref(fixtureIssue, fixtureTeam, "EME-5")
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "Done.", TaskID: "task-1"})
	require.NoError(t, err)
	assert.Equal(t, "comment-1", id)
	calls := h.stub.callsOf("PodiumAddComment")
	require.Len(t, calls, 1)
	assert.Equal(t, fixtureIssue, calls[0].Vars["issueId"])
	assert.Equal(t, "Done.\n\n_Podium task task-1_", calls[0].Vars["body"])
	assert.Empty(t, h.activities())
}

// An attachment after the answer is its own response, so the session stays complete.
func TestAnAttachmentIsLinkedInAResponse(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumUploadFile", map[string]any{"fileUpload": map[string]any{
		"success": true,
		"uploadFile": map[string]any{
			"uploadUrl": h.stub.uploadURL(), "assetUrl": "https://uploads.linear.app/shot.png",
			"headers": []map[string]any{},
		},
	}})
	ctx := context.Background()
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Here it is.", TaskID: "task-1"})
	require.NoError(t, err)
	require.NoError(t, h.src.Attach(ctx, sessionRef(), conductor.Attachment{
		Name: "shot.png", ContentType: "image/png", Size: 3, Body: strings.NewReader("png"),
	}))
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{
		"type": "response", "body": "![shot.png](https://uploads.linear.app/shot.png)",
	}, acts[1]["content"], "the answer above it already names the task")
	require.Len(t, h.stub.seenUploads(), 1)

	// Too large to upload: a link to the task, and after a failure, an error.
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "It broke."})
	require.NoError(t, err)
	require.NoError(t, h.src.Attach(ctx, sessionRef(), conductor.Attachment{
		Name: "huge.mp4", Size: MaxUploadBytes + 1, Body: strings.NewReader(""),
	}))
	acts = h.activities()
	require.Len(t, acts, 4)
	content := acts[3]["content"].(map[string]any)
	assert.Equal(t, "error", content["type"])
	assert.Contains(t, content["body"], "https://podium.example/tasks/task-1")
}

// The transcript is the issue and its comments, as the poller reads it, without the comment
// Linear opens a session's thread with. The app's own answers are the assistant's.
func TestTheAppTranscriptSkipsTheSessionThreadAndKeepsItsAnswers(t *testing.T) {
	h := newAppHarness(t)
	at := time.Date(2026, 11, 12, 20, 46, 0, 0, time.UTC)
	opener := botComment("c-0", "This thread is for an agent session with podium.", at)
	app := map[string]any{
		"id": "c-2", "body": "It is noon.\n\n_Podium task task-1_", "createdAt": iso(at.Add(2 * time.Minute)),
		"parentId": "c-0", "quotedText": nil, "botActor": nil,
		"user": map[string]any{"id": fixtureAppUser, "name": "Podium", "displayName": "podium", "isMe": false},
	}
	node := issueNode(fixtureIssue, "EME-5", "Test issue", StateTypeBacklog, at,
		opener, humanComment("c-1", "What is the time?", at.Add(time.Minute)), app)
	node["comments"].(map[string]any)["pageInfo"] = map[string]any{"hasNextPage": false, "endCursor": ""}
	h.stub.reply("PodiumIssue", map[string]any{"issue": node})

	entries, err := h.src.FetchTranscript(context.Background(), sessionRef())
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "EME-5 — Test issue\n\nthe description of EME-5", entries[0].Text)
	assert.Equal(t, conductor.RoleUser, entries[1].Role)
	assert.Equal(t, "What is the time?", entries[1].Text)
	assert.Equal(t, conductor.RoleAssistant, entries[2].Role, "the app user's comment is its own answer")
	for _, e := range entries {
		assert.NotContains(t, e.Text, "This thread is for an agent session")
	}
	assert.Equal(t, fixtureIssue, h.stub.callsOf("PodiumIssue")[0].Vars["id"])
}

// ---------------------------------------------------------------------------
// refs, construction and Run
// ---------------------------------------------------------------------------

func TestASessionRefCarriesTheSessionAndAPolledRefStillParses(t *testing.T) {
	issue, team, ident, sess, err := ParseSessionRef(SessionRef("i", "t", "ENG-1", "s"))
	require.NoError(t, err)
	assert.Equal(t, []string{"i", "t", "ENG-1", "s"}, []string{issue, team, ident, sess})

	issue, team, ident, sess, err = ParseSessionRef(Ref("i", "t", "ENG-1"))
	require.NoError(t, err)
	assert.Equal(t, []string{"i", "t", "ENG-1", ""}, []string{issue, team, ident, sess})

	issue, team, ident, err = ParseRef(SessionRef("i", "t", "ENG-1", "s"))
	require.NoError(t, err, "the poller's ParseRef reads a session ref's first three parts")
	assert.Equal(t, []string{"i", "t", "ENG-1"}, []string{issue, team, ident})

	for _, bad := range []string{"", "i/t", "/t/ENG-1/s", "i/t/ENG-1/s/x"} {
		_, _, _, _, err := ParseSessionRef(bad)
		assert.Error(t, err, bad)
	}
}

func TestAnAppWithNoLinearPlaybookIsRefused(t *testing.T) {
	ts, err := NewTokenSource(TokenSourceOptions{TokenURL: "https://api.linear.app/oauth/token", ClientID: "id", ClientSecret: "s"})
	require.NoError(t, err)
	client, err := NewClient(ClientOptions{Endpoint: "https://api.linear.app/graphql", Tokens: ts})
	require.NoError(t, err)
	base := AppOptions{
		Client: client, WebhookSecret: fakeWebhookSecret, Listen: "0.0.0.0:8091",
		Serve:     func(context.Context, string, string, http.Handler) error { return nil },
		AppUserID: fixtureAppUser, OrganizationID: fixtureOrg,
		Playbook: func() string { return "" },
	}
	_, err = NewApp(base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no playbook sets `linear: true`", "the poller's words")
	assert.Contains(t, err.Error(), "Settings → Connections")

	base.Playbook = func() string { return "coder" }
	base.AppUserID = ""
	_, err = NewApp(base)
	assert.ErrorContains(t, err, "Save it again")
}

// Run hands the handler to the webhook listener's owner, on the app's address and path, and
// work in flight when it stops is let go without emitting on a closed channel.
func TestRunServesTheWebhookAndStopsCleanly(t *testing.T) {
	type mount struct {
		listen, path string
		h            http.Handler
	}
	mounted := make(chan mount, 1)
	h := newAppHarness(t, func(o *AppOptions) {
		o.Serve = func(ctx context.Context, listen, path string, handler http.Handler) error {
			mounted <- mount{listen, path, handler}
			<-ctx.Done()
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.src.Run(ctx) }()
	m := <-mounted
	assert.Equal(t, "127.0.0.1:0", m.listen)
	assert.Equal(t, WebhookPath, m.path)

	// Nobody drains the events, so the work blocks until Run stops it.
	for i := range cap(h.src.events) + 2 {
		body := h.fixture(t, "session_prompted_reply", func(d map[string]any) {
			d["agentActivity"].(map[string]any)["id"] = "prompt-" + string(rune('a'+i))
		})
		req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
		req.Header.Set(headerSignature, sign(fakeWebhookSecret, body))
		rec := httptest.NewRecorder()
		m.h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	// The work is queued behind the session, so wait for it to fill the channel before stopping.
	require.Eventually(t, func() bool { return len(h.src.events) == cap(h.src.events) },
		5*time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	n := 0
	for range h.src.Events() {
		n++
	}
	assert.Equal(t, cap(h.src.events), n, "what was handed over stays; the rest is dropped, not panicked on")
	rec := h.deliver(h.fixture(t, "session_created_mention"), "delivery-late")
	assert.Equal(t, http.StatusOK, rec.Code, "a stopped source still answers, and starts nothing")
}
