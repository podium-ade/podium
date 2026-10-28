package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// GetConnections reports Slack, GitHub and Linear without any secret value.
func (s *AgentService) GetConnections(
	ctx context.Context, _ *connect.Request[agentv1.GetConnectionsRequest],
) (*connect.Response[agentv1.GetConnectionsResponse], error) {
	slack, gh, err := s.loadConnections(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetConnectionsResponse{
		Slack:  slackView(s.env, s.runningConfig(), slack),
		Github: githubView(s.runningConfig(), gh),
		Linear: &agentv1.LinearConnection{Configured: s.env.LinearEnabled()},
	}), nil
}

// SetSlackConnection validates the pair, tries it against Slack, stores it and switches the
// running conductor to it. restart_required is left true only when that switch failed.
func (s *AgentService) SetSlackConnection(
	ctx context.Context, req *connect.Request[agentv1.SetSlackConnectionRequest],
) (*connect.Response[agentv1.SetSlackConnectionResponse], error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := connections.LoadSlack(ctx, s.store, s.secrets)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	row, err := connections.MergeSlack(existing, req.Msg.GetAppToken(), req.Msg.GetBotToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	next := connections.Overlay(s.runningConfig(), &row, nil)
	if err := next.ValidateConnectionFields(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.checkConnections(ctx, next); err != nil {
		return nil, err
	}
	row.SetBy = Login(ctx)
	row.SetAt = time.Now().UTC()
	s.logger.InfoContext(ctx, "saving a slack connection", "login", row.SetBy,
		"request", redactedSlackRequest{})
	if err := connections.SaveSlack(ctx, s.store, s.secrets, row, existing); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.applyConnections(ctx)
	return connect.NewResponse(&agentv1.SetSlackConnectionResponse{
		Slack: slackView(s.env, s.runningConfig(), &row),
	}), nil
}

// ClearSlackConnection drops the saved pair and falls back to the environment's, or to no
// Slack at all.
func (s *AgentService) ClearSlackConnection(
	ctx context.Context, _ *connect.Request[agentv1.ClearSlackConnectionRequest],
) (*connect.Response[agentv1.ClearSlackConnectionResponse], error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// The environment's pair is what Slack falls back to, so it is tried like a paste.
	next := connections.Overlay(s.runningConfig(), &connections.Slack{
		AppToken: s.env.SlackAppToken, BotToken: s.env.SlackBotToken,
	}, nil)
	if err := s.checkConnections(ctx, next); err != nil {
		return nil, err
	}
	if err := connections.ClearSlack(ctx, s.store, s.secrets); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "cleared the saved slack connection", "login", Login(ctx))
	s.applyConnections(ctx)
	return connect.NewResponse(&agentv1.ClearSlackConnectionResponse{
		Slack: slackView(s.env, s.runningConfig(), nil),
	}), nil
}

// SetGitHubConnection validates the App the way start-up does, tries it against GitHub,
// stores it and switches the running conductor to it.
func (s *AgentService) SetGitHubConnection(
	ctx context.Context, req *connect.Request[agentv1.SetGitHubConnectionRequest],
) (*connect.Response[agentv1.SetGitHubConnectionResponse], error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := connections.LoadGitHub(ctx, s.store, s.secrets)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	row, err := connections.MergeGitHub(existing, req.Msg.GetAppId(), req.Msg.GetPrivateKey(),
		req.Msg.GetWebhookSecret(), req.Msg.GetWebhookListen(),
		req.Msg.GetClientId(), req.Msg.GetClientSecret())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Validate against the process's listen address and task URL. A saved App does not
	// get to change how a task reaches this conductor.
	next := connections.Overlay(s.runningConfig(), nil, &row)
	if err := next.ValidateConnectionFields(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.checkConnections(ctx, next); err != nil {
		return nil, err
	}
	row.SetBy = Login(ctx)
	row.SetAt = time.Now().UTC()
	s.logger.InfoContext(ctx, "saving a github connection", "login", row.SetBy,
		"app_id", row.AppID, "reviews", row.WebhookListen != "", "user_auth", row.UserAuth(),
		"request", redactedGitHubRequest{appID: row.AppID, listen: row.WebhookListen})
	if err := connections.SaveGitHub(ctx, s.store, s.secrets, row, existing); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.applyConnections(ctx)
	return connect.NewResponse(&agentv1.SetGitHubConnectionResponse{
		Github: githubView(s.runningConfig(), &row),
	}), nil
}

// ClearGitHubConnection drops the saved App and turns it off in the running conductor.
func (s *AgentService) ClearGitHubConnection(
	ctx context.Context, _ *connect.Request[agentv1.ClearGitHubConnectionRequest],
) (*connect.Response[agentv1.ClearGitHubConnectionResponse], error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := connections.ClearGitHub(ctx, s.store, s.secrets); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "cleared the saved github connection", "login", Login(ctx))
	s.applyConnections(ctx)
	return connect.NewResponse(&agentv1.ClearGitHubConnectionResponse{
		Github: githubView(s.runningConfig(), nil),
	}), nil
}

// checkConnections refuses a Slack pair or GitHub App that Slack or GitHub does not accept,
// before it is stored. Without it a bad token would be saved, applied and then fail to
// connect, and the next start would fail the same way.
func (s *AgentService) checkConnections(ctx context.Context, next config.Config) error {
	if s.connections == nil {
		return nil
	}
	if err := s.connections.Check(ctx, next); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// applyConnections switches the running conductor to what was just saved. A failure is
// not the save's: the connection is stored, and restart_required stays true so the
// operator knows the running conductor is still on the previous one.
func (s *AgentService) applyConnections(ctx context.Context) {
	if s.connections == nil {
		return
	}
	if err := s.connections.Apply(ctx); err != nil {
		s.logger.ErrorContext(ctx, "the saved connection is stored, but the running conductor "+
			"could not switch to it; restart the conductor to use it", "error", err)
	}
}

func (s *AgentService) requireConnectionStore() error {
	if s.store == nil || s.secrets == nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database or secret store, so it cannot store a connection"))
	}
	return nil
}

func (s *AgentService) loadConnections(ctx context.Context) (*connections.Slack, *connections.GitHub, error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, nil, err
	}
	slack, gh, err := connections.Load(ctx, s.store, s.secrets)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}
	return slack, gh, nil
}

