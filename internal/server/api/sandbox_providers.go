package api

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/nodes"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
)

// SandboxPusher re-sends a provider's configuration to its connected nodes, and says how
// many sandboxes they hold.
type SandboxPusher interface {
	PushSandboxConfig(ctx context.Context, provider string)
	SandboxUsage(ctx context.Context) map[string]nodes.SandboxUsage
}

// SandboxProviderService implements podium.v1.SandboxProviderService: the secret store's
// third table, under the registries' rules. A change reaches the provider's nodes at once.
type SandboxProviderService struct {
	secrets *secrets.Service
	nodes   SandboxPusher
	logger  *slog.Logger
}

// NewSandboxProviderService returns the sandbox provider API.
func NewSandboxProviderService(svc *secrets.Service, nodes SandboxPusher, logger *slog.Logger) *SandboxProviderService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SandboxProviderService{secrets: svc, nodes: nodes, logger: logger}
}

// SetSandboxProvider creates or replaces a provider. The request's secret is zeroed before
// the handler returns.
func (s *SandboxProviderService) SetSandboxProvider(
	ctx context.Context,
	req *connect.Request[podiumv1.SetSandboxProviderRequest],
) (*connect.Response[podiumv1.SetSandboxProviderResponse], error) {
	secret := req.Msg.GetSecret()
	defer secrets.Zero(secret)
	row, err := s.secrets.SetSandboxProvider(ctx, login(ctx), req.Msg.GetName(), req.Msg.GetSettings(),
		req.Msg.GetMaxConcurrent(), secret)
	if err != nil {
		return nil, secretError(err)
	}
	s.nodes.PushSandboxConfig(context.WithoutCancel(ctx), row.Name)
	return connect.NewResponse(&podiumv1.SetSandboxProviderResponse{
		Provider: sandboxProviderToProto(row, s.nodes.SandboxUsage(ctx)[row.Name]),
	}), nil
}

// ListSandboxProviders returns settings and metadata, never a credential.
func (s *SandboxProviderService) ListSandboxProviders(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListSandboxProvidersRequest],
) (*connect.Response[podiumv1.ListSandboxProvidersResponse], error) {
	rows, err := s.secrets.ListSandboxProviders(ctx)
	if err != nil {
		return nil, secretError(err)
	}
	usage := s.nodes.SandboxUsage(ctx)
	out := make([]*podiumv1.SandboxProvider, 0, len(rows))
	for _, row := range rows {
		out = append(out, sandboxProviderToProto(row, usage[row.Name]))
	}
	return connect.NewResponse(&podiumv1.ListSandboxProvidersResponse{Providers: out}), nil
}

// DeleteSandboxProvider removes a provider; its nodes stop taking work.
func (s *SandboxProviderService) DeleteSandboxProvider(
	ctx context.Context,
	req *connect.Request[podiumv1.DeleteSandboxProviderRequest],
) (*connect.Response[podiumv1.DeleteSandboxProviderResponse], error) {
	if err := s.secrets.DeleteSandboxProvider(ctx, login(ctx), req.Msg.GetName()); err != nil {
		return nil, secretError(err)
	}
	s.nodes.PushSandboxConfig(context.WithoutCancel(ctx), req.Msg.GetName())
	return connect.NewResponse(&podiumv1.DeleteSandboxProviderResponse{}), nil
}

func sandboxProviderToProto(row store.SandboxProvider, u nodes.SandboxUsage) *podiumv1.SandboxProvider {
	return &podiumv1.SandboxProvider{
		Name:          row.Name,
		Settings:      row.Settings,
		HasSecret:     row.KeyID != "",
		KeyId:         row.KeyID,
		CreatedBy:     row.CreatedBy,
		UpdatedAt:     timestamppb.New(row.UpdatedAt),
		MaxConcurrent: row.MaxConcurrent,
		AccountLimit:  u.AccountLimit,
		InUse:         u.InUse,
	}
}
