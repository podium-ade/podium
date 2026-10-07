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
