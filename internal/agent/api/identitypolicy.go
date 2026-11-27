package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// AuditIdentityPolicy is an admin changing who turns act as.
const AuditIdentityPolicy = "identity.policy"

// GetIdentityPolicy is who a turn acts as, for anyone signed in.
func (s *AgentService) GetIdentityPolicy(
	ctx context.Context, _ *connect.Request[agentv1.GetIdentityPolicyRequest],
) (*connect.Response[agentv1.GetIdentityPolicyResponse], error) {
	if _, err := requireLogin(ctx); err != nil {
		return nil, err
	}
	p, err := s.store.GetIdentityPolicy(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.GetIdentityPolicyResponse{Policy: identityPolicyToProto(p)}), nil
}

// SetIdentityPolicy sets who turns act as. Admins and owners only. Only the fields a request
// sets change; an empty one leaves its setting as it is. So an admin saving from a page loaded
// before somebody else's save changes what they changed, and does not put back the rest.
func (s *AgentService) SetIdentityPolicy(
	ctx context.Context, req *connect.Request[agentv1.SetIdentityPolicyRequest],
) (*connect.Response[agentv1.SetIdentityPolicyResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if !Infra(ctx) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only admins and owners set who turns act as"))
	}
	in := req.Msg.GetPolicy()
	p, err := s.store.UpdateIdentityPolicy(ctx, store.IdentityPolicy{
		Linear:          in.GetLinear(),
		GitHubWrite:     in.GetGithubWrite(),
		GitHubRead:      in.GetGithubRead(),
		ModelKey:        in.GetModelKey(),
		SharedModelKey:  in.GetSharedModelKey(),
		SlackOutsiders:  in.GetSlackOutsiders(),
		LinearOutsiders: in.GetLinearOutsiders(),
	})
	if err != nil {
		return nil, storeError(err)
	}
	s.auditSubject(ctx, login, AuditIdentityPolicy, "identity", map[string]any{
		"linear": p.Linear, "github_write": p.GitHubWrite, "github_read": p.GitHubRead,
		"model_key": p.ModelKey, "shared_model_key": p.SharedModelKey, "slack_outsiders": p.SlackOutsiders,
		"linear_outsiders": p.LinearOutsiders,
	})
	return connect.NewResponse(&agentv1.SetIdentityPolicyResponse{Policy: identityPolicyToProto(p)}), nil
}

func identityPolicyToProto(p store.IdentityPolicy) *agentv1.IdentityPolicy {
	return &agentv1.IdentityPolicy{
		Linear:          p.Linear,
		GithubWrite:     p.GitHubWrite,
		GithubRead:      p.GitHubRead,
		ModelKey:        p.ModelKey,
		SharedModelKey:  p.SharedModelKey,
		SlackOutsiders:  p.SlackOutsiders,
		LinearOutsiders: p.LinearOutsiders,
	}
}
