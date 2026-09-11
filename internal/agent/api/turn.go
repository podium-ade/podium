package api

// TurnService: what a turn running on this host may ask of the conductor.
//
// It is a service of its own because it is authenticated differently. Every other RPC in
// this package is guarded by RequireBearer — the operator's token, which can rewrite a
// profile, read every session and set a provider key. A turn holds nothing of the sort. It
// presents a token the conductor minted for it, scoped to one conversation and one menu of
// playbooks, and revoked the moment that turn ends.
//
// Nor is it proxied: podium-server mounts `/podium.agent.v1.AgentService/` and nothing else,
// so this surface is reachable only from the conductor's own host — which is exactly where a
// host turn runs.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// TurnTokenHeader carries a turn's token. A header and not a request field: it is a
// credential, and a credential in a message body is a credential in every log that ever
// prints a request.
const TurnTokenHeader = "X-Podium-Turn"

// Delegator is the conductor, as this service needs it. An interface rather than the
// concrete type so the handler can be tested without a turn loop, a database or a control
// plane behind it.
type Delegator interface {
	Delegate(ctx context.Context, token, playbook, instruction string) (store.Delegation, error)
	GetDelegation(ctx context.Context, token, id string) (store.Delegation, string, error)
	Delegations(ctx context.Context, token string) ([]store.Delegation, error)
	CancelDelegation(ctx context.Context, token, id, reason string) (store.Delegation, error)
	InjectDelegation(ctx context.Context, token, id, text string) (store.Delegation, error)
}

// TurnService implements podium.agent.v1.TurnService.
type TurnService struct {
	delegator Delegator
	logger    *slog.Logger
}

// NewTurnService returns the handler. A nil delegator is a conductor that runs no host
// turns: every call then answers FailedPrecondition, which is the truth rather than a crash.
func NewTurnService(delegator Delegator, logger *slog.Logger) *TurnService {
	if logger == nil {
		logger = slog.Default()
	}
	return &TurnService{delegator: delegator, logger: logger}
}

// Delegate starts a task for the calling turn's conversation.
func (s *TurnService) Delegate(
	ctx context.Context, req *connect.Request[agentv1.DelegateRequest],
) (*connect.Response[agentv1.DelegateResponse], error) {
	token, err := s.token(req.Header())
	if err != nil {
		return nil, err
	}
	playbook := strings.TrimSpace(req.Msg.GetPlaybook())
	instruction := strings.TrimSpace(req.Msg.GetInstruction())
	switch {
	case playbook == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("playbook is required"))
	case instruction == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
			"instruction is required: it is the whole brief the delegated task gets"))
	}
	dlg, err := s.delegator.Delegate(ctx, token, playbook, instruction)
	if err != nil {
		return nil, delegationError(err)
	}
	// The instruction is a model's words and can be long; the log carries the shape of what
	// happened, and the row carries the words.
	s.logger.InfoContext(ctx, "a turn delegated a task", "delegation_id", dlg.ID,
		"turn_id", dlg.TurnID, "playbook", dlg.Playbook, "task_id", dlg.TaskID)
	return connect.NewResponse(&agentv1.DelegateResponse{Delegation: delegationProto(dlg)}), nil
}

// GetDelegation is where a delegated task has got to. It is what a turn polls, so it is
// deliberately cheap: one row and the last thing the task said.
func (s *TurnService) GetDelegation(
	ctx context.Context, req *connect.Request[agentv1.GetDelegationRequest],
) (*connect.Response[agentv1.GetDelegationResponse], error) {
	token, err := s.token(req.Header())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	dlg, progress, err := s.delegator.GetDelegation(ctx, token, req.Msg.GetId())
	if err != nil {
		return nil, delegationError(err)
	}
	return connect.NewResponse(&agentv1.GetDelegationResponse{
		Delegation: delegationProto(dlg),
		Progress:   progress,
	}), nil
}

// ListDelegations is everything the calling turn has delegated.
func (s *TurnService) ListDelegations(
	ctx context.Context, req *connect.Request[agentv1.ListDelegationsRequest],
) (*connect.Response[agentv1.ListDelegationsResponse], error) {
	token, err := s.token(req.Header())
	if err != nil {
		return nil, err
	}
	all, err := s.delegator.Delegations(ctx, token)
	if err != nil {
		return nil, delegationError(err)
	}
	out := make([]*agentv1.Delegation, 0, len(all))
	for _, dlg := range all {
		out = append(out, delegationProto(dlg))
	}
	return connect.NewResponse(&agentv1.ListDelegationsResponse{Delegations: out}), nil
}

// CancelDelegation stops one.
func (s *TurnService) CancelDelegation(
	ctx context.Context, req *connect.Request[agentv1.CancelDelegationRequest],
) (*connect.Response[agentv1.CancelDelegationResponse], error) {
	token, err := s.token(req.Header())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	dlg, err := s.delegator.CancelDelegation(ctx, token, req.Msg.GetId(), strings.TrimSpace(req.Msg.GetReason()))
	if err != nil {
		return nil, delegationError(err)
	}
	s.logger.InfoContext(ctx, "a turn cancelled a delegated task",
		"delegation_id", dlg.ID, "turn_id", dlg.TurnID, "task_id", dlg.TaskID)
	return connect.NewResponse(&agentv1.CancelDelegationResponse{Delegation: delegationProto(dlg)}), nil
}

// InjectDelegation delivers one human message into a running delegated task.
func (s *TurnService) InjectDelegation(
	ctx context.Context,
	req *connect.Request[agentv1.InjectDelegationRequest],
) (*connect.Response[agentv1.InjectDelegationResponse], error) {
	token, err := s.token(req.Header())
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	text := strings.TrimSpace(req.Msg.GetText())
	switch {
	case id == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	case text == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("text is required"))
	}
	dlg, err := s.delegator.InjectDelegation(ctx, token, id, text)
	if err != nil {
		return nil, delegationError(err)
	}
	s.logger.InfoContext(ctx, "a turn injected into a delegated task",
		"delegation_id", dlg.ID, "turn_id", dlg.TurnID, "task_id", dlg.TaskID)
	return connect.NewResponse(&agentv1.InjectDelegationResponse{Delegation: delegationProto(dlg)}), nil
}

// token is the caller's turn token, or the reason there is nothing to do.
func (s *TurnService) token(h interface{ Get(string) string }) (string, error) {
	if s.delegator == nil {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"this conductor runs no host turns, so there is nothing to delegate from"))
	}
	token := strings.TrimSpace(h.Get(TurnTokenHeader))
	if token == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New(
			TurnTokenHeader+" is required: only a running turn may use this service"))
	}
	return token, nil
}

// delegationError maps the conductor's refusals onto Connect codes. The messages are the
// conductor's own: the caller is our own MCP server, and a model reading "that playbook was
// not offered to this turn" can correct itself, where "invalid argument" cannot.
func delegationError(err error) error {
	switch {
	case errors.Is(err, conductor.ErrNoTurn):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, conductor.ErrPlaybookNotOffered):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, conductor.ErrDelegationNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, conductor.ErrDelegationOver):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, conductor.ErrNoDelegation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func delegationProto(d store.Delegation) *agentv1.Delegation {
	out := &agentv1.Delegation{
		Id:          d.ID,
		Playbook:    d.Playbook,
		Instruction: d.Instruction,
		TaskId:      d.TaskID,
		Status:      d.Status,
		FinalText:   d.FinalText,
		CreatedAt:   timestamppb.New(d.CreatedAt),
	}
	if d.FinishedAt != nil {
		out.FinishedAt = timestamppb.New(*d.FinishedAt)
	}
	return out
}
