package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
	"github.com/podium-ade/podium/internal/version"
)

// IdentityService implements podium.v1.IdentityService. WhoAmI is still mostly the Identity
// the transport put in the context; when Google Workspace sign-in is on, it also reports
// the instance claim so the web UI can show the claim screen instead of the app.
type IdentityService struct {
	store         *store.Store
	agentEnabled  bool
	googleEnabled bool
}

// NewIdentityService returns the identity handler. agentEnabled is cfg.AgentEnabled(): whether
// this server proxies the conductor's API, which is how the web UI knows to show its Agent
// screen at all. googleEnabled is whether PODIUM_GOOGLE_OAUTH_CLIENT_ID is set.
func NewIdentityService(st *store.Store, agentEnabled, googleEnabled bool) *IdentityService {
	return &IdentityService{store: st, agentEnabled: agentEnabled, googleEnabled: googleEnabled}
}

// WhoAmI reports who the transport says the caller is.
//
// The call is behind the same identity middleware as everything else, and that is the point: a
// client that gets an answer without presenting a credential knows it is on a tailnet and needs
// no login step, and a client that gets 401 knows it must supply the dev token. The web UI uses
// exactly that to decide whether to prompt. When Google sign-in is configured, can_claim tells
// the UI to show the claim screen instead of the rest of the app.
func (s *IdentityService) WhoAmI(
	ctx context.Context,
	_ *connect.Request[podiumv1.WhoAmIRequest],
) (*connect.Response[podiumv1.WhoAmIResponse], error) {
	id, ok := transport.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("whoami: no identity"))
	}
	out := &podiumv1.WhoAmIResponse{
		Login:       id.Login,
		DisplayName: id.DisplayName,
		Kind:        identityKind(id.Kind),
		Tags:        id.NodeTags,
		// The build identity of the process answering. `podium version` reads it to warn
		// about a CLI and a control plane that are not the same release.
		ServerVersion:     version.Version,
		ServerCommit:      version.Commit,
		AgentEnabled:      s.agentEnabled,
		GoogleAuthEnabled: s.googleEnabled,
	}
	if s.store != nil {
		if inst, err := s.store.GetInstance(ctx); err == nil {
			out.Claimed = true
			out.HostedDomain = inst.HostedDomain
		}
		if id.Kind == transport.KindUser {
			if user, err := s.store.GetUser(ctx, id.Login); err == nil {
				out.Roles = user.Roles
				out.ClaimDomain = store.UserDomain(user)
				out.PictureUrl = user.PictureURL
				if out.DisplayName == "" {
					out.DisplayName = user.DisplayName
				}
			} else {
				out.ClaimDomain = store.EmailDomain(id.Login)
			}
			out.CanClaim = s.googleEnabled && out.ClaimDomain != "" && !out.Claimed
		}
	}
	return connect.NewResponse(out), nil
}

// Claim binds an unclaimed instance to the caller's domain. The typed hosted_domain must
// match; a second claim from anyone else is FailedPrecondition.
func (s *IdentityService) Claim(
	ctx context.Context,
	req *connect.Request[podiumv1.ClaimRequest],
) (*connect.Response[podiumv1.ClaimResponse], error) {
	if !s.googleEnabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("claim: Google Workspace sign-in is not configured on this control plane"))
	}
	id, ok := transport.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("claim: no identity"))
	}
	if id.Kind != transport.KindUser {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("claim: only a signed-in user can claim this instance"))
	}
	if s.store == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("claim: store is not configured"))
	}
	domain := strings.ToLower(strings.TrimSpace(req.Msg.GetHostedDomain()))
	if domain == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("claim: hosted_domain is required"))
	}
	inst, err := s.store.ClaimInstance(ctx, id.Login, domain)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadyClaimed):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		case errors.Is(err, store.ErrDomainMismatch):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		case errors.Is(err, store.ErrNotFound):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		default:
			return nil, storeError(err)
		}
	}
	return connect.NewResponse(&podiumv1.ClaimResponse{
		HostedDomain: inst.HostedDomain,
		ClaimedBy:    inst.ClaimedBy,
	}), nil
}

func identityKind(k transport.IdentityKind) podiumv1.IdentityKind {
	switch k {
	case transport.KindUser:
		return podiumv1.IdentityKind_IDENTITY_KIND_USER
	case transport.KindNode:
		return podiumv1.IdentityKind_IDENTITY_KIND_NODE
	case transport.KindLocalToken:
		return podiumv1.IdentityKind_IDENTITY_KIND_LOCAL_TOKEN
	default:
		return podiumv1.IdentityKind_IDENTITY_KIND_UNSPECIFIED
	}
}
