package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// ListLinearConversations is the Linear view (docs/agent.md, "Linear"): every Linear issue's or
// project's conversation the caller may read, one row each, most recently active first.
//
// An admin or owner (Infra) and the dev token read every one. Anybody else reads those whose
// issue they touched through their linked Linear account, that they spoke in, own, or were
// shared — store.ListLinearConversations applies it in the query, so a page is never short of
// rows the caller may not see. linear_state_type narrows it to one column of the board, and the
// first page of any filter says how many rows it matches. Reading is all an admin gets by being one: writing in a
// conversation is still a participant's.
func (s *AgentService) ListLinearConversations(
	ctx context.Context, req *connect.Request[agentv1.ListLinearConversationsRequest],
) (*connect.Response[agentv1.ListLinearConversationsResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	stateType, ok := store.LinearStateTypeFilter(req.Msg.GetLinearStateType())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"linear_state_type %q is not one of %s or %q",
			req.Msg.GetLinearStateType(), strings.Join(store.LinearStateTypes, ", "), store.LinearStateTypeNone))
	}
	seeAll := Infra(ctx)
	page := req.Msg.GetPage()
	got, err := s.store.ListLinearConversations(ctx, login, store.LinearConversationFilter{
		SeeAll:    seeAll,
		Mine:      req.Msg.GetMine(),
		Team:      req.Msg.GetTeam(),
		State:     req.Msg.GetState(),
		StateType: stateType,
	}, int(page.GetLimit()), page.GetCursor())
	if err != nil {
		return nil, storeError(err)
	}
	rows := got.Rows
	teams, err := s.store.LinearConversationTeams(ctx, login, seeAll)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.LinearConversation, 0, len(rows))
	for _, r := range rows {
		role := r.MyRole
		switch {
		case SeesAll(ctx):
			role = store.RoleOwner
		case role == "" && seeAll:
			role = store.RoleViewer
		}
		out = append(out, &agentv1.LinearConversation{
			ChatId:          r.ChatID,
			Kind:            r.Kind,
			Identifier:      r.Identifier,
			Title:           r.Title,
			Url:             r.URL,
			TeamKey:         r.TeamKey,
			LinearState:     r.StateName,
			LinearStateType: r.StateType,
			State:           r.State,
			AskedOf:         r.AskedOf,
			LastActivityAt:  timestamppb.New(r.LastActivityAt),
			LastText:        r.LastText,
			CostUsd:         r.CostUSD,
			MyRole:          role,
			Mine:            r.Mine,
		})
	}
	return connect.NewResponse(&agentv1.ListLinearConversationsResponse{
		Conversations: out, NextCursor: got.Next, Teams: teams, Total: int32(got.Total),
	}), nil
}
