package linear

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
)

// ---------------------------------------------------------------------------
// a source wired to a stub
// ---------------------------------------------------------------------------

// harness is a Source with the two injected dependencies replaced by recorders, and a
// clock a test can move by hand.
type harness struct {
	src   *Source
	stub  *stub
	clock *fakeClock

	mu sync.Mutex
	// sessions is the conductor's sessions table, as the source is allowed to see it.
	sessions map[string]time.Time
	known    map[string]bool
	// cursor is the linear_cursor row.
	cursor time.Time
	// puts is every write of the watermark, in order.
	puts []time.Time
	// cursorErr, when set, is what Get returns.
	cursorErr error
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		stub:     newStub(t),
		clock:    &fakeClock{now: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)},
		sessions: map[string]time.Time{},
		known:    map[string]bool{},
	}
	src, err := New(Options{
		APIKey:       fakeAPIKey,
		Endpoint:     h.stub.endpoint(),
		PollInterval: 30 * time.Second,
		Playbook:     func() string { return "coder" },
		TaskURL:      func(id string) string { return "https://podium.example/tasks/" + id },
		Clock:        h.clock.Now,
		Session: func(_ context.Context, key string) (time.Time, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.sessions[key], h.known[key]
		},
		Cursor: CursorStore{
			Get: func(context.Context) (time.Time, error) {
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.cursor, h.cursorErr
			},
			Put: func(_ context.Context, at time.Time) error {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.cursor = at
				h.puts = append(h.puts, at)
				return nil
			},
		},
	})
	require.NoError(t, err)
	// Run is what learns this in production; the tick tests call tick directly.
	src.me = User{ID: botUserID, Name: "podium", DisplayName: "Podium", IsMe: true}
	h.src = src
	return h
}

// session records that a conversation exists, with the time its last turn started.
func (h *harness) session(key string, lastTurnAt time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.known[key] = true
	h.sessions[key] = lastTurnAt
}

// drain collects every event the source has emitted so far.
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

func (h *harness) writes() []time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Time(nil), h.puts...)
}

// ---------------------------------------------------------------------------
// the client
// ---------------------------------------------------------------------------

// TestTheAPIKeyIsSentBareWithNoBearerPrefix pins the one thing about Linear's auth that is
// easy to get wrong: a personal API key goes in Authorization verbatim. Bearer is for OAuth
// access tokens, and using it gets a 401 that reads like a wrong key.
func TestTheAPIKeyIsSentBareWithNoBearerPrefix(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumViewer", viewerData())

	me, err := h.src.client.Viewer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, botUserID, me.ID)
	assert.Equal(t, "Podium", me.DisplayName)

	auths := h.stub.seenAuths()
	require.Len(t, auths, 1)
	assert.Equal(t, fakeAPIKey, auths[0])
	assert.NotContains(t, auths[0], "Bearer")
}

// TestAViewerFailureIsFatalAtStartup is the acceptance item: a Linear key that is set and
// does not work stops the process, naming Linear, rather than being discovered an hour
// later by nobody picking a ticket up.
func TestAViewerFailureIsFatalAtStartup(t *testing.T) {
	h := newHarness(t)
	h.stub.on("PodiumViewer", func(map[string]any) stubReply {
		return stubReply{
			Message: "Authentication required, not authenticated",
			Code:    CodeAuthentication,
			Status:  http.StatusUnauthorized,
		}
	})

	err := h.src.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear")
	assert.Contains(t, err.Error(), "PODIUM_AGENT_LINEAR_API_KEY")
	assert.Contains(t, err.Error(), "Authentication required")
}

// TestAnAuthenticationErrorIsRecognisedByCodeAndStatus covers both halves of the check, so
// neither can rot alone.
func TestAnAuthenticationErrorIsRecognisedByCodeAndStatus(t *testing.T) {
	byCode := &GraphQLError{Code: CodeAuthentication, Status: http.StatusOK}
	byStatus := &GraphQLError{Status: http.StatusUnauthorized}
	assert.True(t, byCode.Unauthorized())
	assert.True(t, byStatus.Unauthorized())
	assert.False(t, (&GraphQLError{Status: http.StatusOK}).Unauthorized())
}

// TestThrottlingArrivesAsHTTP400 is the trap: Linear rejects a throttled request with 400
// and a RATELIMITED code, not with 429. Both are accepted, and a plain 400 is not.
func TestThrottlingArrivesAsHTTP400(t *testing.T) {
	assert.True(t, (&GraphQLError{Code: CodeRateLimited, Status: http.StatusBadRequest}).RateLimited())
	assert.True(t, (&GraphQLError{Status: http.StatusTooManyRequests}).RateLimited())
	assert.False(t, (&GraphQLError{Status: http.StatusBadRequest, Message: "bad query"}).RateLimited())
}

