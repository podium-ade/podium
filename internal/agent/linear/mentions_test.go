package linear

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMentionHandlesReadsProfileLinksAndAtHandles(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"a profile link", "cc https://linear.app/acme/profiles/ada please", []string{"ada"}},
		{"a markdown link to a profile", "[@Ada](https://linear.app/acme/profiles/Ada) look", []string{"ada"}},
		{"typed", "@grace can you look? and @bo.b, too", []string{"grace", "bo.b"}},
		{"at the start, after punctuation", "@ada: (@bo) @cy.", []string{"ada", "bo", "cy"}},
		{"an escaped handle", "https://linear.app/acme/profiles/j%C3%BCrgen", []string{"jürgen"}},
		{"an email is no mention", "write to ada@acme.com", nil},
		{"a path is no mention", "see acme.com/@ada and /@bo", nil},
		{"code is no mention", "run `npm i @types/node` then\n```\n@ada inside\n```\nok", nil},
		{"each once", "@ada @ADA https://linear.app/acme/profiles/ada", []string{"ada"}},
		{"another site's profiles", "https://example.com/acme/profiles/ada", nil},
		{"nothing", "", nil},
	} {
		assert.Equal(t, tc.want, mentionHandles(tc.text), tc.name)
	}
}

func TestMentionHandlesIsBounded(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		b.WriteString("@user")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" ")
	}
	assert.LessOrEqual(t, len(mentionHandles(b.String())), maxMentions)

	long := strings.Repeat("a", maxMentionText) + " @late"
	assert.Empty(t, mentionHandles(long), "past the bound the text is not read")
}

// A comment's @mention is recorded as the person it names, resolved through one read of the
// workspace's users, which later deliveries reuse. The app, and a handle nobody has, are nobody.
func TestACommentRecordsWhoItMentions(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	reads := 0
	h.stub.on("PodiumUsers", func(map[string]any) stubReply {
		h.mu.Lock()
		defer h.mu.Unlock()
		reads++
		return stubReply{Data: map[string]any{"users": map[string]any{
			"nodes": []any{
				map[string]any{"id": "u-ada", "displayName": "ada"},
				map[string]any{"id": "u-grace", "displayName": "Grace"},
				map[string]any{"id": fixtureAppUser, "displayName": "podium"},
			},
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
		}}}
	})
	comment := func(body string) func(map[string]any) {
		return func(doc map[string]any) {
			doc["type"] = "Comment"
			doc["data"] = map[string]any{"id": "cmt-1", "body": body, "issueId": "issue-12", "userId": "u-bo"}
		}
	}
	h.deliver(h.issueCreated(t, comment("@podium ask https://linear.app/acme/profiles/grace and @ada, not @nobody")), "delivery-1")
	h.settle(t)
	h.deliver(h.issueCreated(t, comment("thanks @ada")), "delivery-2")
	h.settle(t)

	got := issues.got()
	require.Len(t, got, 2)
	assert.Contains(t, got[0].People, IssuePerson{UserID: fixtureOrg + "/u-grace", Via: ViaPersonMentioned})
	assert.Contains(t, got[0].People, IssuePerson{UserID: fixtureOrg + "/u-ada", Via: ViaPersonMentioned})
	assert.Contains(t, got[0].People, IssuePerson{UserID: fixtureOrg + "/u-bo", Via: ViaCommented})
	for _, p := range got[0].People {
		assert.NotEqual(t, fixtureOrg+"/"+fixtureAppUser, p.UserID, "the app is nobody")
	}
	assert.Contains(t, got[1].People, IssuePerson{UserID: fixtureOrg + "/u-ada", Via: ViaPersonMentioned})
	count := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return reads
	}
	assert.Equal(t, 1, count(), "the second delivery reuses the first one's read")

	h.deliver(h.issueCreated(t, comment("@nobody again")), "delivery-3")
	h.settle(t)
	assert.Equal(t, 1, count(), "within the minute, a handle nobody has asks nothing")

	h.clock.advance(usersRetry + time.Second)
	h.deliver(h.issueCreated(t, comment("@ada once more")), "delivery-4")
	h.settle(t)
	assert.Equal(t, 1, count(), "a handle the list has asks nothing until the list is old")
	h.deliver(h.issueCreated(t, comment("@nobody, a minute on")), "delivery-5")
	h.settle(t)
	assert.Equal(t, 2, count(), "a minute on, a handle the list lacks reads it again: a new member")

	h.clock.advance(usersTTL)
	h.deliver(h.issueCreated(t, comment("@ada")), "delivery-6")
	h.settle(t)
	assert.Equal(t, 3, count(), "an old list is read again")
}

// An issue's description is read for mentions too; a removal says nobody.
func TestAnIssueDescriptionRecordsWhoItMentions(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	h.stub.reply("PodiumUsers", map[string]any{"users": map[string]any{
		"nodes":    []any{map[string]any{"id": "u-ada", "displayName": "ada"}},
		"pageInfo": map[string]any{"hasNextPage": false},
	}})
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["data"].(map[string]any)["description"] = "Steps from @ada:\n1. log in"
	}), "delivery-1")
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["action"] = "remove"
		doc["data"].(map[string]any)["description"] = "@ada"
	}), "delivery-2")
	h.settle(t)
	got := issues.got()
	require.Len(t, got, 2)
	assert.Contains(t, got[0].People, IssuePerson{UserID: fixtureOrg + "/u-ada", Via: ViaPersonMentioned})
	assert.NotContains(t, got[1].People, IssuePerson{UserID: fixtureOrg + "/u-ada", Via: ViaPersonMentioned})
}
