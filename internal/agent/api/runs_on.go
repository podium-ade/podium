package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

// GetRunsOn reports where a turn runs when its playbook does not say. Never saved is the
// fleet's own machines.
func (s *AgentService) GetRunsOn(
	ctx context.Context, _ *connect.Request[agentv1.GetRunsOnRequest],
) (*connect.Response[agentv1.GetRunsOnResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this conductor has no database"))
	}
	var row profiles.RunsOn
	if err := s.store.GetSetting(ctx, profiles.RunsOnKey, &row); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.GetRunsOnResponse{RunsOn: runsOnToProto(row)}), nil
}

// SetRunsOn saves where a turn runs when its playbook does not say. A turn already running
// stays where it is.
func (s *AgentService) SetRunsOn(
	ctx context.Context, req *connect.Request[agentv1.SetRunsOnRequest],
) (*connect.Response[agentv1.SetRunsOnResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this conductor has no database"))
	}
	v := strings.TrimSpace(req.Msg.GetRunsOn())
	if v == "" {
		v = profiles.RunsOnNodes
	}
	if err := profiles.ValidateRunsOn(v); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	row := profiles.RunsOn{RunsOn: v, UpdatedBy: Login(ctx), UpdatedAt: time.Now().UTC()}
	if err := s.store.PutSetting(ctx, profiles.RunsOnKey, row); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "where turns run changed", "login", row.UpdatedBy, "runs_on", v)
	return connect.NewResponse(&agentv1.SetRunsOnResponse{RunsOn: runsOnToProto(row)}), nil
}

func runsOnToProto(r profiles.RunsOn) *agentv1.RunsOn {
	out := &agentv1.RunsOn{RunsOn: r.RunsOn, UpdatedBy: r.UpdatedBy}
	if out.RunsOn == "" {
		out.RunsOn = profiles.RunsOnNodes
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(r.UpdatedAt)
	}
	return out
}

func playbookSandboxFromProto(in *agentv1.PlaybookSandbox) *profiles.PlaybookSandbox {
	if in == nil {
		return nil
	}
	out := &profiles.PlaybookSandbox{}
	if b := in.GetBoat(); b != nil && (b.GetType() != "" || b.GetTemplate() != "" || b.GetOrg() != "") {
		out.Boat = &spec.BoatSandbox{
			Type: strings.TrimSpace(b.GetType()), Template: strings.TrimSpace(b.GetTemplate()), Org: strings.TrimSpace(b.GetOrg()),
		}
	}
	if m := in.GetModal(); m != nil && (m.GetRuntime() != "" || m.GetEnvironment() != "") {
		out.Modal = &spec.ModalSandbox{Runtime: strings.TrimSpace(m.GetRuntime()), Environment: strings.TrimSpace(m.GetEnvironment())}
	}
	if out.Boat == nil && out.Modal == nil {
		return nil
	}
	return out
}

func playbookSandboxToProto(in *profiles.PlaybookSandbox) *agentv1.PlaybookSandbox {
	if in == nil {
		return nil
	}
	out := &agentv1.PlaybookSandbox{}
	if b := in.Boat; b != nil {
		out.Boat = &agentv1.PlaybookBoat{Type: b.Type, Template: b.Template, Org: b.Org}
	}
	if m := in.Modal; m != nil {
		out.Modal = &agentv1.PlaybookModal{Runtime: m.Runtime, Environment: m.Environment}
	}
	return out
}

// GetAssistantRunsOn reports where the assistant runs: the saved choice, the conductor's
// default, and what the next turn will actually use.
func (s *AgentService) GetAssistantRunsOn(
	ctx context.Context, _ *connect.Request[agentv1.GetAssistantRunsOnRequest],
) (*connect.Response[agentv1.GetAssistantRunsOnResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this conductor has no database"))
	}
	placement, err := s.assistantPlacement(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetAssistantRunsOnResponse{Placement: placement}), nil
}

// SetAssistantRunsOn saves where the assistant runs. Empty clears the choice back to the
// conductor's default. A turn already running stays where it is.
//
// "host" on a conductor with no host runtime is refused here rather than saved and ignored:
// the conductor would fall back to its default, and the screen would say one thing while
// conversations did another.
func (s *AgentService) SetAssistantRunsOn(
	ctx context.Context, req *connect.Request[agentv1.SetAssistantRunsOnRequest],
) (*connect.Response[agentv1.SetAssistantRunsOnResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this conductor has no database"))
	}
	v := strings.TrimSpace(req.Msg.GetRunsOn())
	if err := profiles.ValidateAssistantRunsOn(v); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if v == profiles.RunsOnHost && s.assistant != nil && !s.assistant.HostAvailable() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this conductor has no host "+
			"runtime (PODIUM_AGENT_HOST_RUNTIME), so the assistant cannot run on its host"))
	}
	row := profiles.RunsOn{RunsOn: v, UpdatedBy: Login(ctx), UpdatedAt: time.Now().UTC()}
	if err := s.store.PutSetting(ctx, profiles.AssistantRunsOnKey, row); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "where the assistant runs changed", "login", row.UpdatedBy, "runs_on", v)
	placement, err := s.assistantPlacement(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetAssistantRunsOnResponse{Placement: placement}), nil
}

func (s *AgentService) assistantPlacement(ctx context.Context) (*agentv1.AssistantPlacement, error) {
	var row profiles.RunsOn
	if err := s.store.GetSetting(ctx, profiles.AssistantRunsOnKey, &row); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &agentv1.AssistantPlacement{Effective: row.RunsOn}
	if row.RunsOn != "" {
		out.Saved = &agentv1.RunsOn{RunsOn: row.RunsOn, UpdatedBy: row.UpdatedBy}
		if !row.UpdatedAt.IsZero() {
			out.Saved.UpdatedAt = timestamppb.New(row.UpdatedAt)
		}
	}
	if s.assistant != nil {
		out.Effective = s.assistant.AssistantPlacement(ctx)
		out.Default = s.assistant.AssistantDefault()
		out.HostAvailable = s.assistant.HostAvailable()
	}
	return out, nil
}
