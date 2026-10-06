package api

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
)

// UserService implements podium.v1.UserService.
type UserService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewUserService returns the users API.
func NewUserService(st *store.Store, logger *slog.Logger) *UserService {
	if logger == nil {
		logger = slog.Default()
	}
	return &UserService{store: st, logger: logger}
}

// ListUsers returns every recorded login, most recently seen first.
func (s *UserService) ListUsers(
	ctx context.Context,
	_ *connect.Request[podiumv1.ListUsersRequest],
) (*connect.Response[podiumv1.ListUsersResponse], error) {
	rows, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*podiumv1.User, 0, len(rows))
	for _, u := range rows {
		out = append(out, userToProto(u))
	}
	return connect.NewResponse(&podiumv1.ListUsersResponse{Users: out}), nil
}

// SetUserRole replaces one login's role. The last owner cannot be demoted.
func (s *UserService) SetUserRole(
	ctx context.Context,
	req *connect.Request[podiumv1.SetUserRoleRequest],
) (*connect.Response[podiumv1.SetUserRoleResponse], error) {
	loginName := req.Msg.GetLogin()
	role := req.Msg.GetRole()
	before, err := s.store.GetUser(ctx, loginName)
	if err != nil {
		return nil, storeError(err)
	}
	user, err := s.store.AssignRole(ctx, loginName, role)
	if err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "user role changed",
		"login", user.Login, "role", role, "was", before.Roles, "by", login(ctx))
	if err := s.store.Audit(ctx, login(ctx), store.ActionUserRoleSet, user.Login, map[string]any{
		"role": role,
		"was":  before.Roles,
	}); err != nil {
		s.logger.WarnContext(ctx, "audit user.role.set failed", "login", user.Login, "error", err)
	}
	return connect.NewResponse(&podiumv1.SetUserRoleResponse{User: userToProto(user)}), nil
}

func userToProto(u store.User) *podiumv1.User {
	return &podiumv1.User{
		Login:        u.Login,
		DisplayName:  u.DisplayName,
		Roles:        u.Roles,
		HostedDomain: u.HostedDomain,
		PictureUrl:   u.PictureURL,
		LastSeenAt:   timestamppb.New(u.LastSeenAt),
	}
}