// TestANonJSONResponseIsReportedWithItsStatus is the egress-proxy case: something answered,
// it was not Linear, and the status plus a bounded quote is all there is to say.
func TestANonJSONResponseIsReportedWithItsStatus(t *testing.T) {
	h := newHarness(t)
	h.stub.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>proxy login</html>"))
	})

	_, err := h.src.client.Viewer(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
	assert.Contains(t, err.Error(), "proxy login")
}

// ---------------------------------------------------------------------------
// the poll
// ---------------------------------------------------------------------------

// TestAPollPageYieldsAnAssignmentAndAFollowUp is the whole derivation in one page: an issue
// with no session is a fresh assignment, and an issue with one is a follow-up carrying only
// what was said after the last turn started.
func TestAPollPageYieldsAnAssignmentAndAFollowUp(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	lastTurn := now.Add(-10 * time.Minute)
	h.session(SourceKey("issue-known"), lastTurn)

	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-new", "ENG-1", "Add a button", StateTypeUnstarted, now.Add(-time.Minute)),
		issueNode("issue-known", "ENG-2", "Fix the button", StateTypeStarted, now,
			humanComment("c-old", "said before the last turn", lastTurn.Add(-time.Minute)),
			humanComment("c-new", "and now make it blue", lastTurn.Add(time.Minute)),
		),
	))

	h.src.tick(context.Background())
	events := h.drain()
	require.Len(t, events, 2)

	assignment := events[0]
	assert.Equal(t, Kind, assignment.SourceKind)
	assert.Equal(t, conductor.SourceLinear, assignment.BriefKind)
	assert.Equal(t, "linear:issue-new", assignment.SourceKey)
	assert.Equal(t, "issue-new/"+teamID+"/ENG-1", assignment.Ref)
	assert.Equal(t, "coder", assignment.Playbook, "the source names the playbook; a ticket has no /prefix")
	assert.Equal(t, "https://linear.app/acme/issue/ENG-1", assignment.URL)
	assert.Empty(t, assignment.Env, "a real source may never put environment on a task spec")
	assert.Empty(t, assignment.Channel, "a ticket has no channel to route on")
	// The identifier leads the text: the runtime's prompt renders the transcript and the
	// instruction, never source.ref, and the coder playbook is told to branch from it.
	assert.Equal(t, "ENG-1 — Add a button\n\nthe description of ENG-1", assignment.Text)

	followup := events[1]
	assert.Equal(t, "linear:issue-known", followup.SourceKey)
	assert.Equal(t, "and now make it blue", followup.Text,
		"only what was said after the last turn started")
	assert.Equal(t, "Alice", followup.Author)
}

// TestTwoNewCommentsInOneTickAreOneTurn is the acceptance item: two consecutive follow-up
// comments inside one poll interval produce exactly one event, oldest first.
func TestTwoNewCommentsInOneTickAreOneTurn(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	lastTurn := now.Add(-time.Hour)
	h.session(SourceKey("issue-1"), lastTurn)

	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-1", "ENG-3", "Two comments", StateTypeStarted, now,
			// Deliberately out of order on the wire: the connection is newest-first.
			humanComment("c-2", "and also the footer", now.Add(-time.Minute)),
			humanComment("c-1", "change the header", now.Add(-2*time.Minute)),
		),
	))

	h.src.tick(context.Background())
	events := h.drain()
	require.Len(t, events, 1, "two comments in one tick are one turn")
	assert.Equal(t, "change the header\n\nand also the footer", events[0].Text)
}

// TestTheBotsOwnCommentsNeverStartATurn covers all three ways Linear reports a comment that
// is not a human: isMe on the user, the bot's own user id, and — the one that matters — no
// user at all, which is what an integration or an agent comment looks like.
func TestTheBotsOwnCommentsNeverStartATurn(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	lastTurn := now.Add(-time.Hour)
	h.session(SourceKey("issue-1"), lastTurn)

	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-1", "ENG-4", "Quiet", StateTypeStarted, now,
			selfComment("c-1", "the answer I posted last turn", now.Add(-3*time.Minute)),
			botComment("c-2", "an integration said something", now.Add(-2*time.Minute)),
			// A whitespace-only human comment is not something said either.
			humanComment("c-3", "   ", now.Add(-time.Minute)),
		),
	))

	h.src.tick(context.Background())
	assert.Empty(t, h.drain(), "nothing a human said, so no turn")
}

