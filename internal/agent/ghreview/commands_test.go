package ghreview

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

func TestACommandIsAWordAtTheStartOfALine(t *testing.T) {
	mention := compileMention("podium-ade")
	for _, tc := range []struct {
		body string
		want []string
	}{
		{"/review", []string{"review"}},
		{"/REVIEW   focus on the migration", []string{"review"}},
		{"@podium-ade review", []string{"review"}},
		{"@Podium-Ade[bot] Review the error handling", []string{"review"}},
		{"thanks!\n  @podium-ade review auth please\nmore text", []string{"review"}},
		{"/review\n/deploy staging\n/review again", []string{"review", "deploy"}},
		{"@podium-ade please fix the tests", []string{"please"}},
		// Not at the start of a line.
		{"could you /review this", nil},
		{"please @podium-ade review", nil},
		// Somebody else's app, or no word.
		{"@podium-ade-other review", nil},
		{"/ review", nil},
		{"@podium-ade", nil},
		// Code is not a command.
		{"```\n/usr/bin/env bash\n```\n", nil},
		{"~~~sh\n/review\n~~~\n/deploy", []string{"deploy"}},
		{"", nil},
	} {
		assert.Equal(t, tc.want, Commands(tc.body, mention), "%q", tc.body)
	}
	// Without a slug only the slash form exists.
	assert.Nil(t, Commands("@podium-ade review", nil))
	assert.Equal(t, []string{"review"}, Commands("/review", nil))
}

// commandComment is a comment on a pull request's conversation tab, from mallory, an account
// with a numeric id; the pull request is mona's.
func commandComment(body string) map[string]any {
	return map[string]any{
		"action": "created",
		"comment": map[string]any{
			"id": 7, "body": body, "created_at": "2026-11-24T11:59:00Z",
			"html_url": "https://github.com/acme/api/pull/12#issuecomment-7",
			"user":     map[string]any{"login": "mallory", "type": "User", "id": 99},
		},
		"issue": map[string]any{
			"number": 12, "title": "Fix login", "draft": true,
			"user":         map[string]any{"login": "mona", "type": "User", "id": 5},
			"pull_request": map[string]any{"url": "https://api.github.com/repos/acme/api/pulls/12"},
		},
		"sender": map[string]any{"login": "mallory", "type": "User", "id": 99},
	}
}

// A command is kept as who asked, for what, on which pull request; what they wrote is kept
// as data, the way a pull request's own body is.
func TestACommandIsKeptForRoutinesAsWhoAskedAndWhatFor(t *testing.T) {
	d, ok := RoutineDelivery("issue_comment", "del-c1", ghBody(t, commandComment("/review the retry loop, please")),
		appIs("podium"), compileMention("podium"), routineNow)
	require.True(t, ok)
	assert.Equal(t, []string{store.GitHubCommandEvent}, d.Events)
	assert.Equal(t, store.GitHubPullSubject("acme/api", 12), d.SubjectKey, "it runs in the pull request's conversation")
	assert.Equal(t, store.GitHubEventFacts{
		Actor: "mallory", ActorID: "99", Pull: true, Draft: true, Command: true, Commands: []string{"review"},
	}, d.Facts, "the base branch is not in a conversation comment: the source reads it")

	top, data := keptData(t, d)
	assert.Equal(t, "https://github.com/acme/api/pull/12#issuecomment-7", top["url"])
	assert.Equal(t, map[string]any{"name": "mallory"}, top["actor"])
	assert.Equal(t, []any{"review"}, data["commands"])
	assert.Equal(t, "/review the retry loop, please", data["comment"])
	assert.Equal(t, "mallory", data["asked_by"])
	assert.Equal(t, "mona", data["author"], "the pull request's author, whom the run makes an editor")
	assert.EqualValues(t, 5, data["author_id"])
}

