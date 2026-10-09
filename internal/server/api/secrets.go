package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
	"github.com/podium-ade/podium/pkg/spec"
)

// userLookup is the user row a global write checks. *store.Store satisfies it.
type userLookup interface {
	GetUser(ctx context.Context, login string) (store.User, error)
}

// SecretService implements podium.v1.SecretService.
//
// There is deliberately no read endpoint. A value goes in through SetSecret and only ever
// comes back out inside an Assign, on its way to the node about to run the task that
// referenced it. A list returns names and metadata: every global secret, plus the caller's
// own personal secrets, and never another login's.
type SecretService struct {
	secrets *secrets.Service
	users   userLookup
	logger  *slog.Logger
}

// NewSecretService returns the secret API. users may be nil, in which case a signed-in
// person cannot write a global secret: the role cannot be checked, so the write is refused.
func NewSecretService(svc *secrets.Service, users userLookup, logger *slog.Logger) *SecretService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SecretService{secrets: svc, users: users, logger: logger}
}

// SetSecret creates or replaces a secret and returns its metadata. The request's value is
// zeroed before the handler returns. An unspecified scope is global.
func (s *SecretService) SetSecret(
	ctx context.Context,
	req *connect.Request[podiumv1.SetSecretRequest],
) (*connect.Response[podiumv1.SetSecretResponse], error) {
	name := req.Msg.GetName()
	value := req.Msg.GetValue()
	defer secrets.Zero(value)

	scope, owner, err := s.authorizeWrite(ctx, req.Msg.GetScope(), name, req.Msg.GetOwner())
	if err != nil {
		return nil, secretError(err)
	}
	row, err := s.secrets.SetScoped(ctx, login(ctx), scope, owner, name, value)
	if err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.SetSecretResponse{Secret: secretToProto(row)}), nil
}

// ListSecrets returns metadata only: every global name plus the caller's own personal
// names. A dev token and a node have no personal secrets, so they see globals. The
// conductor sees globals, and, when it names a login, that login's MCP credentials.
func (s *SecretService) ListSecrets(
	ctx context.Context,
	req *connect.Request[podiumv1.ListSecretsRequest],
) (*connect.Response[podiumv1.ListSecretsResponse], error) {
	owner, agentMCPOnly, err := s.listOwner(ctx, req.Msg.GetOwner())
	if err != nil {
		return nil, secretError(err)
	}
	rows, err := s.secrets.ListVisible(ctx, owner)
	if err != nil {
		return nil, secretError(err)
	}
	out := make([]*podiumv1.Secret, 0, len(rows))
	for _, row := range rows {
		if agentMCPOnly && row.Scope == store.SecretScopePersonal && !spec.IsPersonalMCPSecret(row.Name) {
			continue
		}
		out = append(out, secretToProto(row))
	}
	return connect.NewResponse(&podiumv1.ListSecretsResponse{Secrets: out}), nil
}

// DeleteSecret removes a secret the caller is allowed to write. Tasks already assigned
// keep the copy inside their Assign; the next task that references the name fails to resolve.
func (s *SecretService) DeleteSecret(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteSecretRequest],
) (*connect.Response[podiumv1.DeleteSecretResponse], error) {
	scope, owner, err := s.authorizeWrite(ctx, req.Msg.GetScope(), req.Msg.GetName(), req.Msg.GetOwner())
	if err != nil {
		return nil, secretError(err)
	}
	if err := s.secrets.DeleteScoped(ctx, login(ctx), scope, owner, req.Msg.GetName()); err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.DeleteSecretResponse{}), nil
}