// TestAFinishedIssueIsIgnored: a completed or cancelled ticket gets no turns, however it
// was edited — and an updatedAt watermark fires on any edit at all.
func TestAFinishedIssueIsIgnored(t *testing.T) {
	for _, stateType := range []string{StateTypeCompleted, StateTypeCanceled} {
		t.Run(stateType, func(t *testing.T) {
			h := newHarness(t)
			now := h.clock.Now()
			h.stub.reply("PodiumAssignedIssues", page(false, "",
				issueNode("issue-1", "ENG-5", "Already done", stateType, now),
			))
			h.src.tick(context.Background())
			assert.Empty(t, h.drain())
			// The watermark still advances: the issue was seen, and re-reading it every
			// tick forever is the alternative.
			require.Len(t, h.writes(), 1)
		})
	}
}

// TestAPageIsFollowedWithinOneTick: hasNextPage is followed with the endCursor, in the same
// tick, and the watermark is written once at the end.
func TestAPageIsFollowedWithinOneTick(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()

	var mu sync.Mutex
	calls := 0
	h.stub.on("PodiumAssignedIssues", func(vars map[string]any) stubReply {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			assert.Nil(t, vars["after"], "the first page asks for no cursor")
			// Descending order: the newest issue is on the first page.
			return stubReply{Data: page(true, "cursor-1",
				issueNode("issue-a", "ENG-6", "Newest", StateTypeUnstarted, now))}
		}
		assert.Equal(t, "cursor-1", vars["after"])
		return stubReply{Data: page(false, "",
			issueNode("issue-b", "ENG-7", "Older", StateTypeUnstarted, now.Add(-time.Hour)))}
	})

	h.src.tick(context.Background())
	events := h.drain()
	require.Len(t, events, 2)
	assert.Equal(t, "linear:issue-a", events[0].SourceKey)
	assert.Equal(t, "linear:issue-b", events[1].SourceKey)
	assert.Len(t, h.stub.callsOf("PodiumAssignedIssues"), 2)

	// One write, of the newest updatedAt seen anywhere in the tick. Writing per page would
	// mean a crash after page one skipped every older page for good, because the order is
	// descending and there is no ascending option.
	writes := h.writes()
	require.Len(t, writes, 1)
	assert.True(t, writes[0].Equal(now.UTC()), "the watermark is the newest updatedAt, got %s", writes[0])
}

// TestTheWatermarkIsNotAdvancedWhenAPageFails is the crash-safety rule stated as a test: if
// any page of a tick fails, nothing is recorded and the whole tick replays.
func TestTheWatermarkIsNotAdvancedWhenAPageFails(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()

	var mu sync.Mutex
	calls := 0
	h.stub.on("PodiumAssignedIssues", func(map[string]any) stubReply {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return stubReply{Data: page(true, "cursor-1",
				issueNode("issue-a", "ENG-8", "Newest", StateTypeUnstarted, now))}
		}
		return stubReply{Message: "Internal error", Code: "INTERNAL_SERVER_ERROR", Status: http.StatusOK}
	})

	h.src.tick(context.Background())
	assert.Len(t, h.drain(), 1, "the first page's events were already handed over")
	assert.Empty(t, h.writes(), "a tick that did not finish must replay, not skip")
}

// TestTheFirstTickLooksBackADay: with no cursor the source asks for the last 24 hours, so
// an assignment made while the conductor was down for a day is still picked up and nothing
// older is.
func TestTheFirstTickLooksBackADay(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumAssignedIssues", page(false, ""))

	h.src.tick(context.Background())
	calls := h.stub.callsOf("PodiumAssignedIssues")
	require.Len(t, calls, 1)
	since, err := time.Parse(time.RFC3339Nano, calls[0].Vars["since"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, h.clock.Now().Add(-FirstRunLookback), since, time.Second)
	assert.Equal(t, botUserID, calls[0].Vars["me"])
}

// TestASecondTickAsksFromTheStoredWatermark.
func TestASecondTickAsksFromTheStoredWatermark(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-a", "ENG-9", "One", StateTypeUnstarted, now),
	))
	h.src.tick(context.Background())
	h.drain()

	h.stub.reply("PodiumAssignedIssues", page(false, ""))
	h.src.tick(context.Background())

	calls := h.stub.callsOf("PodiumAssignedIssues")
	require.Len(t, calls, 2)
	since, err := time.Parse(time.RFC3339Nano, calls[1].Vars["since"].(string))
	require.NoError(t, err)
	assert.True(t, since.Equal(now.UTC()), "the second tick starts where the first stopped")
}

// TestAnAssignmentIsEmittedOnceEvenBeforeTheSessionExists closes the window between the
// emit and the conductor's UpsertSession: two ticks inside it must not make two turns.
func TestAnAssignmentIsEmittedOnceEvenBeforeTheSessionExists(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-a", "ENG-10", "Assign me", StateTypeUnstarted, now),
	))

	h.src.tick(context.Background())
	// The watermark moved, so rewind it the way an issue edited again would.
	h.mu.Lock()
	h.cursor = now.Add(-time.Hour)
	h.mu.Unlock()
	h.src.tick(context.Background())

	assert.Len(t, h.drain(), 1, "one assignment, however often the issue is re-read")
}

