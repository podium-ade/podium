package api

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
)

// RegistryService implements podium.v1.RegistryService. It is the secret store's second
// table under the same rules: no read endpoint, any authenticated caller, actor recorded.
type RegistryService struct {
	secrets *secrets.Service
	logger  *slog.Logger
}

// NewRegistryService returns the registry API.
func NewRegistryService(svc *secrets.Service, logger *slog.Logger) *RegistryService {
	if logger == nil {
		logger = slog.Default()
	}
	return &RegistryService{secrets: svc, logger: logger}
}

// SetRegistry creates or replaces the login for one registry host. The request's password
// is zeroed before the handler returns.
func (s *RegistryService) SetRegistry(
	ctx context.Context,
	req *connect.Request[podiumv1.SetRegistryRequest],
) (*connect.Response[podiumv1.SetRegistryResponse], error) {
	password := req.Msg.GetPassword()
	defer secrets.Zero(password)

	row, err := s.secrets.SetRegistry(ctx, login(ctx), req.Msg.GetHost(), req.Msg.GetUsername(), password)
	if err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.SetRegistryResponse{Registry: registryToProto(row)}), nil
}

// ListRegistries returns metadata only: hosts, usernames, key ids and who set them.
func (s *RegistryService) ListRegistries(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListRegistriesRequest],
) (*connect.Response[podiumv1.ListRegistriesResponse], error) {
	rows, err := s.secrets.ListRegistries(ctx)
	if err != nil {
		return nil, secretError(err)
	}
	out := make([]*podiumv1.Registry, 0, len(rows))
	for _, row := range rows {
		out = append(out, registryToProto(row))
	}
	return connect.NewResponse(&podiumv1.ListRegistriesResponse{Registries: out}), nil
}

// DeleteRegistry removes a registry login. The next pull from that host is anonymous.
func (s *RegistryService) DeleteRegistry(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteRegistryRequest],
) (*connect.Response[podiumv1.DeleteRegistryResponse], error) {
	if err := s.secrets.DeleteRegistry(ctx, login(ctx), req.Msg.GetHost()); err != nil {
		return nil, secretError(err)
	}
	return connect.NewResponse(&podiumv1.DeleteRegistryResponse{}), nil
}

func registryToProto(row store.Registry) *podiumv1.Registry {
	return &podiumv1.Registry{
		Host:      row.Host,
		Username:  row.Username,
		KeyId:     row.KeyID,
		CreatedBy: row.CreatedBy,
		UpdatedAt: timestamppb.New(row.UpdatedAt),
	}
}
