package linear

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// Who asked, as the agent app hands it to the conductor: a candidate email for the person who
// wrote the prompt, which the conductor keeps only for a login that has signed in to Podium.

const (
	bobUser  = "7a1c0c39-0d7e-4c0e-9d77-2b8f3f1b0b0b"
	bobEmail = "bob@acme.com"
)

// replyFrom is the captured follow-up, written by somebody else than the session's creator.
func replyFrom(id, name, email string) func(map[string]any) {
	return func(d map[string]any) {
		d["agentActivity"].(map[string]any)["id"] = "prompt-from-" + id
		d["agentActivity"].(map[string]any)["userId"] = id
		d["agentActivity"].(map[string]any)["user"] = map[string]any{
			"id": id, "name": name, "email": email, "url": "https://linear.app/acme/profiles/" + id,
		}
	}
}

// A follow-up can come from somebody other than the person who opened the session, and the
// turn is theirs: the speaker is the prompt's author, never the session's creator.
func TestTheSpeakerIsWhoeverWroteThePromptNotTheSessionsCreator(t *testing.T) {
	h := newAppHarness(t)
	h.person(member(bobUser, bobEmail))
	h.deliver(h.fixture(t, "session_prompted_reply", replyFrom(bobUser, "Bob", "bob@acme.com")), "delivery-1")
	ev := h.event(t)

	assert.Equal(t, bobEmail, ev.Login)
	assert.Equal(t, conductor.SpeakerLinear, ev.SpeakerProvider)
	assert.Equal(t, fixtureOrg+"/"+bobUser, ev.SpeakerID, "the workspace is part of the account's key")
	assert.Equal(t, "Bob", ev.Author)
	calls := h.stub.callsOf("PodiumUserAccount")
	require.Len(t, calls, 1)
	assert.Equal(t, bobUser, calls[0].Vars["id"])
}

// Linear's word on the account, not the payload's, is what the candidate is made of, and it
// is lowercased as Slack's is.
func TestTheCandidateIsTheEmailLinearHasForTheAccountLowercased(t *testing.T) {
	h := newAppHarness(t)
	h.person(member(fixtureUser, " Ada.Lovelace@ACME.com "))
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	assert.Equal(t, "ada.lovelace@acme.com", h.event(t).Login)
}

// Every account check Slack makes before it offers a candidate has its Linear equivalent. A
// speaker who fails one is still named — the conductor refuses them by it — and has no
// candidate.
func TestASpeakerWhoFailsAnAccountCheckHasNoCandidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		user map[string]any
	}{
		{"a guest", map[string]any{"id": fixtureUser, "email": fixtureEmail, "active": true, "guest": true, "app": false}},
		{"a suspended member", map[string]any{"id": fixtureUser, "email": fixtureEmail, "active": false, "guest": false, "app": false}},
		{"an app user", map[string]any{"id": fixtureUser, "email": fixtureEmail, "active": true, "guest": false, "app": true}},
		{"no email", map[string]any{"id": fixtureUser, "email": "", "active": true, "guest": false, "app": false}},
		{"an email that is not one", map[string]any{"id": fixtureUser, "email": "ada@@acme.com", "active": true, "guest": false, "app": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAppHarness(t)
			h.person(tc.user)
			h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
			ev := h.event(t)
			assert.Empty(t, ev.Login)
			assert.Equal(t, conductor.SpeakerLinear, ev.SpeakerProvider)
			assert.Equal(t, fixtureOrg+"/"+fixtureUser, ev.SpeakerID)
		})
	}
}