// TestARateLimitBacksOffAndThenRecovers: throttling starts at the poll interval, doubles,
// caps at five minutes, and is cleared by a tick that works.
func TestARateLimitBacksOffAndThenRecovers(t *testing.T) {
	h := newHarness(t)
	h.stub.on("PodiumAssignedIssues", func(map[string]any) stubReply {
		return stubReply{
			Message: "Rate limit exceeded",
			Code:    CodeRateLimited,
			Status:  http.StatusBadRequest,
		}
	})

	assert.Equal(t, 30*time.Second, h.src.delay(), "no backoff to begin with")
	h.src.tick(context.Background())
	assert.Equal(t, 30*time.Second, h.src.delay(), "the first backoff is the poll interval")
	h.src.tick(context.Background())
	assert.Equal(t, time.Minute, h.src.delay())
	for i := 0; i < 10; i++ {
		h.src.tick(context.Background())
	}
	assert.Equal(t, MaxBackoff, h.src.delay(), "capped at five minutes")
	assert.Empty(t, h.writes(), "a throttled tick records no watermark")

	h.stub.reply("PodiumAssignedIssues", page(false, ""))
	h.src.tick(context.Background())
	assert.Equal(t, 30*time.Second, h.src.delay(), "a tick that worked clears the backoff")
}

// TestAnHTTP429AlsoBacksOff: the documentation of which status a throttle uses disagrees
// with itself, so both are honoured.
func TestAnHTTP429AlsoBacksOff(t *testing.T) {
	h := newHarness(t)
	h.stub.on("PodiumAssignedIssues", func(map[string]any) stubReply {
		return stubReply{Message: "slow down", Status: http.StatusTooManyRequests}
	})
	h.src.tick(context.Background())
	assert.Equal(t, 30*time.Second, h.src.delay())
}

// TestAnUnreadableWatermarkSkipsTheTick: a database that cannot be read must not make the
// source poll from the beginning of time.
func TestAnUnreadableWatermarkSkipsTheTick(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.cursorErr = assertAnError
	h.mu.Unlock()
	h.stub.reply("PodiumAssignedIssues", page(false, ""))

	h.src.tick(context.Background())
	assert.Empty(t, h.stub.callsOf("PodiumAssignedIssues"), "no watermark, no poll")
}

var assertAnError = &GraphQLError{Op: "test", Message: "the database is down", Status: 500}

// ---------------------------------------------------------------------------
// FetchTranscript
// ---------------------------------------------------------------------------

// TestTheTranscriptIsTheTicketThenEveryComment, oldest first, with the bot's own answers as
// the assistant and its scaffolding left out.
func TestTheTranscriptIsTheTicketThenEveryComment(t *testing.T) {
	h := newHarness(t)
	now := h.clock.Now()
	h.stub.reply("PodiumIssue", map[string]any{
		"issue": map[string]any{
			"id":          "issue-a",
			"identifier":  "ENG-11",
			"title":       "Make it faster",
			"description": "it is slow",
			"url":         "https://linear.app/acme/issue/ENG-11",
			"createdAt":   iso(now.Add(-time.Hour)),
			"updatedAt":   iso(now),
			"state":       map[string]any{"id": "s1", "name": "Todo", "type": StateTypeUnstarted},
			"team":        map[string]any{"id": teamID, "key": "ENG"},
			"assignee":    map[string]any{"id": botUserID, "name": "podium", "displayName": "Podium", "isMe": true},
			"comments": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				// Newest first on the wire, as the connection returns them.
				"nodes": []map[string]any{
					humanComment("c-3", "still slow", now.Add(-5*time.Minute)),
					selfComment("c-2", "I made it faster", now.Add(-20*time.Minute)),
					selfComment("c-1", conductor.Placeholder, now.Add(-30*time.Minute)),
					humanComment("c-0", "please look at this", now.Add(-40*time.Minute)),
				},
			},
		},
	})

	entries, err := h.src.FetchTranscript(context.Background(), Ref("issue-a", teamID, "ENG-11"))
	require.NoError(t, err)
	require.Len(t, entries, 4, "the ticket, two human comments and one answer: %+v", entries)

	assert.Equal(t, conductor.RoleUser, entries[0].Role)
	assert.Equal(t, "ENG-11 — Make it faster\n\nit is slow", entries[0].Text)
	assert.Equal(t, "please look at this", entries[1].Text)
	assert.Equal(t, conductor.RoleAssistant, entries[2].Role)
	assert.Equal(t, "I made it faster", entries[2].Text)
	assert.Equal(t, "still slow", entries[3].Text)
	for _, e := range entries {
		assert.NotContains(t, e.Text, conductor.Placeholder, "scaffolding is not conversation")
	}
}

