package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func linearDraft() RoutineDraft {
	return RoutineDraft{
		Name: "Triage new issues", Kind: RoutineKindLinear, Timezone: "UTC",
		Instruction: "Label each new issue by its area.",
		Events:      []string{"comment.create", "Issue.create", "ISSUE.CREATE"},
		Teams:       []string{" eng ", "ENG", "web"},
	}
}

func TestALinearRoutineNamesItsEventsAndTeamsOnceEach(t *testing.T) {
	d, err := PrepareRoutine(linearDraft())
	require.NoError(t, err)
	assert.Equal(t, RoutineKindLinear, d.Kind)
	// In the order the UI lists them, whatever the order or case they came in.
	assert.Equal(t, []string{"Issue.create", "Comment.create"}, d.Events)
	assert.Equal(t, []string{"ENG", "WEB"}, d.Teams)
	assert.Empty(t, d.Schedule)
	// Its next run is never read, but the column needs one.
	assert.False(t, d.NextRunAt.IsZero())
}

func TestALinearRoutineRefusesWhatItCannotRunOn(t *testing.T) {
	for name, edit := range map[string]func(*RoutineDraft){
		"no events":           func(d *RoutineDraft) { d.Events = nil },
		"an unknown entity":   func(d *RoutineDraft) { d.Events = []string{"Reaction.create"} },
		"an unknown action":   func(d *RoutineDraft) { d.Events = []string{"Issue.archive"} },
		"no action":           func(d *RoutineDraft) { d.Events = []string{"Issue"} },
		"a schedule as well":  func(d *RoutineDraft) { d.Schedule = "0 9 * * *" },
		"a team that is text": func(d *RoutineDraft) { d.Teams = []string{"the web team"} },
		"an unknown kind":     func(d *RoutineDraft) { d.Kind = "github" },
	} {
		t.Run(name, func(t *testing.T) {
			d := linearDraft()
			edit(&d)
			_, err := PrepareRoutine(d)
			assert.ErrorIs(t, err, ErrInvalidRoutine)
		})
	}
}

func TestARoutineOnAScheduleNamesNoLinearEvents(t *testing.T) {
	d := RoutineDraft{
		Name: "Digest", Schedule: "0 9 * * *", Timezone: "UTC", Instruction: "Summarise.", NextRunAt: time.Now(),
	}
	out, err := PrepareRoutine(d)
	require.NoError(t, err)
	assert.Equal(t, RoutineKindSchedule, out.Kind, "an empty kind is a schedule, as every routine before was")
	assert.Equal(t, []string{}, out.Events)
	assert.Equal(t, []string{}, out.Teams)

	d.Events = []string{"Issue.create"}
	_, err = PrepareRoutine(d)
	assert.ErrorIs(t, err, ErrInvalidRoutine)
}

func TestALinearTriggerIsSaidInWords(t *testing.T) {
	assert.Equal(t, "On Linear: issue created, comment created (ENG)",
		DescribeLinearTrigger([]string{"Issue.create", "Comment.create"}, []string{"ENG"}))
	assert.Equal(t, "On Linear: project update removed, issue label updated",
		DescribeLinearTrigger([]string{"ProjectUpdate.remove", "IssueLabel.update"}, nil))
	assert.Equal(t, "On Linear", DescribeLinearTrigger(nil, nil))

	r := Routine{Kind: RoutineKindLinear, Events: []string{"Issue.update"}}
	assert.Equal(t, "On Linear: issue updated", r.When())
	r = Routine{Kind: RoutineKindSchedule, Schedule: "0 9 * * *"}
	assert.Equal(t, "0 9 * * *", r.When(), "a schedule's history records the cron expression as before")
}

func TestAPayloadTooLargeToKeepIsAStubThatSaysSo(t *testing.T) {
	small := json.RawMessage(`{"type":"Issue","action":"create","data":{"id":"i-1"}}`)
	assert.Equal(t, small, BoundRoutineEventPayload(small))

	big, err := json.Marshal(map[string]any{
		"type": "Issue", "action": "update", "url": "https://linear.example/acme/issue/ENG-1",
		"data": map[string]any{"description": strings.Repeat("x", MaxRoutineEventPayloadBytes)},
	})
	require.NoError(t, err)
	var stub map[string]any
	require.NoError(t, json.Unmarshal(BoundRoutineEventPayload(big), &stub))
	assert.Equal(t, "Issue", stub["type"])
	assert.Equal(t, "update", stub["action"])
	assert.Equal(t, "https://linear.example/acme/issue/ENG-1", stub["url"])
	assert.Equal(t, true, stub["tooLarge"])
	assert.NotContains(t, stub, "data")
}
