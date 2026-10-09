//go:build integration

package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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
