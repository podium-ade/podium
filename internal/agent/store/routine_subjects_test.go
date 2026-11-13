package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnEventIsAboutTheTicketItConcerns(t *testing.T) {
	for name, tc := range map[string]struct {
		event, payload, key, title string
	}{
		"an issue": {"Issue.create",
			`{"data":{"id":"iss-1","identifier":"ENG-12","title":"Fix login"}}`, "Issue:iss-1", "ENG-12 Fix login"},
		"an issue with no identifier": {"Issue.update",
			`{"data":{"id":"iss-1","title":"Fix login"}}`, "Issue:iss-1", "Fix login"},
		"a comment by its issue id": {"Comment.create",
			`{"data":{"id":"cmt-1","issueId":"iss-1","body":"Any news?"}}`, "Issue:iss-1", ""},
		"a comment with its issue": {"Comment.update",
			`{"data":{"id":"cmt-1","issue":{"id":"iss-1","identifier":"ENG-12","title":"Fix login"}}}`,
			"Issue:iss-1", "ENG-12 Fix login"},
		"a project": {"Project.update",
			`{"data":{"id":"prj-1","name":"Launch"}}`, "Project:prj-1", "Launch"},
		"a project update by its project id": {"ProjectUpdate.create",
			`{"data":{"id":"pu-1","projectId":"prj-1","body":"On track"}}`, "Project:prj-1", ""},
		"a project update with its project": {"ProjectUpdate.create",
			`{"data":{"id":"pu-1","project":{"id":"prj-1","name":"Launch"}}}`, "Project:prj-1", "Launch"},
		"a cycle by its number": {"Cycle.update",
			`{"data":{"id":"cyc-1","number":7}}`, "Cycle:cyc-1", "Cycle 7"},
		"a cycle by its name": {"Cycle.create",
			`{"data":{"id":"cyc-1","number":7,"name":"Autumn"}}`, "Cycle:cyc-1", "Autumn"},
		"a label": {"IssueLabel.create",
			`{"data":{"id":"lbl-1","name":"bug"}}`, "IssueLabel:lbl-1", "bug"},
	} {
		t.Run(name, func(t *testing.T) {
			key, title := RoutineEventSubject(tc.event, json.RawMessage(tc.payload))
			assert.Equal(t, tc.key, key)
			assert.Equal(t, tc.title, title)
		})
	}
}

func TestAnEventLinearSentInAnotherShapeIsAboutNothing(t *testing.T) {
	for name, tc := range map[string]struct{ event, payload string }{
		"a comment on no issue":            {"Comment.create", `{"data":{"id":"cmt-1","body":"hi"}}`},
		"an issue with no id":              {"Issue.create", `{"data":{"identifier":"ENG-12"}}`},
		"an id that is an object":          {"Issue.create", `{"data":{"id":{"nested":true}}}`},
		"a project update with no project": {"ProjectUpdate.create", `{"data":{"id":"pu-1"}}`},
		"no data":                          {"Issue.create", `{"type":"Issue"}`},
		"data that is a list":              {"Issue.create", `{"data":[1,2]}`},
		"not JSON":                         {"Issue.create", `not json`},
		"a stub for a payload too large":   {"Issue.update", `{"type":"Issue","tooLarge":true}`},
		"a type no routine runs on":        {"Reaction.create", `{"data":{"id":"r-1"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			key, title := RoutineEventSubject(tc.event, json.RawMessage(tc.payload))
			assert.Empty(t, key)
			assert.Empty(t, title)
		})
	}
}

func TestATicketChatIsNamedForTheRoutineAndTheTicket(t *testing.T) {
	assert.Equal(t, "Triage · ENG-12 Fix login", RoutineSubjectChatTitle("Triage", "Issue:iss-1", "ENG-12 Fix login"))
	assert.Equal(t, "Triage · Issue:iss-1", RoutineSubjectChatTitle("Triage", "Issue:iss-1", " "))
	long := RoutineSubjectChatTitle("Triage", "Issue:iss-1", strings.Repeat("word ", 40))
	assert.LessOrEqual(t, len([]rune(long)), MaxChatTitleRunes)
	assert.True(t, strings.HasSuffix(long, "…"))
}

func TestAChatPerTicketIsALinearRoutinesAlone(t *testing.T) {
	d := linearDraft()
	out, err := PrepareRoutine(d)
	require.NoError(t, err)
	assert.Equal(t, RoutineChatModeRoutine, out.ChatMode, "an empty mode is the routine's own chat, as before")

	d.ChatMode = " Subject "
	out, err = PrepareRoutine(d)
	require.NoError(t, err)
	assert.Equal(t, RoutineChatModeSubject, out.ChatMode)

	d.ChatMode = "thread"
	_, err = PrepareRoutine(d)
	assert.ErrorIs(t, err, ErrInvalidRoutine)

	sched := RoutineDraft{
		Name: "Digest", Schedule: "0 9 * * *", Timezone: "UTC", Instruction: "Summarise.", NextRunAt: time.Now(),
		ChatMode: RoutineChatModeSubject,
	}
	_, err = PrepareRoutine(sched)
	require.ErrorIs(t, err, ErrInvalidRoutine)
	assert.Contains(t, err.Error(), "a chat per ticket is for a routine on Linear events")
}

func TestSwitchingToAChatPerTicketIsInTheHistory(t *testing.T) {
	r := Routine{Kind: RoutineKindLinear, Events: []string{"Issue.create"}, ChatMode: RoutineChatModeRoutine}
	per := r
	per.ChatMode = RoutineChatModeSubject
	assert.Equal(t, "On Linear: issue created · a chat per ticket", per.When())
	assert.True(t, revised(r, per))
	assert.True(t, per.PerSubject())
	assert.False(t, r.PerSubject())
	// A mode a schedule cannot have is never read as one.
	sched := Routine{Kind: RoutineKindSchedule, Schedule: "0 9 * * *", ChatMode: RoutineChatModeSubject}
	assert.False(t, sched.PerSubject())
}
