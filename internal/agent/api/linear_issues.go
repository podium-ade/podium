package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/tickets"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// LinearIssueReader reads Linear issues for a person, and only those they could see in Linear
// (tickets.Reader).
type LinearIssueReader interface {
	// Lookup is tickets.ErrNotFound for an issue there is none of, or the viewer may not see.
	Lookup(ctx context.Context, v tickets.Viewer, ref linear.IssueRef, detail bool) (tickets.Issue, error)
	// KnownTeams is the team keys an identifier may be of.
	KnownTeams(ctx context.Context) map[string]bool
}

const (
	// maxLinearIssueRefs bounds one GetLinearIssues. A chat names a handful.
	maxLinearIssueRefs = 25
	// linearIssuesTimeout bounds asking Linear for them. A chip that does not arrive is the
	// identifier as plain text, which is what it was.
	linearIssuesTimeout = 10 * time.Second
	// linearPanelRows is how many tasks and routine runs the panel lists.
	linearPanelRows = 20
)

// GetLinearIssues describes the issues a chat names to the caller, leaving out each one the
// caller could not see in Linear exactly as it leaves out an identifier no team has (package
// tickets says who may see what). An admin or owner sees every one.
func (s *AgentService) GetLinearIssues(
	ctx context.Context, req *connect.Request[agentv1.GetLinearIssuesRequest],
) (*connect.Response[agentv1.GetLinearIssuesResponse], error) {
	v, err := linearViewer(ctx)
	if err != nil {
		return nil, err
	}
	out := &agentv1.GetLinearIssuesResponse{}
	if s.linearIssues == nil {
		return connect.NewResponse(out), nil
	}
	refs := req.Msg.GetRefs()
	if len(refs) > maxLinearIssueRefs {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at most 25 Linear issues at once"))
	}
	ctx, cancel := context.WithTimeout(ctx, linearIssuesTimeout)
	defer cancel()
	// An identifier of a team the workspace does not have is no issue, and Linear is not asked
	// about it: that is what keeps UTF-8 and ISO-8601 from costing a request each.
	var known map[string]bool
	seen := map[string]bool{}
	for _, raw := range refs {
		ref, ok := linear.ParseIssueRef(raw)
		if !ok || seen[raw] {
			continue
		}
		if ref.Identifier != "" {
			if known == nil {
				known = s.linearIssues.KnownTeams(ctx)
			}
			if !known[ref.TeamKey()] {
				continue
			}
		}
		seen[raw] = true
		issue, err := s.linearIssues.Lookup(ctx, v, ref, req.Msg.GetDetail())
		if errors.Is(err, tickets.ErrNotFound) {
			continue
		}
		if err != nil {
			s.logger.WarnContext(ctx, "reading a Linear issue failed", "ref", raw, "error", err)
			continue
		}
		pb := ticketToProto(issue)
		pb.Ref = raw
		pb.ChatId = s.readableLinearChat(ctx, issue.Key(), v.Login)
		out.Issues = append(out.Issues, pb)
	}
	return connect.NewResponse(out), nil
}