// A speaker Linear cannot tell the source about — not found, or the API failing — has no
// candidate this time, and is asked about again next time rather than remembered.
func TestASpeakerWhoseAccountCannotBeReadHasNoCandidateAndIsAskedAgain(t *testing.T) {
	h := newAppHarness(t)
	h.mu.Lock()
	delete(h.users, fixtureUser)
	h.mu.Unlock()
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	ev := h.event(t)
	assert.Empty(t, ev.Login)
	assert.Equal(t, conductor.SpeakerLinear, ev.SpeakerProvider, "named, so the conductor refuses it")
	assert.Contains(t, h.log.String(), "checking a speaker's account failed")
	assert.NotContains(t, h.log.String(), fixtureEmail, "an email is not logged")

	h.person(member(fixtureUser, fixtureEmail))
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2")
	assert.Equal(t, fixtureEmail, h.event(t).Login)
	assert.Len(t, h.stub.callsOf("PodiumUserAccount"), 2)
}

// A prompt with no user, or one the app wrote itself, names the provider and no account: the
// conductor refuses it rather than mistaking it for the poller's.
func TestAPromptWithNoPersonStillNamesTheProvider(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"no user":        func(d map[string]any) { d["agentActivity"].(map[string]any)["user"] = nil },
		"the app itself": replyFrom(fixtureAppUser, "Podium", "podium@oauthapp.linear.app"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newAppHarness(t)
			h.deliver(h.fixture(t, "session_prompted_reply", edit), "delivery-1")
			ev := h.event(t)
			assert.Empty(t, ev.Login)
			assert.Equal(t, conductor.SpeakerLinear, ev.SpeakerProvider)
			assert.Empty(t, h.stub.callsOf("PodiumUserAccount"), "nobody to ask about")
		})
	}
}

// What Linear said about an account is trusted for accountTTL, as Slack's users.info is, and
// asked again after it: somebody made a guest or suspended stops matching within minutes.
func TestAnAccountIsAskedAboutOnceWithinItsTTL(t *testing.T) {
	h := newAppHarness(t)
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	require.Equal(t, fixtureEmail, h.event(t).Login)
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2")
	require.Equal(t, fixtureEmail, h.event(t).Login)
	assert.Len(t, h.stub.callsOf("PodiumUserAccount"), 1)

	h.person(map[string]any{"id": fixtureUser, "email": fixtureEmail, "active": false, "guest": false, "app": false})
	h.clock.advance(accountTTL)
	h.deliver(h.fixture(t, "session_prompted_reply", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["id"] = "prompt-later"
		d["webhookTimestamp"] = h.clock.Now().UnixMilli()
	}), "delivery-3")
	assert.Empty(t, h.event(t).Login, "suspended since")
	assert.Len(t, h.stub.callsOf("PodiumUserAccount"), 2)
}

// ---------------------------------------------------------------------------
// a refusal
// ---------------------------------------------------------------------------

// Somebody who is not in Podium is told so in the session, in words, with an error that ends
// it. Nothing ran, so the delegated issue does not move and nothing is kept alive.
func TestARefusedDelegationIsToldInTheSessionAndMovesNothing(t *testing.T) {
	h := newAppHarness(t)
	h.issueIn(StateTypeBacklog)
	ctx := context.Background()
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	ev := h.event(t)

	require.NoError(t, h.src.React(ctx, ev.Ref, conductor.ReactionRefused))
	acts := h.activities()
	require.Len(t, acts, 2, "the acknowledgement, then the refusal")
	assert.Equal(t, map[string]any{"type": "error", "body": refusedText}, acts[1]["content"])
	assert.NotEqual(t, true, acts[1]["ephemeral"], "a refusal stays")
	assert.Contains(t, refusedText, "signed in to Podium")
	assert.Contains(t, refusedText, "same email")

	assert.Empty(t, h.stub.callsOf("PodiumIssueState"), "the issue is not even looked at")
	assert.Empty(t, h.stub.callsOf("PodiumMoveIssue"))
	assert.Zero(t, h.clock.pending(), "nothing is kept alive")
	h.clock.advance(time.Hour)
	assert.Len(t, h.activities(), 2)
}