// TestABadRefIsRefusedRatherThanGuessedAt.
func TestABadRefIsRefusedRatherThanGuessedAt(t *testing.T) {
	h := newHarness(t)
	for _, ref := range []string{"", "issue-only", "/team/ENG-1", "a/b/c/d"} {
		_, err := h.src.FetchTranscript(context.Background(), ref)
		assert.Error(t, err, "ref %q", ref)
	}
	issueID, team, identifier, err := ParseRef(Ref("issue-a", teamID, "ENG-1"))
	require.NoError(t, err)
	assert.Equal(t, []string{"issue-a", teamID, "ENG-1"}, []string{issueID, team, identifier})
}

// ---------------------------------------------------------------------------
// Post, Edit, React
// ---------------------------------------------------------------------------

// TestAFinalIsOneCommentWithTheTextVerbatimAndAFooter. The text is content that came out of
// a task: it is posted and interpreted in no way at all.
func TestAFinalIsOneCommentWithTheTextVerbatimAndAFooter(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-1"}},
	})

	answer := "Done. See the PR.\n\nhttps://github.com/acme/repo/pull/7"
	id, err := h.src.Post(context.Background(), Ref("issue-a", teamID, "ENG-1"),
		conductor.Outbound{Type: conductor.OutFinal, Text: answer, TaskID: "task_01j"})
	require.NoError(t, err)
	assert.Equal(t, "comment-1", id)

	calls := h.stub.callsOf("PodiumAddComment")
	require.Len(t, calls, 1)
	assert.Equal(t, "issue-a", calls[0].Vars["issueId"])
	body := calls[0].Vars["body"].(string)
	assert.True(t, strings.HasPrefix(body, answer), "the answer is verbatim and first: %q", body)
	assert.Contains(t, body, "_Podium task task_01j_")
	// The id is supplied by this client, which is what makes a create idempotent on
	// Linear's side.
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
		calls[0].Vars["id"])
}

// TestAProgressPostCarriesNoFooter: the footer belongs on the answer, not on every "still
// working" line.
func TestAProgressPostCarriesNoFooter(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-1"}},
	})
	_, err := h.src.Post(context.Background(), Ref("issue-a", teamID, "ENG-1"),
		conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder, TaskID: "task_01j"})
	require.NoError(t, err)
	body := h.stub.callsOf("PodiumAddComment")[0].Vars["body"].(string)
	assert.Equal(t, conductor.Placeholder, body)
}

// TestProgressEditsAreThrottledToOneEveryTwoSeconds, with the newest text winning and the
// superseded one dropped. There is no timer: the outcome edit at the end of the turn
// rewrites the comment anyway.
func TestProgressEditsAreThrottledToOneEveryTwoSeconds(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-1"}},
	})
	h.stub.reply("PodiumEditComment", map[string]any{
		"commentUpdate": map[string]any{"success": true},
	})

	id, err := h.src.Post(context.Background(), ref,
		conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)

	// Straight after the post, inside the window: held, not sent.
	require.NoError(t, h.src.Edit(context.Background(), ref, id,
		conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ reading the code"}))
	assert.Empty(t, h.stub.callsOf("PodiumEditComment"), "an edit inside the window is superseded")

	h.clock.advance(EditThrottle + time.Millisecond)
	require.NoError(t, h.src.Edit(context.Background(), ref, id,
		conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ running the tests"}))
	edits := h.stub.callsOf("PodiumEditComment")
	require.Len(t, edits, 1)
	assert.Equal(t, "comment-1", edits[0].Vars["id"])
	assert.Equal(t, "⏳ running the tests", edits[0].Vars["body"], "the newest text wins")

	// And the outcome edit rewrites the same comment, which is the second and last write
	// of the turn's two comments.
	require.NoError(t, h.src.React(context.Background(), ref, conductor.ReactionDone))
	edits = h.stub.callsOf("PodiumEditComment")
	require.Len(t, edits, 2)
	assert.Equal(t, doneText, edits[1].Vars["body"])
	assert.Empty(t, h.stub.callsOf("PodiumMoveIssue"), "finishing a turn does not move the ticket")
}

// TestAFailedTurnMarksTheWorkingCommentAndLeavesTheStateAlone.
func TestAFailedTurnMarksTheWorkingCommentAndLeavesTheStateAlone(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-1"}},
	})
	h.stub.reply("PodiumEditComment", map[string]any{"commentUpdate": map[string]any{"success": true}})

	_, err := h.src.Post(context.Background(), ref,
		conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	require.NoError(t, h.src.React(context.Background(), ref, conductor.ReactionFailed))

	edits := h.stub.callsOf("PodiumEditComment")
	require.Len(t, edits, 1)
	assert.Equal(t, failedText, edits[0].Vars["body"])
	assert.Empty(t, h.stub.callsOf("PodiumMoveIssue"))
}

// TestAnOutcomeWithNoWorkingCommentSaysNothing: after a restart the comment ids are gone,
// and inventing a third comment to say "done" would be worse than saying nothing — the
// final comment is the answer either way.
func TestAnOutcomeWithNoWorkingCommentSaysNothing(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.src.React(context.Background(),
		Ref("issue-a", teamID, "ENG-1"), conductor.ReactionDone))
	assert.Empty(t, h.stub.seen())
}

// TestWorkingMovesTheIssueToInProgressByName, and resolves the state once per team.
func TestWorkingMovesTheIssueToInProgressByName(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumTeamStates", map[string]any{
		"team": map[string]any{
			"id": teamID, "key": "ENG",
			"states": map[string]any{"nodes": []map[string]any{
				{"id": "s-backlog", "name": "Backlog", "type": StateTypeBacklog, "position": 0.0},
				{"id": "s-doing", "name": "In Progress", "type": StateTypeStarted, "position": 2.0},
				{"id": "s-review", "name": "In Review", "type": StateTypeStarted, "position": 1.0},
			}},
		},
	})
	h.stub.reply("PodiumMoveIssue", map[string]any{"issueUpdate": map[string]any{"success": true}})

	require.NoError(t, h.src.React(context.Background(),
		Ref("issue-a", teamID, "ENG-1"), conductor.ReactionWorking))
	moves := h.stub.callsOf("PodiumMoveIssue")
	require.Len(t, moves, 1)
	assert.Equal(t, "issue-a", moves[0].Vars["id"])
	assert.Equal(t, "s-doing", moves[0].Vars["stateId"], "by name, not by position")

	// A second turn on the same team spends no query on it.
	require.NoError(t, h.src.React(context.Background(),
		Ref("issue-b", teamID, "ENG-2"), conductor.ReactionWorking))
	assert.Len(t, h.stub.callsOf("PodiumTeamStates"), 1, "the board is resolved once per process")
	assert.Len(t, h.stub.callsOf("PodiumMoveIssue"), 2)
}

