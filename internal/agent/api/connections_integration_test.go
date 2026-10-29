//go:build integration

package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func TestConnectionsRoundTripNeverReturnsASecret(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	f.svc.env = f.svc.running

	app := "xapp-not-a-real-token-1234"
	bot := "xoxb-not-a-real-token-5678"
	saved, err := f.svc.SetSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.SetSlackConnectionRequest{
		AppToken: app,
		BotToken: bot,
	}))
	require.NoError(t, err)
	assert.True(t, saved.Msg.GetSlack().GetRestartRequired())
	assert.Equal(t, connections.SourceSaved, saved.Msg.GetSlack().GetSource())
	assert.Equal(t, "1234", saved.Msg.GetSlack().GetAppTokenHint())
	assert.NotContains(t, saved.Msg.String(), app)
	assert.NotContains(t, saved.Msg.String(), bot)
	assert.NotContains(t, f.log.String(), app)
	assert.NotContains(t, f.log.String(), bot)

	// The process is now running with the pair it saved, so a restart is no longer owed.
	f.svc.running.SlackAppToken = app
	f.svc.running.SlackBotToken = bot
	got, err := f.svc.GetConnections(loginCtx("ada"), connect.NewRequest(&agentv1.GetConnectionsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetSlack().GetRestartRequired())
	assert.NotContains(t, got.Msg.String(), app)

	key := githubTestKey(t)
	gh, err := f.svc.SetGitHubConnection(loginCtx("ada"), connect.NewRequest(&agentv1.SetGitHubConnectionRequest{
		AppId:         "123456",
		PrivateKey:    key,
		WebhookSecret: "whsec-not-real-9999",
		WebhookListen: "0.0.0.0:8091",
	}))
	require.NoError(t, err)
	assert.True(t, gh.Msg.GetGithub().GetConfigured())
	assert.True(t, gh.Msg.GetGithub().GetReviews())
	assert.Equal(t, "123456", gh.Msg.GetGithub().GetAppId())
	assert.Equal(t, "0.0.0.0:8091", gh.Msg.GetGithub().GetWebhookListen())
	assert.NotContains(t, gh.Msg.String(), "PRIVATE KEY")
	assert.NotContains(t, gh.Msg.String(), "whsec-not-real-9999")
	assert.NotContains(t, f.log.String(), "whsec-not-real-9999")
	assert.False(t, strings.Contains(f.log.String(), "BEGIN"))

	cleared, err := f.svc.ClearSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.ClearSlackConnectionRequest{}))
	require.NoError(t, err)
	assert.False(t, cleared.Msg.GetSlack().GetConfigured())
	_, err = f.svc.ClearSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.ClearSlackConnectionRequest{}))
	require.NoError(t, err, "clearing twice is not an error")
}