func TestOnlyANewCommandFromSomebodyElseIsKept(t *testing.T) {
	edited := commandComment("/review")
	edited["action"] = "edited"
	own := commandComment("/review")
	own["sender"] = map[string]any{"login": "podium[bot]", "type": "Bot"}
	issue := commandComment("/review")
	delete(issue["issue"].(map[string]any), "pull_request")
	for name, body := range map[string]map[string]any{
		"an edit":            edited,
		"the App's own":      own,
		"no command":         commandComment("looks good to me"),
		"a mention mid-line": commandComment("thanks @podium review later"),
		"on an issue":        issue,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := RoutineDelivery("issue_comment", "del-c2", ghBody(t, body), appIs("podium"), compileMention("podium"), routineNow)
			assert.False(t, ok)
		})
	}
}

// On the diff tab the delivery describes the pull request, base branch included.
func TestACommandOnTheDiffCarriesItsBranch(t *testing.T) {
	body := map[string]any{
		"action": "created",
		"comment": map[string]any{
			"id": 8, "body": "@podium review this function", "path": "api/handler.go", "line": 42,
			"user": map[string]any{"login": "mallory", "type": "User", "id": 99},
		},
		"pull_request": map[string]any{
			"number": 12, "title": "Fix login", "draft": false,
			"user": map[string]any{"login": "mona", "id": 5},
			"head": map[string]any{"ref": "fix-login", "sha": "abc123"}, "base": map[string]any{"ref": "main"},
		},
	}
	d, ok := RoutineDelivery("pull_request_review_comment", "del-c3", ghBody(t, body), appIs("podium"), compileMention("podium"), routineNow)
	require.True(t, ok)
	assert.Equal(t, "main", d.Facts.Base)
	_, data := keptData(t, d)
	assert.Equal(t, "api/handler.go", data["path"])
	assert.Equal(t, "abc123", data["head_sha"])
}

// keptBy makes h's routines take what is offered, n at a time, and remember it.
func keptBy(h *harness, n int) *[]store.GitHubDelivery {
	var mu sync.Mutex
	var got []store.GitHubDelivery
	h.src.routines = func(_ context.Context, d store.GitHubDelivery) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, d)
		return n, nil
	}
	return &got
}

// A command a routine took is that routine's: the comment is recorded, and nothing answers it
// as a mention, so a routine on /review and the assistant never both reply.
func TestACommandARoutineTookStartsNothingElse(t *testing.T) {
	h := newHarness(t)
	got := keptBy(h, 1)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", commentBy("@podium review", 99)).Code)

	all := h.drainAll()
	require.Len(t, all, 1, "no turn: only the comment, for the record")
	assert.True(t, all[0].RecordOnly, "the comment is still part of the pull request's record")
	require.Len(t, *got, 1)
	assert.Equal(t, "dev", (*got)[0].Facts.Base, "the base branch was read from the pull request")
}

// A command no routine answers is an ordinary comment: a mention is a mention, and /review on
// its own asks nothing of anybody.
func TestACommandNoRoutineTookIsAnOrdinaryComment(t *testing.T) {
	h := newHarness(t)
	keptBy(h, 0)
	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-1", commentBy("@podium review", 99)).Code)
	evs := h.drain()
	require.Len(t, evs, 1)
	assert.True(t, evs[0].Mentioned)

	require.Equal(t, http.StatusOK, postWebhook(t, h, "issue_comment", "d-2", commentBy("/review", 99)).Code)
	assert.Empty(t, h.drain())
}

// commentBy is prComment from a GitHub account with a numeric id, as real payloads carry it.
func commentBy(body string, id int64) map[string]any {
	p := prComment(body)
	p["comment"].(map[string]any)["user"] = map[string]any{
		"login": "mallory", "type": "User", "id": id, "avatar_url": "https://avatars/m.png",
	}
	p["sender"] = map[string]any{"login": "mallory", "type": "User", "id": id}
	p["repository"].(map[string]any)["full_name"] = "acme/repo"
	return p
}