// authorizeWrite decides the scope and owner a caller may write. A person's own personal
// secret is always theirs: the request cannot name somebody else. The conductor may write
// one personal name for somebody else, and only an MCP credential, because that is the
// token a person saved on their own MCP server.
func (s *SecretService) authorizeWrite(ctx context.Context, requested podiumv1.SecretScope, name, requestedOwner string) (scope, owner string, err error) {
	id, ok := transport.From(ctx)
	if !ok {
		return "", "", fmt.Errorf("%w: unauthenticated", secrets.ErrForbidden)
	}
	requestedOwner = strings.TrimSpace(requestedOwner)
	switch requested {
	case podiumv1.SecretScope_SECRET_SCOPE_UNSPECIFIED, podiumv1.SecretScope_SECRET_SCOPE_GLOBAL:
		if requestedOwner != "" {
			return "", "", fmt.Errorf("%w: a global secret has no owner", secrets.ErrInvalidSecret)
		}
		switch id.Kind {
		case transport.KindLocalToken, transport.KindAgent:
			return store.SecretScopeGlobal, "", nil
		case transport.KindUser:
			if s.users == nil {
				return "", "", fmt.Errorf("%w: only an admin can set a global secret", secrets.ErrForbidden)
			}
			user, uerr := s.users.GetUser(ctx, id.Login)
			if uerr != nil || !store.HasRole(user.Roles, store.RoleAdmin) {
				return "", "", fmt.Errorf("%w: only an admin can set a global secret", secrets.ErrForbidden)
			}
			return store.SecretScopeGlobal, "", nil
		default:
			return "", "", fmt.Errorf("%w: only an admin can set a global secret", secrets.ErrForbidden)
		}
	case podiumv1.SecretScope_SECRET_SCOPE_PERSONAL:
		switch id.Kind {
		case transport.KindUser:
			if id.Login == "" {
				return "", "", fmt.Errorf("%w: only the owning person can set a personal secret", secrets.ErrForbidden)
			}
			if requestedOwner != "" && requestedOwner != id.Login {
				return "", "", fmt.Errorf("%w: a personal secret belongs to the caller", secrets.ErrForbidden)
			}
			return store.SecretScopePersonal, id.Login, nil
		case transport.KindAgent:
			if requestedOwner == "" || !spec.IsPersonalMCPSecret(name) {
				return "", "", fmt.Errorf("%w: only the owning person can set a personal secret", secrets.ErrForbidden)
			}
			return store.SecretScopePersonal, requestedOwner, nil
		default:
			return "", "", fmt.Errorf("%w: only the owning person can set a personal secret", secrets.ErrForbidden)
		}
	default:
		return "", "", fmt.Errorf("%w: unknown secret scope", secrets.ErrInvalidSecret)
	}
}

// listOwner is whose personal secrets a list may include. agentMCPOnly means the caller
// is the conductor naming a login, and the list must keep that login's other secrets out.
func (s *SecretService) listOwner(ctx context.Context, requested string) (owner string, agentMCPOnly bool, err error) {
	id, ok := transport.From(ctx)
	if !ok {
		return "", false, fmt.Errorf("%w: unauthenticated", secrets.ErrForbidden)
	}
	requested = strings.TrimSpace(requested)
	switch id.Kind {
	case transport.KindUser:
		if requested != "" && requested != id.Login {
			return "", false, fmt.Errorf("%w: a list shows only your own secrets", secrets.ErrForbidden)
		}
		return id.Login, false, nil
	case transport.KindAgent:
		if requested == "" {
			return "", false, nil
		}
		return requested, true, nil
	default:
		if requested != "" {
			return "", false, fmt.Errorf("%w: a list shows only your own secrets", secrets.ErrForbidden)
		}
		return "", false, nil
	}
}

func secretToProto(row store.Secret) *podiumv1.Secret {
	scope := podiumv1.SecretScope_SECRET_SCOPE_GLOBAL
	if row.Scope == store.SecretScopePersonal {
		scope = podiumv1.SecretScope_SECRET_SCOPE_PERSONAL
	}
	return &podiumv1.Secret{
		Name:      row.Name,
		Version:   row.Version,
		KeyId:     row.KeyID,
		CreatedBy: row.CreatedBy,
		UpdatedAt: timestamppb.New(row.UpdatedAt),
		Scope:     scope,
		Owner:     row.Owner,
	}
}

// secretError maps the secrets package's own sentinels onto Connect codes and falls back
// to the store mapping. A server with no master key is a precondition failure, not an
// internal error: nothing is broken, it is simply not configured for secrets.
func secretError(err error) error {
	switch {
	case errors.Is(err, secrets.ErrNoKey):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, secrets.ErrMissing):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, secrets.ErrDecrypt):
		return connect.NewError(connect.CodeInternal, err)
	case errors.Is(err, secrets.ErrInvalidSecret), errors.Is(err, secrets.ErrReservedName):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, secrets.ErrNameTaken):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, secrets.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		return storeError(err)
	}
}
