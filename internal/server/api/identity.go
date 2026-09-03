package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/transport"
	"github.com/alvaroibarguen/podium/internal/version"
)

// IdentityService implements podium.v1.IdentityService. It reads nothing and writes nothing: the
// whole answer is the Identity the transport already put in the context.
type IdentityService struct{}

// NewIdentityService returns the identity handler.
func NewIdentityService() *IdentityService { return &IdentityService{} }

// WhoAmI reports who the transport says the caller is.
//
// The call is behind the same identity middleware as everything else, and that is the point: a
// client that gets an answer without presenting a credential knows it is on a tailnet and needs
// no login step, and a client that gets 401 knows it must supply the dev token. The web UI uses
// exactly that to decide whether to prompt.
func (s *IdentityService) WhoAmI(
	ctx context.Context,
	_ *connect.Request[podiumv1.WhoAmIRequest],
) (*connect.Response[podiumv1.WhoAmIResponse], error) {
	id, ok := transport.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("whoami: no identity"))
	}
	return connect.NewResponse(&podiumv1.WhoAmIResponse{
		Login:       id.Login,
		DisplayName: id.DisplayName,
		Kind:        identityKind(id.Kind),
		Tags:        id.NodeTags,
		// The build identity of the process answering. `podium version` reads it to warn
		// about a CLI and a control plane that are not the same release.
		ServerVersion: version.Version,
		ServerCommit:  version.Commit,
	}), nil
}

func identityKind(k transport.IdentityKind) podiumv1.IdentityKind {
	switch k {
	case transport.KindUser:
		return podiumv1.IdentityKind_IDENTITY_KIND_USER
	case transport.KindNode:
		return podiumv1.IdentityKind_IDENTITY_KIND_NODE
	case transport.KindDevToken:
		return podiumv1.IdentityKind_IDENTITY_KIND_DEV_TOKEN
	default:
		return podiumv1.IdentityKind_IDENTITY_KIND_UNSPECIFIED
	}
}
