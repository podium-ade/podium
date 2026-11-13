package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSink records the data changes the app hands over.
type fakeSink struct {
	mu      sync.Mutex
	changes []DataChange
}

func (f *fakeSink) LinearDataChange(_ context.Context, d DataChange) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, d)
	return 1, nil
}

func (f *fakeSink) got() []DataChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DataChange(nil), f.changes...)
}

// issueCreated is a data change as Linear sends one when an issue is created, with invented
// values. edit changes it before it is signed.
func (h *appHarness) issueCreated(t *testing.T, edit ...func(map[string]any)) []byte {
	t.Helper()
	doc := map[string]any{
		"action": "create", "type": "Issue", "organizationId": fixtureOrg,
		"createdAt": "2026-11-13T10:00:00.000Z", "url": "https://linear.example/acme/issue/ENG-12/flaky-login",
		"webhookTimestamp": h.clock.Now().UnixMilli(), "webhookId": "webhook-1",
		"actor": map[string]any{"id": fixtureUser, "name": "Ada", "email": fixtureEmail, "type": "user"},
		"data": map[string]any{
			"id": "issue-12", "identifier": "ENG-12", "title": "Login is flaky on a secret title",
			"teamId": fixtureTeam, "team": map[string]any{"id": fixtureTeam, "key": "ENG", "name": "Engineering"},
		},
	}
	for _, e := range edit {
		e(doc)
	}
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return out
}

func TestADataChangeIsHandedToTheRoutinesWithoutWhatIsNotNeeded(t *testing.T) {
	sink := &fakeSink{}
	h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
	rec := h.deliver(h.issueCreated(t), "delivery-1")
	assert.Equal(t, http.StatusOK, rec.Code)
	h.settle(t)

	got := sink.got()
	require.Len(t, got, 1)
	d := got[0]
	assert.Equal(t, "delivery-1", d.DeliveryID)
	assert.Equal(t, "Issue.create", d.Event())
	assert.Equal(t, "ENG", d.TeamKey)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(d.Payload, &payload))
	assert.Equal(t, "create", payload["action"])
	assert.Equal(t, "Issue", payload["type"])
	assert.Equal(t, "https://linear.example/acme/issue/ENG-12/flaky-login", payload["url"])
	assert.Equal(t, map[string]any{"id": fixtureUser, "name": "Ada", "type": "user"}, payload["actor"],
		"who made the change, without their email")
	assert.NotContains(t, payload, "organizationId")
	assert.NotContains(t, payload, "webhookId")
	assert.Equal(t, "ENG-12", payload["data"].(map[string]any)["identifier"])

	// Counted under one label value whatever the entity, and the body is never logged.
	assert.Equal(t, 1.0, h.deliveries(t, deliveryTypeData, DeliveryAccepted))
	assert.NotContains(t, h.log.String(), "secret title")
	h.noEvent(t)

	// A retried delivery is handed over once.
	h.deliver(h.issueCreated(t), "delivery-1")
	h.settle(t)
	assert.Len(t, sink.got(), 1)
}

func TestTheAppsOwnChangeIsNotHandedOver(t *testing.T) {
	sink := &fakeSink{}
	h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["actor"] = map[string]any{"id": fixtureAppUser, "name": "Podium", "type": "user"}
	}), "delivery-own")
	h.settle(t)
	assert.Empty(t, sink.got(), "a routine that acts through the app cannot start itself")
	assert.Equal(t, 1.0, h.deliveries(t, deliveryTypeData, DeliveryIgnored))
	assert.Contains(t, h.log.String(), "the app's own change")
}

func TestADataChangeIsIgnoredWithNoSinkOrARevokedAppOrAnotherWorkspace(t *testing.T) {
	t.Run("no sink", func(t *testing.T) {
		h := newAppHarness(t)
		h.deliver(h.issueCreated(t), "delivery-1")
		h.settle(t)
		assert.Equal(t, 1.0, h.deliveries(t, deliveryTypeData, DeliveryIgnored))
	})
	t.Run("revoked", func(t *testing.T) {
		sink := &fakeSink{}
		h := newAppHarness(t, func(o *AppOptions) { o.Events, o.Revoked = sink, true })
		h.deliver(h.issueCreated(t), "delivery-1")
		h.settle(t)
		assert.Empty(t, sink.got())
	})
	t.Run("another workspace", func(t *testing.T) {
		sink := &fakeSink{}
		h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
		h.deliver(h.issueCreated(t, func(doc map[string]any) { doc["organizationId"] = "another-org" }), "delivery-1")
		h.settle(t)
		assert.Empty(t, sink.got())
	})
	t.Run("not a data change", func(t *testing.T) {
		sink := &fakeSink{}
		h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
		h.deliver(h.issueCreated(t, func(doc map[string]any) { doc["action"] = "archive" }), "delivery-1")
		h.settle(t)
		assert.Empty(t, sink.got())
		assert.Equal(t, 1.0, h.deliveries(t, "other", DeliveryIgnored))
	})
}

func TestATeamNamedOnlyByIdIsAskedOfLinearOnce(t *testing.T) {
	sink := &fakeSink{}
	h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
	h.stub.on("PodiumTeamKey", func(vars map[string]any) stubReply {
		return stubReply{Data: map[string]any{"team": map[string]any{"id": vars["id"], "key": "ENG"}}}
	})
	comment := func(id string) []byte {
		return h.issueCreated(t, func(doc map[string]any) {
			doc["type"] = "Comment"
			doc["data"] = map[string]any{"id": id, "body": "hello", "issue": map[string]any{"id": "issue-12", "teamId": fixtureTeam}}
		})
	}
	h.deliver(comment("comment-1"), "delivery-1")
	h.deliver(comment("comment-2"), "delivery-2")
	h.settle(t)

	got := sink.got()
	require.Len(t, got, 2)
	assert.Equal(t, "Comment.create", got[0].Event())
	assert.Equal(t, "ENG", got[0].TeamKey)
	assert.Equal(t, "ENG", got[1].TeamKey)
	assert.Len(t, h.stub.callsOf("PodiumTeamKey"), 1)
}

func TestADataChangeThatNamesNoTeamIsHandedOverWithNone(t *testing.T) {
	sink := &fakeSink{}
	h := newAppHarness(t, func(o *AppOptions) { o.Events = sink })
	h.deliver(h.issueCreated(t, func(doc map[string]any) {
		doc["type"] = "Project"
		doc["action"] = "update"
		doc["data"] = map[string]any{"id": "project-1", "name": "Launch", "teamIds": []string{fixtureTeam}}
		doc["updatedFrom"] = map[string]any{"name": "Prelaunch"}
		doc["actor"] = nil
	}), "delivery-1")
	h.settle(t)
	got := sink.got()
	require.Len(t, got, 1)
	assert.Empty(t, got[0].TeamKey)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(got[0].Payload, &payload))
	assert.Equal(t, map[string]any{"name": "Prelaunch"}, payload["updatedFrom"])
	assert.Nil(t, payload["actor"])
}
