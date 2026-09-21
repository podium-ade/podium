package api

import (
	"context"
	"errors"

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

// ListSlackChannels reports every known Slack channel. When Slack is connected it also
// refreshes names from membership, which is how a channel the bot was invited to appears
// before anyone has mentioned it.
func (s *AgentService) ListSlackChannels(
	ctx context.Context, _ *connect.Request[agentv1.ListSlackChannelsRequest],
) (*connect.Response[agentv1.ListSlackChannelsResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	if s.slack != nil {
		page, err := s.slack.ListChannels(ctx)
		if err != nil {
			s.logger.WarnContext(ctx, "listing slack channels failed; returning the catalogue", "error", err)
		} else {
			for _, ch := range page {
				if err := s.store.UpsertSlackChannelName(ctx, ch.ID, ch.Name); err != nil {
					s.logger.WarnContext(ctx, "recording a slack channel name failed",
						"channel", ch.ID, "error", err)
				}
			}
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
	return connect.NewResponse(&agentv1.ListSlackChannelsResponse{
		Channels:       out,
		SlackConnected: s.slack != nil,
	}), nil
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
