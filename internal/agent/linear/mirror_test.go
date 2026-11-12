package linear

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// ---------------------------------------------------------------------------
// a session is a chat in Podium
// ---------------------------------------------------------------------------

// An agent session is mirrored under its own key, whatever else its ref says; a ref the
// poller wrote names no session and is not mirrored at all.
func TestASessionIsMirroredAndAPolledTicketIsNot(t *testing.T) {
	h := newAppHarness(t)
	key, ok := h.src.MirrorKey(sessionRef())
	require.True(t, ok)
	assert.Equal(t, SessionKey(delegationSession), key)

	key, ok = h.src.MirrorKey(DelegatedSessionRef(fixtureIssue, fixtureTeam, "EME-5", delegationSession))
	require.True(t, ok)
	assert.Equal(t, SessionKey(delegationSession), key, "a delegation's session is the same conversation")

	_, ok = h.src.MirrorKey(Ref(fixtureIssue, fixtureTeam, "EME-5"))
	assert.False(t, ok, "the poller has nobody to own or see a chat")
	_, ok = h.src.MirrorKey("not a ref")
	assert.False(t, ok)
}

// The session's chat says whether the issue's team is private, as Linear says it, so sharing
// the chat warns with Linear's own word. Linear is asked once per team for a while, and a team
// it cannot answer for is left unknown, never guessed.
func TestTheChatKnowsWhetherTheIssuesTeamIsPrivate(t *testing.T) {
	h := newAppHarness(t)
	h.mu.Lock()
	h.private[fixtureTeam] = true
	h.mu.Unlock()
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	assert.Equal(t, conductor.ChannelPrivate, h.event(t).ChannelPrivacy)
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2")
	assert.Equal(t, conductor.ChannelPrivate, h.event(t).ChannelPrivacy)
	assert.Len(t, h.stub.callsOf("PodiumTeamPrivacy"), 1, "asked once within its TTL")

	h.clock.advance(accountTTL)
	h.mu.Lock()
	delete(h.private, fixtureTeam)
	h.mu.Unlock()
	h.deliver(h.fixture(t, "session_prompted_reply", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["id"] = "prompt-later"
	}), "delivery-3")
	assert.Empty(t, h.event(t).ChannelPrivacy, "a team Linear did not answer for is not known")
	assert.Len(t, h.stub.callsOf("PodiumTeamPrivacy"), 2)
}

// A retried session opening is not acknowledged again here: a second "Starting on this." after
// the session answered would show it working. A retried follow-up is handed to the conductor
// again, with the same prompt id, and the conductor's record is what stops it starting a second
// turn — on this conductor or on whichever one the retry reached.
func TestARetriedFollowUpIsLeftToTheConductorsRecord(t *testing.T) {
	h := newAppHarness(t)
	require.Equal(t, http.StatusOK, h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-1").Code)
	first := h.event(t)
	h.clock.advance(time.Minute)
	require.Equal(t, http.StatusOK, h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2").Code)
	again := h.event(t)
	assert.Equal(t, first.ExternalID, again.ExternalID, "the conductor dedupes on the prompt's own id")
	assert.Empty(t, h.activities(), "a follow-up is never acknowledged")

	// The same delivery id is still a cheap repeat on this conductor.
	require.Equal(t, http.StatusOK, h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2").Code)
	h.noEvent(t)
}

// ---------------------------------------------------------------------------
// a stop that reached another conductor, or happened before a restart
// ---------------------------------------------------------------------------

// The turn's "This was cancelled", for a task a Stop in Linear cancelled, is never posted: the
// session was closed with "Stopped." already. The control plane's cancel reason says so on
// any conductor and after a restart, when no source remembers the Stop. A task cancelled for
// any other reason is still said to have been.
func TestAStopsCancellationIsNotPostedWhereverItArrives(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)

	id, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{
		Type: conductor.OutFailure, Text: "This was cancelled. Task `task-1`.", TaskID: "task-1",
		CancelReason: StopReason,
	})
	require.NoError(t, err, "a refused post is not a failed one, or the outbox would retry it")
	assert.Empty(t, id)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "late", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, h.activities(), 1, "only the placeholder: the stopped task says nothing more")

	h.startTurn(t)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{
		Type: conductor.OutFailure, Text: "This was cancelled. Task `task-2`.", TaskID: "task-2",
		CancelReason: "cancelled by alice@acme.com",
	})
	require.NoError(t, err)
	acts := h.activities()
	require.Len(t, acts, 3)
	assert.Equal(t, "error", acts[2]["content"].(map[string]any)["type"])
}

// Another conductor heard the Stop and is closing the session; the turn runs here. From the
// moment it says so, nothing the dying task says is posted here, and nothing is kept alive —
// but the prompts this conductor is holding are not dropped, since they may be newer than the
// Stop, and the next turn is answered.
func TestAStopHeardElsewhereSilencesTheTurnRunningHere(t *testing.T) {
	h := newAppHarness(t)
	h.replyLinks()
	ctx := context.Background()
	id := h.startTurn(t)
	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ building", TaskID: "task-1"}))
	before := len(h.activities())

	h.src.StoppedElsewhere("linear:issue-somebody-else", "task-1")
	h.src.StoppedElsewhere(SessionKey(delegationSession), "task-1")

	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.Edit(ctx, sessionRef(), id, conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ still", TaskID: "task-1"}))
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), conductor.Outbound{
		Type: conductor.OutActivity, TaskID: "task-1",
		Text: `{"kind":"tool","tool":"read","status":"completed","input":"{\"filePath\":\"late.go\"}"}`,
	}))
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFailure, Text: "This was cancelled.", TaskID: "task-1"})
	require.NoError(t, err)
	h.clock.advance(KeepAliveAfter + time.Minute)
	assert.Len(t, h.activities(), before, "nothing more is said for the stopped turn, and it is not kept alive")
	assert.Empty(t, h.stopped(), "the conductor that heard the Stop asks for it; this one does not")

	h.startTurn(t)
	_, err = h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Answer.", TaskID: "task-2"})
	require.NoError(t, err)
	acts := h.activities()
	require.Len(t, acts, before+2)
	assert.Equal(t, "response", acts[before+1]["content"].(map[string]any)["type"])
}
