package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
)

// OrganizationService implements podium.v1.OrganizationService: the deployment's name and
// the organization it serves. The RBAC catalog makes the write an admin's.
type OrganizationService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewOrganizationService returns the handler.
func NewOrganizationService(st *store.Store, logger *slog.Logger) *OrganizationService {
	return &OrganizationService{store: st, logger: logger}
}

// GetOrganization returns the names as saved; never set is both empty.
func (s *OrganizationService) GetOrganization(
	ctx context.Context, _ *connect.Request[podiumv1.GetOrganizationRequest],
) (*connect.Response[podiumv1.GetOrganizationResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this control plane has no database"))
	}
	org, err := s.store.GetOrganization(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&podiumv1.GetOrganizationResponse{Organization: organizationToProto(org)}), nil
}

// UpdateOrganization replaces both names.
func (s *OrganizationService) UpdateOrganization(
	ctx context.Context, req *connect.Request[podiumv1.UpdateOrganizationRequest],
) (*connect.Response[podiumv1.UpdateOrganizationResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this control plane has no database"))
	}
	org, err := s.store.SetOrganization(ctx, store.Organization{
		OrganizationName: req.Msg.GetOrganizationName(),
		DeploymentName:   req.Msg.GetDeploymentName(),
		UpdatedBy:        login(ctx),
	})
	if errors.Is(err, store.ErrInvalidArgument) {
		// The sentence after the sentinel is the one a person can act on.
		msg := strings.TrimPrefix(err.Error(), store.ErrInvalidArgument.Error()+": ")
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "organization names changed", "login", org.UpdatedBy,
		"organization", org.OrganizationName, "deployment", org.DeploymentName)
	return connect.NewResponse(&podiumv1.UpdateOrganizationResponse{Organization: organizationToProto(org)}), nil
}

func organizationToProto(o store.Organization) *podiumv1.Organization {
	out := &podiumv1.Organization{
		OrganizationName: o.OrganizationName,
		DeploymentName:   o.DeploymentName,
		UpdatedBy:        o.UpdatedBy,
	}
	if !o.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(o.UpdatedAt)
	}
	return out
}
