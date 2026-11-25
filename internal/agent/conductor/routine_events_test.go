package conductor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

func storedEvent(t *testing.T, event string, payload map[string]any) store.RoutineEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return store.RoutineEvent{Event: event, Payload: raw, ReceivedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func TestAnEventIsBriefedWithWhoWhereAndItsData(t *testing.T) {
	events := BriefEvents([]store.RoutineEvent{storedEvent(t, "Issue.update", map[string]any{
		"type": "Issue", "action": "update", "url": "https://linear.example/acme/issue/ENG-12",
		"createdAt":   "2026-01-02T03:04:00Z",
		"actor":       map[string]any{"id": "user-1", "name": "Robin", "type": "user"},
		"data":        map[string]any{"identifier": "ENG-12", "priority": 1},
		"updatedFrom": map[string]any{"priority": 3},
	})})
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "Issue.update", e.Event)
	assert.Equal(t, "issue updated", e.Label)
	assert.Equal(t, "2026-01-02T03:04:00Z", e.At, "when Linear says it happened")
	assert.Equal(t, "Robin", e.Actor)
	assert.Equal(t, "https://linear.example/acme/issue/ENG-12", e.URL)
	assert.JSONEq(t, `{"identifier":"ENG-12","priority":1}`, string(e.Data))
	assert.JSONEq(t, `{"priority":3}`, string(e.UpdatedFrom))
	assert.False(t, e.Trimmed)
}

func TestLargeEventsAreTrimmedAndTheRunStaysWithinItsBrief(t *testing.T) {
	long := strings.Repeat("words ", 4000)
	var stored []store.RoutineEvent
	for range 20 {
		stored = append(stored, storedEvent(t, "Issue.create", map[string]any{
			"type": "Issue", "action": "create",
			"data": map[string]any{"identifier": "ENG-1", "description": long, "labels": make([]any, 200)},
		}))
	}
	events := BriefEvents(stored)
	require.Len(t, events, 20, "every event is named, even past the budget")
	total := 0
	for _, e := range events {
		assert.True(t, e.Trimmed)
		assert.True(t, json.Valid(e.Data))
		assert.LessOrEqual(t, len(e.Data), maxEventDataBytes)
		total += len(e.Data)
	}
	assert.LessOrEqual(t, total, maxEventsBriefBytes+len(events)*200)
	assert.Contains(t, string(events[0].Data), "ENG-1")
	assert.Contains(t, string(events[19].Data), "Left out")
}

func TestTrimJSONAlwaysAnswersJSONWithinItsLimit(t *testing.T) {
	small := json.RawMessage(`{"a":1}`)
	out, cut := TrimJSON(small, 100)
	assert.JSONEq(t, `{"a":1}`, string(out))
	assert.False(t, cut)

	nested := map[string]any{"title": "t", "deep": map[string]any{"x": strings.Repeat("y", 5000)}}
	raw, _ := json.Marshal(nested)
	out, cut = TrimJSON(raw, 300)
	assert.True(t, cut)
	assert.True(t, json.Valid(out))
	assert.LessOrEqual(t, len(out), 300)
	assert.Contains(t, string(out), `"title":"t"`)

	out, cut = TrimJSON(json.RawMessage(`not json`), 100)
	assert.JSONEq(t, `{}`, string(out))
	assert.True(t, cut)
}

func TestARunsDividerSaysWhichLinearEventStartedIt(t *testing.T) {
	issue := storedEvent(t, "Issue.create", map[string]any{"data": map[string]any{"identifier": "ENG-12"}})
	comment := storedEvent(t, "Comment.create", map[string]any{"data": map[string]any{"issue": map[string]any{"identifier": "ENG-7"}}})
	project := storedEvent(t, "Project.update", map[string]any{"data": map[string]any{"name": "Launch"}})
	cycle := storedEvent(t, "Cycle.remove", map[string]any{"data": map[string]any{}})

	assert.Equal(t, "Linear: ENG-12 created", eventDivider([]store.RoutineEvent{issue}))
	assert.Equal(t, "Linear: comment on ENG-7 created and 2 more", eventDivider([]store.RoutineEvent{comment, issue, project}))
	assert.Equal(t, "Linear: project “Launch” updated", eventDivider([]store.RoutineEvent{project}))
	assert.Equal(t, "Linear: cycle removed", eventDivider([]store.RoutineEvent{cycle}))
	assert.Empty(t, eventDivider(nil))
}

func TestARunsDividerSaysWhichGitHubEventStartedIt(t *testing.T) {
	failed := storedEvent(t, "workflow_run.failed", map[string]any{"source": "github", "data": map[string]any{
		"repo": "acme/api", "workflow": "CI", "pull_requests": []any{map[string]any{"number": 42}},
	}})
	onBranch := storedEvent(t, "check_suite.completed", map[string]any{"source": "github", "data": map[string]any{
		"repo": "acme/api", "app": "CircleCI", "head_branch": "main",
	}})
	merged := storedEvent(t, "pull_request.merged", map[string]any{"source": "github", "data": map[string]any{
		"repo": "acme/api", "number": 7,
	}})
	review := storedEvent(t, "pull_request_review.submitted", map[string]any{"source": "github", "data": map[string]any{
		"repo": "acme/api", "number": 7,
	}})
	issue := storedEvent(t, "issues.opened", map[string]any{"source": "github", "data": map[string]any{
		"repo": "acme/web", "number": 3,
	}})

	assert.Equal(t, "GitHub: workflow “CI” failed on acme/api#42", eventDivider([]store.RoutineEvent{failed}))
	assert.Equal(t, "GitHub: workflow “CI” failed on acme/api#42 and 1 more", eventDivider([]store.RoutineEvent{failed, merged}))
	assert.Equal(t, "GitHub: checks “CircleCI” completed on acme/api@main", eventDivider([]store.RoutineEvent{onBranch}))
	assert.Equal(t, "GitHub: acme/api#7 merged", eventDivider([]store.RoutineEvent{merged}))
	assert.Equal(t, "GitHub: review submitted on acme/api#7", eventDivider([]store.RoutineEvent{review}))
	assert.Equal(t, "GitHub: issue acme/web#3 opened", eventDivider([]store.RoutineEvent{issue}))

	b := BriefEvents([]store.RoutineEvent{failed})
	require.Len(t, b, 1)
	assert.Equal(t, "workflow run failed", b[0].Label)
}
