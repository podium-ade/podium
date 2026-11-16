package api

// Who a turn ran as, and what of it its readers see (docs/linear-agent-plan.md, "Ticket
// chats").
//
// A Linear issue's conversation is read by everybody who touched the issue and by admins, and
// each of its turns runs as whoever prompted it: their secrets, their MCP servers, their GitHub
// account. So every row the bot wrote there says whose turn it was (ran_as), and what the turn
// did on its way — its tool calls with their inputs and outputs, its thoughts and progress
// lines, the task it ran and the files that task produced — is shown only to the person it ran
// as and to admins and owners. Everybody else gets the turn's prompt and its answer, which
// Linear shows them anyway, and a redacted row in place of each step, which the UI counts into
// "4 steps, ran as Alice". The rule is applied here, on what leaves the API, not in the UI.
//
// It is applied to Linear conversations only. A shared web chat or a mirrored Slack thread runs
// each turn as its asker too, but the people in one chose to share it and have always seen its
// whole trail; changing that is a decision of its own.
//
// A row is the work of the turn that ran its task — the turn's own task, or one it delegated —
// and a row with no task is the assistant's own, said while its turn was the one running: a
// conversation runs one turn at a time (queue.go), and a turn's row is written before it says
// anything. A row that cannot be placed — a task no turn of the conversation ran — is nobody's,
// and shown in full only to admins.

import (
	"context"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// botRow is whether a message is something the bot said, which a turn wrote.
func botRow(m store.ChatMessage) bool {
	switch m.Role {
	case store.RoleUser, store.RoleNotice:
		return false
	}
	return true
}

// shapeRow puts who ran the turn on one of the bot's rows, and withholds what the reader may
// not see of it. full is a reader who sees every turn in full (an admin, an owner, the dev
// token); otherwise only the turn's runner does. name resolves a login to a display name.
func shapeRow(pb *agentv1.ChatMessage, m store.ChatMessage, run *store.TurnRunner, viewer string, full bool,
	name func(string) string,
) {
	if !botRow(m) {
		return
	}
	if run != nil {
		pb.RanAs = run.Login
		pb.RanAsRoutine = run.Routine
		if run.Login != "" && name != nil {
			pb.RanAsName = name(run.Login)
		}
	}
	if full || (run != nil && run.Login != "" && run.Login == viewer) {
		return
	}
	// Somebody else's turn. The task, and the files its tools produced, are its runner's.
	pb.TaskId = ""
	pb.Attachments = nil
	if m.Role == store.RoleAssistant && !pb.GetDeleted() {
		// The answer is read: Linear shows it to everybody on the issue.
		return
	}
	pb.Redacted = true
	pb.Text = ""
}

// runnerView is one reader's view of one chat's turns, for a stream that lives as long as
// they watch. A Linear conversation read by somebody who is not an admin is shaped; every other
// chat, and every admin, gets rows as they are, with ran_as on a Linear conversation's.
type runnerView struct {
	s      *AgentService
	chatID string
	viewer string
	linear bool
	full   bool
	ix     store.TurnIndex
	loaded time.Time
	names  map[string]string
}

func (s *AgentService) newRunnerView(ctx context.Context, row store.Chat, viewer string) *runnerView {
	return &runnerView{
		s:      s,
		chatID: row.ID,
		viewer: viewer,
		linear: row.Origin == store.OriginLinear || store.IsLinearConversation(row.SourceKey),
		full:   Infra(ctx),
		names:  map[string]string{},
	}
}

// refresh re-reads the conversation's turns when a row may be newer than what was read: a
// turn that started since would otherwise have its rows placed on the one before it.
func (v *runnerView) refresh(ctx context.Context, at time.Time) error {
	if !v.loaded.IsZero() && at.Before(v.loaded) {
		return nil
	}
	now := time.Now().UTC()
	turns, err := v.s.store.ChatTurnRunners(ctx, v.chatID)
	if err != nil {
		return err
	}
	v.ix, v.loaded = store.NewTurnIndex(turns), now
	return nil
}

func (v *runnerView) name(login string) string {
	if n, ok := v.names[login]; ok {
		return n
	}
	n, _, err := v.s.store.MemberProfile(context.Background(), login)
	if err != nil {
		n = ""
	}
	v.names[login] = n
	return n
}

// message is one stored row as this reader may see it.
func (v *runnerView) message(ctx context.Context, m store.ChatMessage) (*agentv1.ChatMessage, error) {
	pb := chatMessageToProto(m)
	if !v.linear || !botRow(m) {
		return pb, nil
	}
	if err := v.refresh(ctx, m.TS); err != nil {
		return nil, err
	}
	shapeRow(pb, m, v.ix.TurnOf(m.TaskID, m.TS), v.viewer, v.full, v.name)
	return pb, nil
}

// messageFrame is message as a frame's payload.
func (v *runnerView) messageFrame(ctx context.Context, m store.ChatMessage) (*agentv1.ChatFrame_Message, error) {
	pb, err := v.message(ctx, m)
	if err != nil {
		return nil, err
	}
	return &agentv1.ChatFrame_Message{Message: pb}, nil
}

// mine is whether the newest turn is one this reader sees in full, for what a live frame
// says about the turn in flight: its progress line and its task.
func (v *runnerView) mine(ctx context.Context) bool {
	if !v.linear || v.full {
		return true
	}
	if err := v.refresh(ctx, time.Now().UTC()); err != nil {
		return false
	}
	t := v.ix.Latest()
	return t != nil && t.Login != "" && t.Login == v.viewer
}

// frame is one live frame as this reader may see it.
func (v *runnerView) frame(ctx context.Context, f *agentv1.ChatFrame) *agentv1.ChatFrame {
	switch x := f.GetFrame().(type) {
	case *agentv1.ChatFrame_Progress:
		if !v.mine(ctx) && x.Progress != conductor.Placeholder {
			return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Progress{Progress: conductor.Placeholder}}
		}
	case *agentv1.ChatFrame_Status:
		if x.Status.GetTaskId() != "" && !v.mine(ctx) {
			return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Status{Status: &agentv1.ChatStatus{State: x.Status.GetState()}}}
		}
	}
	return f
}

// turnForReader shapes one turn of ListTurns: the task of a turn the reader did not run, in a
// Linear conversation, is its runner's.
func turnForReader(t *agentv1.Turn, viewer string, full bool) {
	if full {
		return
	}
	login := t.GetStartedBy()
	if login != "" && login == viewer {
		return
	}
	t.TaskId = ""
}