// TestWorkingFallsBackToTheFirstStartedState when a team renamed the column.
func TestWorkingFallsBackToTheFirstStartedState(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumTeamStates", map[string]any{
		"team": map[string]any{
			"id": teamID, "key": "ENG",
			"states": map[string]any{"nodes": []map[string]any{
				{"id": "s-todo", "name": "Todo", "type": StateTypeUnstarted, "position": 0.0},
				{"id": "s-review", "name": "Reviewing", "type": StateTypeStarted, "position": 3.0},
				{"id": "s-hacking", "name": "Hacking", "type": StateTypeStarted, "position": 1.0},
			}},
		},
	})
	h.stub.reply("PodiumMoveIssue", map[string]any{"issueUpdate": map[string]any{"success": true}})

	require.NoError(t, h.src.React(context.Background(),
		Ref("issue-a", teamID, "ENG-1"), conductor.ReactionWorking))
	moves := h.stub.callsOf("PodiumMoveIssue")
	require.Len(t, moves, 1)
	assert.Equal(t, "s-hacking", moves[0].Vars["stateId"], "the lowest-positioned started state")
}

// TestABoardWithNoStartedStateIsLeftAlone rather than failing the turn.
func TestABoardWithNoStartedStateIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumTeamStates", map[string]any{
		"team": map[string]any{
			"id": teamID, "key": "ENG",
			"states": map[string]any{"nodes": []map[string]any{
				{"id": "s-todo", "name": "Todo", "type": StateTypeUnstarted, "position": 0.0},
			}},
		},
	})
	require.NoError(t, h.src.React(context.Background(),
		Ref("issue-a", teamID, "ENG-1"), conductor.ReactionWorking))
	assert.Empty(t, h.stub.callsOf("PodiumMoveIssue"))
}

// TestAnUnknownReactionIsAnError, so a new one added to the interface is not silently a
// no-op here.
func TestAnUnknownReactionIsAnError(t *testing.T) {
	h := newHarness(t)
	err := h.src.React(context.Background(), Ref("issue-a", teamID, "ENG-1"), conductor.Reaction("shrug"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shrug")
}

// ---------------------------------------------------------------------------
// Attach
// ---------------------------------------------------------------------------

// postFinal is the state Attach is always called in: a final comment exists.
func (h *harness) postFinal(t *testing.T, ref, text string) {
	t.Helper()
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-final"}},
	})
	h.stub.reply("PodiumEditComment", map[string]any{"commentUpdate": map[string]any{"success": true}})
	_, err := h.src.Post(context.Background(), ref,
		conductor.Outbound{Type: conductor.OutFinal, Text: text, TaskID: "task_01j"})
	require.NoError(t, err)
}

