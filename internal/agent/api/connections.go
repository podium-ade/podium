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
		Slack:  slackView(s.env, s.running, slack),
		Github: githubView(s.running, gh),
		Linear: &agentv1.LinearConnection{Configured: s.env.LinearEnabled()},
	}), nil
}

// SetSlackConnection validates the pair and stores it. The running process keeps the
// sockets it already opened; restart_required says so.
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
	next := connections.Overlay(s.running, &row, nil)
	if err := next.ValidateConnectionFields(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	row.SetBy = Login(ctx)
	row.SetAt = time.Now().UTC()
	s.logger.InfoContext(ctx, "saving a slack connection", "login", row.SetBy,
		"request", redactedSlackRequest{})
	if err := connections.SaveSlack(ctx, s.store, s.secrets, row, existing); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.SetSlackConnectionResponse{
		Slack: slackView(s.env, s.running, &row),
	}), nil
}

// ClearSlackConnection drops the saved pair. The environment applies on the next start.
func (s *AgentService) ClearSlackConnection(
	ctx context.Context, _ *connect.Request[agentv1.ClearSlackConnectionRequest],
) (*connect.Response[agentv1.ClearSlackConnectionResponse], error) {
	if err := s.requireConnectionStore(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := connections.ClearSlack(ctx, s.store, s.secrets); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "cleared the saved slack connection", "login", Login(ctx))
	return connect.NewResponse(&agentv1.ClearSlackConnectionResponse{
		Slack: slackView(s.env, s.running, nil),
	}), nil
}

// SetGitHubConnection validates the App the way start-up does, then stores it.
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
	next := connections.Overlay(s.running, nil, &row)
	if err := next.ValidateConnectionFields(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	row.SetBy = Login(ctx)
	row.SetAt = time.Now().UTC()
	s.logger.InfoContext(ctx, "saving a github connection", "login", row.SetBy,
		"app_id", row.AppID, "reviews", row.WebhookListen != "", "user_auth", row.UserAuth(),
		"request", redactedGitHubRequest{appID: row.AppID, listen: row.WebhookListen})
	if err := connections.SaveGitHub(ctx, s.store, s.secrets, row, existing); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.SetGitHubConnectionResponse{
		Github: githubView(s.running, &row),
	}), nil
}

// ClearGitHubConnection drops the saved App. The App is off on the next start.
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
	return connect.NewResponse(&agentv1.ClearGitHubConnectionResponse{
		Github: githubView(s.running, nil),
	}), nil
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
