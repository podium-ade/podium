package api

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
)

func TestLinearViewWithNothingSaved(t *testing.T) {
	out := linearView(config.Config{LinearAPIKey: "lin_api_poller"}, config.Config{}, nil)
	assert.True(t, out.GetAvailable())
	assert.False(t, out.GetConfigured())
	assert.Empty(t, out.GetSource())
	assert.True(t, out.GetPolling(), "the environment's key is the poller's, and the view says so")
	assert.False(t, out.GetRestartRequired())

	assert.False(t, linearView(config.Config{}, config.Config{}, nil).GetPolling())
}

func TestLinearViewReturnsNoSecret(t *testing.T) {
	saved := &connections.Linear{
		ClientID: "client-1", ClientSecret: "lsec-not-real-1234",
		WebhookSecret: "lin_wh_not-real-5678", WebhookListen: "0.0.0.0:8091",
		AppUserID: "app-user-1", AppUserName: "Podium",
		OrganizationName: "Acme", OrganizationURLKey: "acme",
		SetBy: "ada", SetAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
	}
	running := connections.Overlay(config.Config{}, nil, nil, saved)
	out := linearView(config.Config{}, running, saved)
	assert.True(t, out.GetConfigured())
	assert.Equal(t, connections.SourceSaved, out.GetSource())
	assert.Equal(t, "client-1", out.GetClientId())
	assert.True(t, out.GetClientSecretSet())
	assert.True(t, out.GetWebhookSecretSet())
	assert.Equal(t, "5678", out.GetWebhookSecretHint())
	assert.Equal(t, "0.0.0.0:8091", out.GetWebhookListen())
	assert.Equal(t, "Podium", out.GetAppUserName())
	assert.Equal(t, "acme", out.GetOrganizationUrlKey())
	assert.Equal(t, "ada", out.GetSetBy())
	assert.False(t, out.GetRestartRequired())
	assert.NotContains(t, out.String(), "lsec-not-real")
	assert.NotContains(t, out.String(), "lin_wh_not-real")

	assert.True(t, linearView(config.Config{}, config.Config{}, saved).GetRestartRequired(),
		"an app the process has not picked up is owed a restart")
}

func TestARedactedLinearRequestLogsNoSecret(t *testing.T) {
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("saving",
		"request", redactedLinearRequest{clientID: "client-1", listen: "0.0.0.0:8091"})
	out := buf.String()
	assert.Contains(t, out, "client-1")
	assert.Contains(t, out, "0.0.0.0:8091")
	assert.Contains(t, out, "client_secret=[redacted]")
	assert.Contains(t, out, "webhook_secret=[redacted]")
}
