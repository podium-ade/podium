package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func names(login string) string {
	return map[string]string{"alice@acme.com": "Alice", "bob@acme.com": "Bob"}[login]
}

var (
	aliceTurn   = &store.TurnRunner{TurnID: "t1", Login: "alice@acme.com", Tasks: []string{"task-1"}}
	routineTurn = &store.TurnRunner{TurnID: "t2", Routine: "Nightly triage", RoutineSystem: true}
)

func shapedRow(m store.ChatMessage, run *store.TurnRunner, viewer string, full bool) *agentv1.ChatMessage {
	pb := chatMessageToProto(m)
	shapeRow(pb, m, run, viewer, full, names)
	return pb
}

// The person a turn ran as, and an admin, see all of it; everybody else reads its answer and
// sees each step as a redacted row, with none of its text, task or files.
func TestSomebodyElsesTurnIsReadButItsWorkIsNot(t *testing.T) {
	files := []store.ChatAttachment{{ArtifactID: "art-1", Name: "plan.md"}}
	tool := store.ChatMessage{Seq: 2, Role: store.RoleActivity, Text: `{"kind":"tool","tool":"bash","input":"cat ~/.env"}`, TaskID: "task-1", Via: store.ViaBot}
	thought := store.ChatMessage{Seq: 3, Role: store.RoleProgress, Text: "reading the secrets file", Via: store.ViaBot}
	answer := store.ChatMessage{Seq: 4, Role: store.RoleAssistant, Text: "Fixed in #42.", TaskID: "task-1", Attachments: files, Via: store.ViaBot}
	prompt := store.ChatMessage{Seq: 1, Role: store.RoleUser, Text: "@Podium fix it", AuthorLogin: "alice@acme.com", Via: store.ViaLinear}

	for _, who := range []struct {
		name   string
		viewer string
		full   bool
	}{{"its runner", "alice@acme.com", false}, {"an admin", "carol@acme.com", true}} {
		t.Run(who.name, func(t *testing.T) {
			pb := shapedRow(tool, aliceTurn, who.viewer, who.full)
			assert.False(t, pb.GetRedacted())
			assert.Equal(t, tool.Text, pb.GetText())
			assert.Equal(t, "task-1", pb.GetTaskId())
			assert.Equal(t, "alice@acme.com", pb.GetRanAs())
			assert.Equal(t, "Alice", pb.GetRanAsName())
			assert.Len(t, shapedRow(answer, aliceTurn, who.viewer, who.full).GetAttachments(), 1)
		})
	}

	t.Run("anybody else", func(t *testing.T) {
		for _, m := range []store.ChatMessage{tool, thought} {
			pb := shapedRow(m, aliceTurn, "bob@acme.com", false)
			assert.True(t, pb.GetRedacted(), m.Role)
			assert.Empty(t, pb.GetText(), m.Role)
			assert.Empty(t, pb.GetTaskId(), m.Role)
			assert.Equal(t, "alice@acme.com", pb.GetRanAs(), "it still says whose turn it was")
			assert.Equal(t, "Alice", pb.GetRanAsName())
		}
		pb := shapedRow(answer, aliceTurn, "bob@acme.com", false)
		assert.False(t, pb.GetRedacted(), "the answer is read: Linear shows it to everybody")
		assert.Equal(t, "Fixed in #42.", pb.GetText())
		assert.Empty(t, pb.GetTaskId(), "but not the task behind it")
		assert.Empty(t, pb.GetAttachments(), "nor the files its tools produced")

		user := shapedRow(prompt, aliceTurn, "bob@acme.com", false)
		assert.Equal(t, "@Podium fix it", user.GetText(), "a prompt is read")
		assert.Empty(t, user.GetRanAs(), "a person's message is theirs, not a turn's")
	})
}

// A row no turn can be placed on is nobody's, so only an admin sees it; a system routine's run
// is the company's.
func TestARowOfNobodysTurnIsAnAdminsAlone(t *testing.T) {
	tool := store.ChatMessage{Role: store.RoleActivity, Text: "{}", Via: store.ViaBot}
	assert.True(t, shapedRow(tool, nil, "alice@acme.com", false).GetRedacted())
	assert.False(t, shapedRow(tool, nil, "carol@acme.com", true).GetRedacted())

	pb := shapedRow(tool, routineTurn, "alice@acme.com", false)
	assert.True(t, pb.GetRedacted())
	assert.Empty(t, pb.GetRanAs())
	assert.Equal(t, "Nightly triage", pb.GetRanAsRoutine())
}

func TestARailIssueWaitsOnItsAskerOrOnAnybody(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	assert.Nil(t, linearIssueToProto(nil, "alice@acme.com"))

	quiet := linearIssueToProto(&store.ChatLinearIssue{Identifier: "EME-6", Title: "Login", StateName: "In Progress", StateType: "started"}, "alice@acme.com")
	assert.Equal(t, "EME-6", quiet.GetIdentifier())
	assert.Equal(t, "started", quiet.GetStateType())
	assert.Nil(t, quiet.GetAskedAt())
	assert.False(t, quiet.GetWaitingOnMe())

	asked := &store.ChatLinearIssue{Identifier: "EME-6", AskedAt: &at, AskedOf: "alice@acme.com"}
	assert.True(t, linearIssueToProto(asked, "alice@acme.com").GetWaitingOnMe())
	assert.False(t, linearIssueToProto(asked, "bob@acme.com").GetWaitingOnMe(), "Alice's turn asked Alice")
	assert.True(t, linearIssueToProto(asked, "bob@acme.com").GetAskedAt().AsTime().Equal(at))

	nobodys := &store.ChatLinearIssue{AskedAt: &at}
	assert.True(t, linearIssueToProto(nobodys, "bob@acme.com").GetWaitingOnMe(), "a question nobody's turn asked is everybody's")
}

func TestATurnsTaskIsItsRunnersInTheTurnList(t *testing.T) {
	turn := func() *agentv1.Turn {
		return &agentv1.Turn{Id: "t1", TaskId: "task-1", StartedBy: "alice@acme.com", FinalText: "done", StartedAt: timestamppb.Now()}
	}
	mine := turn()
	turnForReader(mine, "alice@acme.com", false)
	assert.Equal(t, "task-1", mine.GetTaskId())
	admin := turn()
	turnForReader(admin, "carol@acme.com", true)
	assert.Equal(t, "task-1", admin.GetTaskId())
	theirs := turn()
	turnForReader(theirs, "bob@acme.com", false)
	assert.Empty(t, theirs.GetTaskId())
	assert.Equal(t, "done", theirs.GetFinalText())
}

// A live progress line or task id of a turn the reader did not run says only that it works.
func TestALiveFrameOfSomebodyElsesTurnSaysOnlyThatItWorks(t *testing.T) {
	v := &runnerView{linear: true, viewer: "bob@acme.com",
		ix: store.NewTurnIndex([]store.TurnRunner{*aliceTurn}), loaded: time.Now().Add(time.Hour)}
	progress := &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Progress{Progress: "reading ~/.env"}}
	assert.Equal(t, conductor.Placeholder, v.frame(t.Context(), progress).GetProgress())
	status := &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Status{Status: &agentv1.ChatStatus{State: "started", TaskId: "task-1"}}}
	got := v.frame(t.Context(), status).GetStatus()
	assert.Equal(t, "started", got.GetState())
	assert.Empty(t, got.GetTaskId())

	v.viewer = "alice@acme.com"
	assert.Equal(t, "reading ~/.env", v.frame(t.Context(), progress).GetProgress())
	assert.Equal(t, "task-1", v.frame(t.Context(), status).GetStatus().GetTaskId())
}
