package linear

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIssues records what the app says about issues.
type fakeIssues struct {
	mu   sync.Mutex
	seen []IssueRecord
}

func (f *fakeIssues) LinearIssueSeen(_ context.Context, r IssueRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r)
	return nil
}

func (f *fakeIssues) got() []IssueRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]IssueRecord(nil), f.seen...)
}

// A delegation says what the issue is and who touched it: the person who delegated it, the
// prompt's speaker, and the creator the payload names. Accounts are named as identities keys
// them, and the app itself is nobody.
func TestADelegationRecordsTheIssueAndWhoTouchedIt(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	h.deliver(h.fixture(t, "session_created_delegation"), "delivery-1")
	h.event(t)
	got := issues.got()
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SourceKey(fixtureIssue), r.Key())
	assert.Equal(t, IssueKindIssue, r.Kind)
	assert.Equal(t, "EME-5", r.Identifier)
	assert.Equal(t, "https://linear.app/acme/issue/EME-5/test-issue", r.URL)
	who := fixtureOrg + "/" + fixtureUser
	assert.Contains(t, r.People, IssuePerson{UserID: who, Via: ViaDelegated})
	assert.Contains(t, r.People, IssuePerson{UserID: who, Via: ViaPrompted})
	for _, p := range r.People {
		assert.NotEqual(t, fixtureOrg+"/"+fixtureAppUser, p.UserID, "the app touched nothing")
	}
}

// A mention is recorded as one, and a follow-up only as a prompt: the session was opened before.
func TestAMentionAndAFollowUpRecordHowTheyTouchedTheIssue(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	h.deliver(h.fixture(t, "session_created_mention"), "delivery-1")
	h.event(t)
	h.deliver(h.fixture(t, "session_prompted_reply"), "delivery-2")
	h.event(t)
	got := issues.got()
	require.Len(t, got, 2)
	who := fixtureOrg + "/" + fixtureUser
	assert.Contains(t, got[0].People, IssuePerson{UserID: who, Via: ViaMentioned})
	assert.NotContains(t, got[1].People, IssuePerson{UserID: who, Via: ViaDelegated})
	assert.Contains(t, got[1].People, IssuePerson{UserID: who, Via: ViaPrompted})
}

// A data change says what its issue is now — its state on an update — and who touched it, with
// no routine running on Linear events, and with the app's own change too: the app moving an
// issue is still the issue's state.
func TestADataChangeRecordsItsIssueWhetherOrNotARoutineRunsOnIt(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["action"] = "update"
		data := doc["data"].(map[string]any)
		data["state"] = map[string]any{"name": "In Progress", "type": "started"}
		data["creatorId"] = "u-creator"
		data["assigneeId"] = "u-assignee"
	}), "delivery-1")
	h.settle(t)
	got := issues.got()
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SourceKey("issue-12"), r.Key())
	assert.Equal(t, "ENG-12", r.Identifier)
	assert.Equal(t, "ENG", r.TeamKey)
	assert.Equal(t, "In Progress", r.StateName)
	assert.Equal(t, "started", r.StateType)
	assert.ElementsMatch(t, []IssuePerson{
		{UserID: fixtureOrg + "/u-creator", Via: ViaCreated},
		{UserID: fixtureOrg + "/u-assignee", Via: ViaAssigned},
		{UserID: fixtureOrg + "/" + fixtureUser, Via: ViaChanged},
	}, r.People)

	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["action"] = "update"
		doc["actor"] = map[string]any{"id": fixtureAppUser, "name": "Podium", "type": "user"}
	}), "delivery-own")
	h.settle(t)
	got = issues.got()
	require.Len(t, got, 2, "the app's own change still says what the issue is")
	assert.Empty(t, got[1].People)
}

// A comment is about its issue, and its author touched it; a project update is about its
// project; a label is about no conversation.
func TestACommentIsItsIssuesAndAProjectUpdateItsProjects(t *testing.T) {
	issues := &fakeIssues{}
	h := newAppHarness(t, func(o *AppOptions) { o.Issues = issues })
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["type"] = "Comment"
		doc["data"] = map[string]any{"id": "cmt-1", "body": "hi", "issueId": "issue-12", "userId": "u-bo",
			"issue": map[string]any{"id": "issue-12", "identifier": "ENG-12", "title": "Flaky login"}}
	}), "delivery-1")
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["type"] = "ProjectUpdate"
		doc["data"] = map[string]any{"id": "pu-1", "projectId": "prj-1", "userId": "u-bo"}
	}), "delivery-2")
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["type"] = "IssueLabel"
		doc["data"] = map[string]any{"id": "lbl-1", "name": "bug"}
	}), "delivery-3")
	h.settle(t)
	got := issues.got()
	require.Len(t, got, 2)
	assert.Equal(t, SourceKey("issue-12"), got[0].Key())
	assert.Equal(t, "ENG-12", got[0].Identifier)
	assert.Contains(t, got[0].People, IssuePerson{UserID: fixtureOrg + "/u-bo", Via: ViaCommented})
	assert.Equal(t, ProjectKey("prj-1"), got[1].Key())
	assert.Contains(t, got[1].People, IssuePerson{UserID: fixtureOrg + "/u-bo", Via: ViaCommented})
}
