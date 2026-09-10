package api

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
)

// SecretService implements podium.v1.SecretService.
//
// There is deliberately no read endpoint. A value goes in through SetSecret and only ever
// comes back out inside an Assign, on its way to the node about to run the task that
// referenced it. Authorization is "any authenticated caller" in this slice; the actor is
// recorded on the row and in the audit log so RBAC can be layered on later.
type SecretService struct {
	secrets *secrets.Service
	logger  *slog.Logger
}

// NewSecretService returns the secret API.
func NewSecretService(svc *secrets.Service, logger *slog.Logger) *SecretService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SecretService{secrets: svc, logger: logger}
}

// SetSecret creates or replaces a secret and returns its metadata. The request's value is
// zeroed before the handler returns.
func (s *SecretService) SetSecret(
	ctx context.Context,
	req *connect.Request[podiumv1.SetSecretRequest],
) (*connect.Response[podiumv1.SetSecretResponse], error) {
	name := req.Msg.GetName()
	value := req.Msg.GetValue()
	defer secrets.Zero(value)

	row, err := s.secrets.Set(ctx, login(ctx), name, value)
	if err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.SetSecretResponse{Secret: secretToProto(row)}), nil
}

// ListSecrets returns metadata only: names, versions, key ids and who set them.
func (s *SecretService) ListSecrets(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListSecretsRequest],
) (*connect.Response[podiumv1.ListSecretsResponse], error) {
	rows, err := s.secrets.List(ctx)
	if err != nil {
		return nil, secretError(err)
	}
	out := make([]*podiumv1.Secret, 0, len(rows))
	for _, row := range rows {
		out = append(out, secretToProto(row))
	}
	return connect.NewResponse(&podiumv1.ListSecretsResponse{Secrets: out}), nil
}

// DeleteSecret removes a secret. Tasks already assigned keep the copy inside their
// Assign; the next task that references the name fails to resolve.
func (s *SecretService) DeleteSecret(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteSecretRequest],
) (*connect.Response[podiumv1.DeleteSecretResponse], error) {
	if err := s.secrets.Delete(ctx, login(ctx), req.Msg.GetName()); err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.DeleteSecretResponse{}), nil
}

func secretToProto(row store.Secret) *podiumv1.Secret {
	return &podiumv1.Secret{
		Name:      row.Name,
		Version:   row.Version,
		KeyId:     row.KeyID,
		CreatedBy: row.CreatedBy,
		UpdatedAt: timestamppb.New(row.UpdatedAt),
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
	case errors.Is(err, secrets.ErrInvalidSecret):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return storeError(err)
	}
}