// GetLinearIssueConversation is the panel of an issue's conversation, to whoever may read the
// conversation: reading it already shows what the issue says, so the panel shows nobody more
// than the chat does.
func (s *AgentService) GetLinearIssueConversation(
	ctx context.Context, req *connect.Request[agentv1.GetLinearIssueConversationRequest],
) (*connect.Response[agentv1.GetLinearIssueConversationResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	v, err := linearViewer(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, storeError(err)
	}
	ok, err := s.mayReadChat(ctx, c, v.Login)
	if err != nil {
		return nil, storeError(err)
	}
	kind, id := store.LinearKind(c.SourceKey)
	if !ok || kind == "" {
		// Somebody else's chat, or no Linear conversation: as if there were none.
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such Linear conversation"))
	}
	out := &agentv1.GetLinearIssueConversationResponse{}
	if kind == store.LinearKindIssue {
		out.Issue = s.panelIssue(ctx, c.SourceKey, id)
		if out.Issue != nil {
			out.Issue.ChatId = c.ID
		}
	}
	if sess, err := s.store.GetSessionByKey(ctx, c.SourceKey); err == nil {
		tasks, err := s.store.SessionDelegations(ctx, sess.ID, linearPanelRows)
		if err != nil {
			return nil, storeError(err)
		}
		for _, d := range tasks {
			t := &agentv1.LinearConversationTask{
				TaskId: d.TaskID, Playbook: d.Playbook, Status: d.Status, CreatedAt: timestamppb.New(d.CreatedAt),
			}
			if d.FinishedAt != nil {
				t.FinishedAt = timestamppb.New(*d.FinishedAt)
			}
			out.Tasks = append(out.Tasks, t)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, storeError(err)
	}
	runs, err := s.store.ChatRoutineRuns(ctx, c.ID, linearPanelRows)
	if err != nil {
		return nil, storeError(err)
	}
	for _, r := range runs {
		pb := &agentv1.LinearConversationRoutineRun{
			Id: r.ID, RoutineId: r.RoutineID, RoutineName: r.RoutineName, Status: r.Status, Summary: r.Summary,
			CreatedAt: timestamppb.New(r.CreatedAt),
		}
		if r.FinishedAt != nil {
			pb.FinishedAt = timestamppb.New(*r.FinishedAt)
		}
		out.RoutineRuns = append(out.RoutineRuns, pb)
	}
	return connect.NewResponse(out), nil
}

// panelIssue is the conversation's issue as Linear has it now, or as the webhooks described it
// when Linear cannot be asked. The reader of the conversation is shown it as an admin would
// be: the conversation is the issue's, and they may read that.
func (s *AgentService) panelIssue(ctx context.Context, sourceKey, issueID string) *agentv1.LinearIssue {
	if s.linearIssues != nil {
		ctx, cancel := context.WithTimeout(ctx, linearIssuesTimeout)
		defer cancel()
		issue, err := s.linearIssues.Lookup(ctx, tickets.Viewer{Admin: true}, linear.IssueRef{ID: issueID}, true)
		if err == nil {
			return ticketToProto(issue)
		}
		if !errors.Is(err, tickets.ErrNotFound) {
			s.logger.WarnContext(ctx, "reading a conversation's Linear issue failed", "issue", issueID, "error", err)
		}
	}
	row, err := s.store.GetLinearIssue(ctx, sourceKey)
	if err != nil {
		return nil
	}
	return &agentv1.LinearIssue{
		Id: row.LinearID, Identifier: row.Identifier, Title: row.Title, Url: row.URL, TeamKey: row.TeamKey,
		StateName: row.StateName, StateType: row.StateType, UpdatedAt: timestamppb.New(row.UpdatedAt), Cached: true,
	}
}

// readableLinearChat is the issue's conversation when there is one the caller may read, and ""
// otherwise.
func (s *AgentService) readableLinearChat(ctx context.Context, sourceKey, login string) string {
	if s.store == nil {
		return ""
	}
	c, err := s.store.ChatBySourceKey(ctx, sourceKey)
	if err != nil || c.DeletedAt != nil {
		return ""
	}
	ok, err := s.mayReadChat(ctx, c, login)
	if err != nil || !ok {
		return ""
	}
	return c.ID
}

// linearViewer is the caller as the ticket rule sees them: an admin, an owner or the dev token
// sees every issue, and anybody else is their login.
func linearViewer(ctx context.Context) (tickets.Viewer, error) {
	if Infra(ctx) {
		login := Login(ctx)
		if login == "unknown" {
			login = ""
		}
		return tickets.Viewer{Login: login, Admin: true}, nil
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return tickets.Viewer{}, err
	}
	return tickets.Viewer{Login: login}, nil
}

func ticketToProto(i tickets.Issue) *agentv1.LinearIssue {
	pb := &agentv1.LinearIssue{
		Id: i.ID, Identifier: i.Identifier, Title: i.Title, Url: i.URL, TeamKey: i.TeamKey, TeamName: i.TeamName,
		StateName: i.StateName, StateType: i.StateType, Assignee: i.Assignee, Labels: i.Labels, Cached: i.Cached,
	}
	if !i.UpdatedAt.IsZero() {
		pb.UpdatedAt = timestamppb.New(i.UpdatedAt)
	}
	for _, l := range i.Links {
		pb.Links = append(pb.Links, &agentv1.LinearIssueLink{Title: l.Title, Url: l.URL, Source: l.Source})
	}
	return pb
}