// TestAnImageIsUploadedAndEmbeddedInTheFinalComment: the bytes go to Linear's asset store
// and the final comment is edited to carry the markdown, so a turn with screenshots is
// still two comments.
func TestAnImageIsUploadedAndEmbeddedInTheFinalComment(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "here is the button")

	h.stub.reply("PodiumUploadFile", map[string]any{
		"fileUpload": map[string]any{
			"success": true,
			"uploadFile": map[string]any{
				"uploadUrl": h.stub.uploadURL(),
				"assetUrl":  "https://uploads.linear.app/assets/shot.png",
				"headers":   []map[string]any{{"key": "x-linear-signature", "value": "abc"}},
			},
		},
	})

	png := []byte("\x89PNG\r\n\x1a\nnot really a png")
	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name:        "shot.png",
		ContentType: "image/png",
		Size:        int64(len(png)),
		Body:        strings.NewReader(string(png)),
		TaskID:      "task_01j",
	}))

	prep := h.stub.callsOf("PodiumUploadFile")
	require.Len(t, prep, 1)
	assert.Equal(t, "shot.png", prep[0].Vars["filename"])
	assert.Equal(t, "image/png", prep[0].Vars["contentType"])
	assert.EqualValues(t, len(png), prep[0].Vars["size"])

	uploads := h.stub.seenUploads()
	require.Len(t, uploads, 1)
	assert.Equal(t, png, uploads[0].Body)
	assert.Equal(t, "abc", uploads[0].Headers.Get("x-linear-signature"),
		"the headers fileUpload returned must be sent verbatim")

	edits := h.stub.callsOf("PodiumEditComment")
	require.Len(t, edits, 1)
	assert.Equal(t, "comment-final", edits[0].Vars["id"])
	body := edits[0].Vars["body"].(string)
	assert.Contains(t, body, "here is the button", "the answer survives the append")
	assert.Contains(t, body, "![shot.png](https://uploads.linear.app/assets/shot.png)")
}

// TestANonImageIsLinkedRatherThanEmbedded.
func TestANonImageIsLinkedRatherThanEmbedded(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "the report")
	h.stub.reply("PodiumUploadFile", map[string]any{
		"fileUpload": map[string]any{
			"success": true,
			"uploadFile": map[string]any{
				"uploadUrl": h.stub.uploadURL(),
				"assetUrl":  "https://uploads.linear.app/assets/report.csv",
				"headers":   []map[string]any{},
			},
		},
	})
	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name: "report.csv", ContentType: "text/csv", Size: 3,
		Body: strings.NewReader("a,b"), TaskID: "task_01j",
	}))
	body := h.stub.callsOf("PodiumEditComment")[0].Vars["body"].(string)
	assert.Contains(t, body, "[report.csv](https://uploads.linear.app/assets/report.csv)")
	assert.NotContains(t, body, "![report.csv]")
}

// TestAnUploadLinearRefusesFallsBackToATaskLink. The turn is not failed over an attachment:
// the answer is already posted and the artifact is on the task.
func TestAnUploadLinearRefusesFallsBackToATaskLink(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "here is the button")
	h.stub.on("PodiumUploadFile", func(map[string]any) stubReply {
		return stubReply{Message: "Entity not found", Code: "BAD_USER_INPUT"}
	})

	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name: "shot.png", ContentType: "image/png", Size: 4,
		Body: strings.NewReader("data"), TaskID: "task_01j",
	}))
	body := h.stub.callsOf("PodiumEditComment")[0].Vars["body"].(string)
	assert.Contains(t, body, "https://podium.example/tasks/task_01j")
	assert.Contains(t, body, "signed in to Podium")
}

// TestAFailedPutFallsBackToATaskLink too: the mutation worked and the bytes did not.
func TestAFailedPutFallsBackToATaskLink(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "here is the button")
	h.stub.reply("PodiumUploadFile", map[string]any{
		"fileUpload": map[string]any{
			"success": true,
			"uploadFile": map[string]any{
				"uploadUrl": h.stub.uploadURL(),
				"assetUrl":  "https://uploads.linear.app/assets/shot.png",
				"headers":   []map[string]any{},
			},
		},
	})
	h.stub.mu.Lock()
	h.stub.uploadStatus = http.StatusForbidden
	h.stub.mu.Unlock()

	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name: "shot.png", ContentType: "image/png", Size: 4,
		Body: strings.NewReader("data"), TaskID: "task_01j",
	}))
	body := h.stub.callsOf("PodiumEditComment")[0].Vars["body"].(string)
	assert.Contains(t, body, "https://podium.example/tasks/task_01j")
	assert.NotContains(t, body, "uploads.linear.app")
}