// A refusal said while somebody else's turn is open in the session must not end it: it is a
// thought that stays, and that turn goes on being kept alive and answering.
func TestARefusalDuringSomebodyElsesTurnDoesNotEndIt(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionWorking))

	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionRefused))
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "thought", "body": refusedText}, acts[1]["content"])
	assert.NotEqual(t, true, acts[1]["ephemeral"])

	h.clock.advance(ActivityThrottle)
	require.NoError(t, h.src.PostActivity(ctx, sessionRef(), toolCall("bash", "make test")))
	assert.Len(t, h.activities(), 3, "the turn still speaks")
	h.clock.advance(KeepAliveAfter)
	assert.Equal(t, keepAliveText, h.activities()[3]["content"].(map[string]any)["body"], "and is still kept alive")
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done."})
	require.NoError(t, err)
	assert.Equal(t, "response", h.activities()[4]["content"].(map[string]any)["type"])
}

// A turn waiting on its question is open too.
func TestARefusalWhileATurnWaitsOnAQuestionIsAThought(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutQuestion, Text: "Which branch?"})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionRefused))
	assert.Equal(t, "thought", h.activities()[2]["content"].(map[string]any)["type"])
}

// Once the last turn has answered, a refusal is an error again.
func TestARefusalAfterTheLastTurnAnsweredEndsTheSession(t *testing.T) {
	h := newAppHarness(t)
	ctx := context.Background()
	h.startTurn(t)
	_, err := h.src.Post(ctx, sessionRef(), conductor.Outbound{Type: conductor.OutFinal, Text: "Done."})
	require.NoError(t, err)
	require.NoError(t, h.src.React(ctx, sessionRef(), conductor.ReactionRefused))
	assert.Equal(t, map[string]any{"type": "error", "body": refusedText}, h.activities()[2]["content"])
}

// A session somebody stopped still hears the refusal of a later prompt: it is the source's own
// word about that prompt, not anything the stopped turn said.
func TestARefusalAfterAStopIsStillSaid(t *testing.T) {
	h := newAppHarness(t)
	h.startTurn(t)
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	require.Len(t, h.activities(), 2)
	require.NoError(t, h.src.React(context.Background(), sessionRef(), conductor.ReactionRefused))
	acts := h.activities()
	require.Len(t, acts, 3)
	assert.Equal(t, refusedText, acts[2]["content"].(map[string]any)["body"])
}

// ---------------------------------------------------------------------------
// what is not gated
// ---------------------------------------------------------------------------

// Stopping needs no Podium login: whoever pressed Stop is not looked up at all, and the turn
// is stopped.
func TestAStopFromSomebodyOutsidePodiumStillStops(t *testing.T) {
	h := newAppHarness(t)
	h.mu.Lock()
	h.users = map[string]map[string]any{}
	h.mu.Unlock()
	h.startTurn(t)
	h.deliver(h.fixture(t, "session_prompted_stop"), "delivery-1")
	h.settle(t)
	assert.Equal(t, []string{SessionKey(delegationSession)}, h.stopped())
	assert.Empty(t, h.stub.callsOf("PodiumUserAccount"))
	acts := h.activities()
	require.Len(t, acts, 2)
	assert.Equal(t, map[string]any{"type": "response", "body": stoppedText}, acts[1]["content"])
}

// The poller proves nobody, and says so by naming no account: its turns run as nobody, which
// the conductor admits as it always has.
func TestAPolledEventNamesNoSpeaker(t *testing.T) {
	h := newHarness(t)
	h.stub.reply("PodiumAssignedIssues", page(false, "",
		issueNode("issue-new", "ENG-1", "Add a button", StateTypeUnstarted, h.clock.Now().Add(-time.Minute)),
	))
	h.src.tick(context.Background())
	events := h.drain()
	require.Len(t, events, 1)
	assert.Empty(t, events[0].Login)
	assert.Empty(t, events[0].SpeakerProvider)
	assert.Empty(t, events[0].SpeakerID)
	assert.Empty(t, h.stub.callsOf("PodiumUserAccount"))
}
