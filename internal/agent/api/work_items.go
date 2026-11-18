package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/work"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// errMergeProviders is a request for every provider while there are several: their pages are
// each keyed by their own cursor, and merging them into one is not built yet.
var errMergeProviders = errors.New("listing every provider at once is not supported with more than one; name a provider")

// ListWorkItems is the Issues view (docs/agent.md, "Issues"): every issue's or project's
// conversation the caller may read, from each issue tracker Podium lists (a work.Source per
// provider; today Linear), one row each, most recently active first.
//
// An admin or owner (Infra) and the dev token read every one. Anybody else reads those whose
// issue they touched through their linked account, that they spoke in, own, or were shared —
// the source applies it in its query, so a page is never short of rows the caller may not see.
// status narrows it to one column of the board, and the first page of any filter says how many
// rows it matches. Reading is all an admin gets by being one: writing in a conversation is still
// a participant's.
//
// provider "" is every provider. While there is one, that is it; with several, the request has
// to name one (errMergeProviders), since merging their pages is not built.
func (s *AgentService) ListWorkItems(
	ctx context.Context, req *connect.Request[agentv1.ListWorkItemsRequest],
) (*connect.Response[agentv1.ListWorkItemsResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	status, ok := work.ParseStatusFilter(req.Msg.GetStatus())
	if !ok {
		names := make([]string, 0, len(work.Statuses))
		for _, st := range work.Statuses {
			names = append(names, string(st))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"status %q is not one of %s or %q", req.Msg.GetStatus(), strings.Join(names, ", "), work.StatusNone))
	}
	providers := make([]string, 0, len(s.workSources))
	for _, src := range s.workSources {
		providers = append(providers, src.Provider())
	}
	src, err := s.workSource(req.Msg.GetProvider())
	if err != nil {
		return nil, err
	}
	if src == nil {
		return connect.NewResponse(&agentv1.ListWorkItemsResponse{Providers: providers}), nil
	}

	seeAll := Infra(ctx)
	page := req.Msg.GetPage()
	got, err := src.List(ctx, login, work.Filter{
		SeeAll: seeAll,
		Mine:   req.Msg.GetMine(),
		Team:   req.Msg.GetTeam(),
		State:  req.Msg.GetState(),
		Status: status,
		Limit:  int(page.GetLimit()),
		Cursor: page.GetCursor(),
	})
	if err != nil {
		return nil, storeError(err)
	}
	items := make([]*agentv1.WorkItem, 0, len(got.Items))
	for _, it := range got.Items {
		role := it.MyRole
		switch {
		case SeesAll(ctx):
			role = store.RoleOwner
		case role == "" && seeAll:
			role = store.RoleViewer
		}
		items = append(items, &agentv1.WorkItem{
			ChatId:         it.ChatID,
			Provider:       it.Provider,
			Kind:           it.Kind,
			Key:            it.Key,
			Title:          it.Title,
			Url:            it.URL,
			Team:           it.Team,
			Status:         string(it.Status),
			StatusLabel:    it.StatusLabel,
			State:          it.State,
			AskedOf:        it.AskedOf,
			LastActivityAt: timestamppb.New(it.LastActivityAt),
			LastText:       it.LastText,
			CostUsd:        it.CostUSD,
			MyRole:         role,
			Mine:           it.Mine,
		})
	}
	teams := make([]*agentv1.WorkTeam, 0, len(got.Teams))
	for _, t := range got.Teams {
		teams = append(teams, &agentv1.WorkTeam{Provider: src.Provider(), Key: t})
	}
	return connect.NewResponse(&agentv1.ListWorkItemsResponse{
		Items: items, NextCursor: got.Next, Teams: teams, Providers: providers, Total: int32(got.Total),
	}), nil
}

// workSource is the source a ListWorkItems request reads: the one named, or the only one for
// "". Nil with no error is no source at all, which lists nothing.
func (s *AgentService) workSource(provider string) (work.Source, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		switch len(s.workSources) {
		case 0:
			return nil, nil
		case 1:
			return s.workSources[0], nil
		default:
			return nil, connect.NewError(connect.CodeUnimplemented, errMergeProviders)
		}
	}
	for _, src := range s.workSources {
		if src.Provider() == provider {
			return src, nil
		}
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider %q is not an issue tracker Podium lists", provider))
}