// TestAFileOverTheLimitIsNeverUploaded: the size is known before the transfer, so a 60 MB
// artifact costs a link and no bytes.
func TestAFileOverTheLimitIsNeverUploaded(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "a big file")

	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name: "core.dump", Size: MaxUploadBytes + 1,
		Body: strings.NewReader("x"), TaskID: "task_01j",
	}))
	assert.Empty(t, h.stub.callsOf("PodiumUploadFile"), "nothing over the limit is offered to Linear")
	assert.Empty(t, h.stub.seenUploads())
	body := h.stub.callsOf("PodiumEditComment")[0].Vars["body"].(string)
	assert.Contains(t, body, "https://podium.example/tasks/task_01j")
}

// TestAnAttachmentWithNoLengthIsRefused: fileUpload declares the length up front, so a
// reader nobody measured cannot be sent. The caller always knows it.
func TestAnAttachmentWithNoLengthIsRefused(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "a file")
	require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
		Name: "shot.png", ContentType: "image/png", Size: 0,
		Body: strings.NewReader("data"), TaskID: "task_01j",
	}))
	assert.Empty(t, h.stub.callsOf("PodiumUploadFile"))
	body := h.stub.callsOf("PodiumEditComment")[0].Vars["body"].(string)
	assert.Contains(t, body, "task_01j")
}

// TestSeveralAttachmentsShareOneComment, which is what keeps a turn to two comments.
func TestSeveralAttachmentsShareOneComment(t *testing.T) {
	h := newHarness(t)
	ref := Ref("issue-a", teamID, "ENG-1")
	h.postFinal(t, ref, "before and after")
	h.stub.on("PodiumUploadFile", func(vars map[string]any) stubReply {
		return stubReply{Data: map[string]any{"fileUpload": map[string]any{
			"success": true,
			"uploadFile": map[string]any{
				"uploadUrl": h.stub.uploadURL(),
				"assetUrl":  "https://uploads.linear.app/assets/" + vars["filename"].(string),
				"headers":   []map[string]any{},
			},
		}}}
	})

	for _, name := range []string{"before.png", "after.png"} {
		require.NoError(t, h.src.Attach(context.Background(), ref, conductor.Attachment{
			Name: name, ContentType: "image/png", Size: 4,
			Body: strings.NewReader("data"), TaskID: "task_01j",
		}))
	}
	edits := h.stub.callsOf("PodiumEditComment")
	require.Len(t, edits, 2, "two edits of one comment, not two comments")
	final := edits[1].Vars["body"].(string)
	assert.Contains(t, final, "before and after")
	assert.Contains(t, final, "![before.png]")
	assert.Contains(t, final, "![after.png]")
	assert.Len(t, h.stub.callsOf("PodiumAddComment"), 1)
}

// ---------------------------------------------------------------------------
// construction
// ---------------------------------------------------------------------------

// TestASourceWithNoLinearPlaybookIsRefused is the other half of the zero-playbook rule: the
// profile loader allows a bot with no Linear playbook, and this is where a Linear key with
// nothing to run it in becomes an error naming both.
func TestASourceWithNoLinearPlaybookIsRefused(t *testing.T) {
	_, err := New(Options{
		APIKey:       fakeAPIKey,
		Endpoint:     DefaultLinearEndpointForTest,
		PollInterval: time.Minute,
		Session:      func(context.Context, string) (time.Time, bool) { return time.Time{}, false },
		Cursor: CursorStore{
			Get: func(context.Context) (time.Time, error) { return time.Time{}, nil },
			Put: func(context.Context, time.Time) error { return nil },
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear: true")
	assert.Contains(t, err.Error(), "PODIUM_AGENT_LINEAR_API_KEY")
}

// DefaultLinearEndpointForTest keeps the endpoint literal out of the assertions above.
const DefaultLinearEndpointForTest = "https://api.linear.app/graphql"

// TestAnEndpointThatIsNotAURLIsRefused.
func TestAnEndpointThatIsNotAURLIsRefused(t *testing.T) {
	for _, endpoint := range []string{"", "api.linear.app/graphql", "ftp://api.linear.app"} {
		_, err := NewClient(ClientOptions{Endpoint: endpoint, APIKey: fakeAPIKey})
		assert.Error(t, err, "endpoint %q", endpoint)
	}
	_, err := NewClient(ClientOptions{Endpoint: DefaultLinearEndpointForTest})
	assert.Error(t, err, "a client with no API key is a client that will 401 on every call")
}

// TestTheSourceIsAConductorSource, which is the whole contract steps 17 and 21 share.
func TestTheSourceIsAConductorSource(t *testing.T) {
	h := newHarness(t)
	var src conductor.Source = h.src
	assert.Equal(t, "linear", src.Kind())
	assert.Equal(t, conductor.SourceLinear, src.Kind(), "the brief's source.kind and Kind() agree")
	assert.NotNil(t, src.Events())
}