func TestSetSlackConnectionRefusesOneToken(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	_, err := f.svc.SetSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.SetSlackConnectionRequest{
		AppToken: "xapp-only",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func githubTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// The settings row holds what is not a credential. The key and secrets are in the secret
// store, and saving one field does not rewrite the others.
func TestASavedConnectionKeepsItsCredentialsInTheSecretStore(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	ctx := loginCtx("ada")

	key := githubTestKey(t)
	_, err := f.svc.SetGitHubConnection(ctx, connect.NewRequest(&agentv1.SetGitHubConnectionRequest{
		AppId: "123456", PrivateKey: key, ClientId: "Iv23abc", ClientSecret: "csec-not-real",
	}))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, f.svc.store.GetSetting(t.Context(), connections.GitHubKey, &raw))
	assert.Equal(t, true, raw["vaulted"])
	assert.NotContains(t, raw, "private_key")
	assert.NotContains(t, raw, "client_secret")
	assert.Equal(t, strings.TrimSpace(key), string(f.secrets.set[connections.GitHubPrivateKeySecret]))
	assert.Equal(t, "csec-not-real", string(f.secrets.set[connections.GitHubClientSecretSecret]))

	keyVersion := f.secrets.versions[connections.GitHubPrivateKeySecret]
	got, err := f.svc.SetGitHubConnection(ctx, connect.NewRequest(&agentv1.SetGitHubConnectionRequest{
		AppId: "123456", ClientId: "Iv23abc",
	}))
	require.NoError(t, err)
	assert.True(t, got.Msg.GetGithub().GetPrivateKeySet())
	assert.True(t, got.Msg.GetGithub().GetClientSecretSet())
	assert.Equal(t, keyVersion, f.secrets.versions[connections.GitHubPrivateKeySecret],
		"a save that kept the key must not rewrite it")

	_, err = f.svc.ClearGitHubConnection(ctx, connect.NewRequest(&agentv1.ClearGitHubConnectionRequest{}))
	require.NoError(t, err)
	assert.NotContains(t, f.secrets.set, connections.GitHubPrivateKeySecret)
	assert.NotContains(t, f.secrets.set, connections.GitHubClientSecretSecret)
}

// A row saved before the secret store held these is moved on its first load, and is
// identical to the conductor afterwards.
func TestAConnectionSavedInClearIsMovedIntoTheSecretStore(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	ctx := t.Context()
	require.NoError(t, f.svc.store.PutSetting(ctx, connections.GitHubKey, map[string]any{
		"app_id": "1", "private_key": "pem", "webhook_secret": "whsec", "webhook_listen": "0.0.0.0:8091",
		"client_id": "Iv23abc", "client_secret": "csec",
	}))
	require.NoError(t, f.svc.store.PutSetting(ctx, connections.SlackKey, map[string]any{
		"app_token": "xapp-old", "bot_token": "xoxb-old",
	}))

	slack, gh, err := connections.Load(ctx, f.svc.store, f.secrets)
	require.NoError(t, err)
	assert.Equal(t, "pem", gh.PrivateKey)
	assert.Equal(t, "csec", gh.ClientSecret)
	assert.Equal(t, "xoxb-old", slack.BotToken)

	for _, k := range []string{connections.GitHubKey, connections.SlackKey} {
		var raw map[string]any
		require.NoError(t, f.svc.store.GetSetting(ctx, k, &raw))
		assert.Equal(t, true, raw["vaulted"], k)
		for _, field := range []string{"private_key", "webhook_secret", "client_secret", "app_token", "bot_token"} {
			assert.NotContains(t, raw, field, k)
		}
	}
	assert.Equal(t, "whsec", string(f.secrets.set[connections.GitHubWebhookSecretSecret]))
	assert.Equal(t, "xapp-old", string(f.secrets.set[connections.SlackAppTokenSecret]))

	slack, gh, err = connections.Load(ctx, f.svc.store, f.secrets)
	require.NoError(t, err)
	assert.Equal(t, "pem", gh.PrivateKey)
	assert.Equal(t, "whsec", gh.WebhookSecret)
	assert.Equal(t, "xapp-old", slack.AppToken)
}

// fakeApplier stands in for the running conductor's connections. Apply does what the real
// one does on success: it tells the service what is now running.
type fakeApplier struct {
	svc      *AgentService
	checkErr error
	applyErr error
	checked  []config.Config
	applied  int
}

func (f *fakeApplier) Check(_ context.Context, cfg config.Config) error {
	f.checked = append(f.checked, cfg)
	return f.checkErr
}

func (f *fakeApplier) Apply(ctx context.Context) error {
	f.applied++
	if f.applyErr != nil {
		return f.applyErr
	}
	slack, gh, err := connections.Load(ctx, f.svc.store, f.svc.secrets)
	if err != nil {
		return err
	}
	f.svc.SetRunning(connections.Overlay(f.svc.env, slack, gh), nil)
	return nil
}

func TestASavedConnectionIsAppliedWithoutARestart(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	f.svc.env = f.svc.running
	applier := &fakeApplier{svc: f.svc}
	f.svc.connections = applier
	ctx := loginCtx("ada")

	saved, err := f.svc.SetSlackConnection(ctx, connect.NewRequest(&agentv1.SetSlackConnectionRequest{
		AppToken: "xapp-not-a-real-token-1234", BotToken: "xoxb-not-a-real-token-5678",
	}))
	require.NoError(t, err)
	require.Len(t, applier.checked, 1)
	assert.Equal(t, "xoxb-not-a-real-token-5678", applier.checked[0].SlackBotToken,
		"the pair is checked before it is stored")
	assert.Equal(t, 1, applier.applied)
	assert.False(t, saved.Msg.GetSlack().GetRestartRequired())

	gh, err := f.svc.SetGitHubConnection(ctx, connect.NewRequest(&agentv1.SetGitHubConnectionRequest{
		AppId: "123456", PrivateKey: githubTestKey(t),
	}))
	require.NoError(t, err)
	assert.False(t, gh.Msg.GetGithub().GetRestartRequired())

	cleared, err := f.svc.ClearGitHubConnection(ctx, connect.NewRequest(&agentv1.ClearGitHubConnectionRequest{}))
	require.NoError(t, err)
	assert.False(t, cleared.Msg.GetGithub().GetRestartRequired())
	assert.Equal(t, 3, applier.applied)
}

func TestARefusedConnectionIsNotSaved(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	applier := &fakeApplier{svc: f.svc, checkErr: errors.New("slack did not accept the bot token: invalid_auth")}
	f.svc.connections = applier

	_, err := f.svc.SetSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.SetSlackConnectionRequest{
		AppToken: "xapp-not-a-real-token-1234", BotToken: "xoxb-revoked-5678",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "invalid_auth")
	assert.Zero(t, applier.applied)
	_, ok := f.secrets.set[connections.SlackBotTokenSecret]
	assert.False(t, ok, "nothing reached the secret store")
}

// A save the running conductor cannot switch to is still a save, and says a restart is owed.
func TestAConnectionThatCannotBeAppliedStillSaves(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	f.svc.connections = &fakeApplier{svc: f.svc, applyErr: errors.New("webhook listener: address in use")}

	saved, err := f.svc.SetSlackConnection(loginCtx("ada"), connect.NewRequest(&agentv1.SetSlackConnectionRequest{
		AppToken: "xapp-not-a-real-token-1234", BotToken: "xoxb-not-a-real-token-5678",
	}))
	require.NoError(t, err)
	assert.True(t, saved.Msg.GetSlack().GetRestartRequired())
	assert.Contains(t, f.log.String(), "could not switch to it")
}

// The filters are their own row, saved without the App and without a restart.
func TestGitHubWebhookFiltersRoundTrip(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", nil)
	f.svc.running = config.Config{Listen: "0.0.0.0:8090", TaskURL: config.DefaultTaskURL}
	ctx := loginCtx("ada")

	got, err := f.svc.GetConnections(ctx, connect.NewRequest(&agentv1.GetConnectionsRequest{}))
	require.NoError(t, err)
	def := got.Msg.GetGithub().GetFilters()
	assert.Equal(t, connections.ReplyBotThreads, def.GetReplyMode())
	assert.True(t, def.GetAutoReview())
	assert.False(t, def.GetConversationReplies(), "a conversation-tab reply needs the mention unless turned on")

	saved, err := f.svc.SetGitHubWebhookFilters(ctx, connect.NewRequest(&agentv1.SetGitHubWebhookFiltersRequest{
		Filters: &agentv1.GitHubWebhookFilters{
			AllowedActors:  []string{"@alice", "alice", "bob"},
			TriggerPhrases: []string{" /review "},
			ReplyMode:      connections.ReplyAlways,

			ConversationReplies: true,
		},
	}))
	require.NoError(t, err)
	out := saved.Msg.GetGithub().GetFilters()
	assert.Equal(t, []string{"alice", "bob"}, out.GetAllowedActors())
	assert.Equal(t, []string{"/review"}, out.GetTriggerPhrases())
	assert.Equal(t, connections.ReplyAlways, out.GetReplyMode())
	assert.False(t, out.GetAutoReview())
	assert.True(t, out.GetConversationReplies())
	assert.Equal(t, "ada", out.GetSetBy())
	assert.False(t, saved.Msg.GetGithub().GetRestartRequired())

	got, err = f.svc.GetConnections(ctx, connect.NewRequest(&agentv1.GetConnectionsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, got.Msg.GetGithub().GetFilters().GetAllowedActors())

	_, err = f.svc.SetGitHubWebhookFilters(ctx, connect.NewRequest(&agentv1.SetGitHubWebhookFiltersRequest{
		Filters: &agentv1.GitHubWebhookFilters{AllowedActors: []string{"not a login"}},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
