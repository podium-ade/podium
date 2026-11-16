package linear

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// What somebody says in a ticket's conversation in Podium is posted to the issue as a comment
// from the app, under their name and picture — whatever the ref names, never into a session.
func TestAPersonsMessageFromPodiumIsACommentUnderTheirName(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumAddCommentAs", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-9"}},
	})
	ref, ok := h.src.PersonRef(SourceKey(fixtureIssue))
	require.True(t, ok)
	id, err := h.src.Post(context.Background(), ref, conductor.Outbound{
		Type: conductor.OutRelay, Text: "  Can you also check the login page?  ",
		Author: "Ada Lovelace", AuthorAvatar: "https://example.com/ada.png",
	})
	require.NoError(t, err)
	assert.Equal(t, "comment-9", id)

	calls := h.stub.callsOf("PodiumAddCommentAs")
	require.Len(t, calls, 1)
	input := calls[0].Vars["input"].(map[string]any)
	assert.Equal(t, fixtureIssue, input["issueId"])
	assert.Equal(t, "Can you also check the login page?", input["body"])
	assert.Equal(t, "Ada Lovelace", input["createAsUser"])
	assert.Equal(t, "https://example.com/ada.png", input["displayIconUrl"])
	assert.NotEmpty(t, input["id"], "a client id, so a retry cannot post it twice")
	assert.Empty(t, h.activities(), "nothing goes into an agent session")
	assert.Empty(t, h.stub.callsOf("PodiumAddComment"))

	// Into a session's ref too: a person's words are a comment, not an activity.
	_, err = h.src.Post(context.Background(), sessionRef(), conductor.Outbound{Type: conductor.OutRelay, Text: "And this.", Author: "Ada"})
	require.NoError(t, err)
	input = h.stub.callsOf("PodiumAddCommentAs")[1].Vars["input"].(map[string]any)
	assert.NotContains(t, input, "displayIconUrl", "no picture is sent when there is none")
	assert.Empty(t, h.activities())
}

// An answer to it is a comment from the app, with the task it came from, as a turn the poller
// started is answered: a web turn has no agent session.
func TestAnAnswerToAPodiumMessageIsTheAppsComment(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumAddComment", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "comment-10"}},
	})
	ref, ok := h.src.PersonRef(SourceKey(fixtureIssue))
	require.True(t, ok)
	_, err := h.src.Post(context.Background(), ref, conductor.Outbound{Type: conductor.OutFinal, Text: "Checked it.", TaskID: "task-7"})
	require.NoError(t, err)
	calls := h.stub.callsOf("PodiumAddComment")
	require.Len(t, calls, 1)
	assert.Equal(t, "Checked it.\n\n_Podium task task-7_", calls[0].Vars["body"])
	assert.Empty(t, h.activities())

	_, ok = h.src.PersonRef(SourceKey("project:p-1"))
	assert.False(t, ok, "a project has no comments to post into")
	_, ok = h.src.PersonRef(SessionKey("s-1"))
	assert.False(t, ok, "an old session-keyed conversation is not an issue's")
}

// The comment the app posted for somebody must not come back as a prompt: not when Linear
// names the app as its speaker, and not when it names the comment.
func TestTheAppsOwnCommentIsNotAPrompt(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumAddCommentAs", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "relayed-1"}},
	})
	_, err := h.src.Post(context.Background(), Ref(fixtureIssue, fixtureTeam, "EME-5"), conductor.Outbound{
		Type: conductor.OutRelay, Text: "@podium what is the time?", Author: "Ada Lovelace",
	})
	require.NoError(t, err)

	// A mention session Linear opened from that comment.
	fromComment := h.fixture(t, "session_created_mention", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["sourceCommentId"] = "relayed-1"
	})
	h.deliver(fromComment, "delivery-1")
	h.noEvent(t)

	// A prompt Linear says the app itself wrote.
	byApp := h.fixture(t, "session_prompted_reply", func(d map[string]any) {
		d["agentActivity"].(map[string]any)["user"] = map[string]any{"id": fixtureAppUser, "name": "Podium"}
	})
	h.deliver(byApp, "delivery-2")
	h.noEvent(t)
	assert.Equal(t, 2.0, h.deliveries(t, TypeAgentSession, DeliveryIgnored))

	// Anybody else's prompt is still one.
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-3")
	assert.Equal(t, "What is the time?", h.event(t).Text)
}

// Nor is it an event for the routines on Linear events, whoever Linear says made it.
func TestTheAppsOwnCommentIsNotHandedToRoutines(t *testing.T) {
	sink := &fakeSink{}
	h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
	h.stub.reply("PodiumAddCommentAs", map[string]any{
		"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "relayed-2"}},
	})
	_, err := h.src.Post(context.Background(), Ref(fixtureIssue, fixtureTeam, "EME-5"), conductor.Outbound{
		Type: conductor.OutRelay, Text: "hello", Author: "Ada Lovelace",
	})
	require.NoError(t, err)

	comment := func(id, author string) []byte {
		return h.issueCreated(t, func(d map[string]any) {
			d["type"] = "Comment"
			d["data"] = map[string]any{"id": id, "body": "hello", "issueId": fixtureIssue, "userId": author}
		})
	}
	h.deliver(comment("relayed-2", fixtureUser), "delivery-1")
	h.deliver(comment("other-1", fixtureAppUser), "delivery-2")
	h.settle(t)
	assert.Empty(t, sink.got())

	h.deliver(comment("other-2", fixtureUser), "delivery-3")
	h.settle(t)
	assert.Len(t, sink.got(), 1, "a person's own comment still is one")
}

// In a transcript, a comment the app posted as somebody is theirs, not the app's answer.
func TestACommentPostedAsSomebodyIsTheirsInTheTranscript(t *testing.T) {
	h := newAppHarness(t)
	at := time.Date(2026, 11, 12, 20, 46, 0, 0, time.UTC)
	relayed := map[string]any{
		"id": "c-3", "body": "Please also check Safari.", "createdAt": iso(at.Add(time.Minute)),
		"parentId": nil, "quotedText": nil,
		"botActor": map[string]any{"id": "b-1", "name": "Podium", "userDisplayName": "Ada Lovelace"},
		"user":     map[string]any{"id": fixtureAppUser, "name": "Podium", "displayName": "podium", "isMe": true},
	}
	node := issueNode(fixtureIssue, "EME-5", "Test issue", StateTypeBacklog, at, relayed)
	node["comments"].(map[string]any)["pageInfo"] = map[string]any{"hasNextPage": false, "endCursor": ""}
	h.stub.reply("PodiumIssue", map[string]any{"issue": node})

	entries, err := h.src.FetchTranscript(context.Background(), sessionRef())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, conductor.RoleUser, entries[1].Role)
	assert.Equal(t, "Ada Lovelace", entries[1].Author)
}
