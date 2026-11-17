package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/slack"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// SlackDirectory is the Slack source as the Channels screen is allowed to see it: the
// conversations the bot is in, so the catalogue can be seeded before the first mention.
type SlackDirectory interface {
	ListChannels(ctx context.Context) ([]slack.ChannelInfo, error)
}

// ListSlackChannels reports the stored catalogue of Slack channels. Listing a workspace's
// channels from Slack is slow and the set rarely changes, so it does that only when asked
// to refresh, or once when the catalogue has never been filled from Slack — otherwise a
// fresh conductor would show nothing until someone mentioned the bot.
func (s *AgentService) ListSlackChannels(
	ctx context.Context, req *connect.Request[agentv1.ListSlackChannelsRequest],
) (*connect.Response[agentv1.ListSlackChannelsResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	dir := s.slackDirectory()
	refreshedAt, err := s.store.SlackChannelsRefreshedAt(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	switch {
	case req.Msg.GetRefresh():
		if dir == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("slack is not connected on this conductor"))
		}
		// A failed refresh is an error, not a quiet fall back to the stored rows: the caller
		// asked for fresh data and must be told it did not get it.
		if err := s.refreshSlackChannels(ctx, dir); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	case dir != nil && refreshedAt.IsZero():
		if err := s.refreshSlackChannels(ctx, dir); err != nil {
			s.logger.WarnContext(ctx, "the first listing of slack channels failed; returning the catalogue", "error", err)
		}
	}
	if dir != nil {
		if refreshedAt, err = s.store.SlackChannelsRefreshedAt(ctx); err != nil {
			return nil, storeError(err)
		}
	}
	rows, err := s.store.ListSlackChannels(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.SlackChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, slackChannelToProto(r))
	}
	resp := &agentv1.ListSlackChannelsResponse{
		Channels:       out,
		SlackConnected: dir != nil,
	}
	if !refreshedAt.IsZero() {
		resp.RefreshedAt = timestamppb.New(refreshedAt)
	}
	return connect.NewResponse(resp), nil
}

// refreshSlackChannels lists the conversations the bot is in and stores their names, which is
// how a channel the bot was invited to appears before anyone has mentioned it. The time is
// recorded only once every row is written, so a half-finished refresh does not read as fresh.
func (s *AgentService) refreshSlackChannels(ctx context.Context, dir SlackDirectory) error {
	page, err := dir.ListChannels(ctx)
	if err != nil {
		return fmt.Errorf("list channels from slack: %w", err)
	}
	for _, ch := range page {
		if err := s.store.UpsertSlackChannelName(ctx, ch.ID, ch.Name); err != nil {
			return err
		}
	}
	return s.store.MarkSlackChannelsRefreshed(ctx)
}

// SetSlackChannelDescription stores the operator note a turn of this channel is briefed with.
func (s *AgentService) SetSlackChannelDescription(
	ctx context.Context, req *connect.Request[agentv1.SetSlackChannelDescriptionRequest],
) (*connect.Response[agentv1.SetSlackChannelDescriptionResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	id := req.Msg.GetId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("set slack channel description: id is required"))
	}
	row, err := s.store.SetSlackChannelDescription(ctx, id, req.Msg.GetDescription())
	switch {
	case errors.Is(err, store.ErrInvalidSlackChannelDescription):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a slack channel description was set",
		"channel", id, "login", Login(ctx))
	return connect.NewResponse(&agentv1.SetSlackChannelDescriptionResponse{
		Channel: slackChannelToProto(row),
	}), nil
}

func slackChannelToProto(c store.SlackChannel) *agentv1.SlackChannel {
	return &agentv1.SlackChannel{
		Id:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		UpdatedAt:   timestamppb.New(c.UpdatedAt),
	}
}