func slackView(env, running config.Config, saved *connections.Slack) *agentv1.SlackConnection {
	out := &agentv1.SlackConnection{}
	app, bot := env.SlackAppToken, env.SlackBotToken
	if saved != nil {
		app, bot = saved.AppToken, saved.BotToken
		out.Source = connections.SourceSaved
		out.SetBy = saved.SetBy
		if !saved.SetAt.IsZero() {
			out.SetAt = timestamppb.New(saved.SetAt)
		}
	} else if app != "" && bot != "" {
		out.Source = connections.SourceEnvironment
	}
	out.Configured = app != "" && bot != ""
	out.AppTokenHint = connections.Hint(app)
	out.BotTokenHint = connections.Hint(bot)
	out.RestartRequired = connections.SlackDiffers(env, running, saved)
	return out
}

func githubView(running config.Config, saved *connections.GitHub) *agentv1.GitHubConnection {
	// The GitHub App is not read from the environment. An empty saved row is off,
	// whatever PODIUM_AGENT_GITHUB_* still says.
	out := &agentv1.GitHubConnection{}
	var id, secret, listen string
	var keySet bool
	if saved != nil {
		id = saved.AppID
		keySet = saved.PrivateKey != ""
		secret, listen = saved.WebhookSecret, saved.WebhookListen
		out.Source = connections.SourceSaved
		out.SetBy = saved.SetBy
		if !saved.SetAt.IsZero() {
			out.SetAt = timestamppb.New(saved.SetAt)
		}
	}
	out.AppId = id
	out.PrivateKeySet = keySet
	out.Configured = id != "" && keySet
	out.WebhookListen = listen
	out.WebhookSecretSet = secret != ""
	out.WebhookSecretHint = connections.Hint(secret)
	out.Reviews = secret != "" && listen != ""
	if saved != nil {
		out.ClientId = saved.ClientID
		out.ClientSecretSet = saved.ClientSecret != ""
	}
	out.RestartRequired = connections.GitHubDiffers(config.Config{}, running, saved)
	return out
}

// redactedSlackRequest is the only form of a SetSlackConnection request that reaches a
// log line. The tokens are not fields of this type, so they cannot be printed.
type redactedSlackRequest struct{}

func (redactedSlackRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("app_token", "[redacted]"),
		slog.String("bot_token", "[redacted]"),
	)
}

// redactedGitHubRequest logs the id and the listen address, which are not secrets, and
// names the key and the webhook secret without their values.
type redactedGitHubRequest struct {
	appID  string
	listen string
}

func (r redactedGitHubRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("app_id", r.appID),
		slog.String("private_key", "[redacted]"),
		slog.String("webhook_secret", "[redacted]"),
		slog.String("webhook_listen", r.listen),
		slog.String("client_secret", "[redacted]"),
	)
}
