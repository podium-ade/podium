package connections

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/config"
)

func TestMergeSlackKeepsABlankField(t *testing.T) {
	saved := &Slack{AppToken: "xapp-kept", BotToken: "xoxb-kept"}
	got, err := MergeSlack(saved, "", "xoxb-new")
	require.NoError(t, err)
	assert.Equal(t, "xapp-kept", got.AppToken)
	assert.Equal(t, "xoxb-new", got.BotToken)
}

func TestMergeSlackRequiresBothOnTheFirstSave(t *testing.T) {
	_, err := MergeSlack(nil, "xapp-only", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bot token")

	_, err = MergeSlack(nil, "xoxb-swapped", "xapp-swapped")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "xapp-")
}

func TestMergeGitHubTurnsReviewsOffWhenListenIsCleared(t *testing.T) {
	saved := &GitHub{
		AppID: "123", PrivateKey: "pem",
		WebhookSecret: "secret", WebhookListen: "0.0.0.0:8091",
	}
	got, err := MergeGitHub(saved, "123", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "pem", got.PrivateKey)
	assert.Empty(t, got.WebhookSecret)
	assert.Empty(t, got.WebhookListen)
}

func TestMergeGitHubRejectsAListenAddressWithoutAPort(t *testing.T) {
	_, err := MergeGitHub(nil, "123", "pem", "secret", "8091")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host:port")
}

func TestOverlayReplacesTheKeyFile(t *testing.T) {
	cfg := config.Config{GitHubAppID: "1", GitHubAppKeyFile: "/etc/podium/github-app.pem"}
	got := Overlay(cfg, nil, &GitHub{AppID: "99", PrivateKey: "pem", WebhookListen: "0.0.0.0:9"})
	assert.Equal(t, "99", got.GitHubAppID)
	assert.Equal(t, "pem", got.GitHubAppKey)
	assert.Empty(t, got.GitHubAppKeyFile, "a saved key is the key, so the file path must not stay beside it")
	assert.Equal(t, "/etc/podium/github-app.pem", cfg.GitHubAppKeyFile, "overlay does not mutate the env config")
}

func TestHintWithholdsAShortSecret(t *testing.T) {
	assert.Equal(t, "1234", Hint("xapp-not-real-1234"))
	assert.Empty(t, Hint("abcd"))
	assert.Empty(t, Hint(""))
}

func TestSavedGitHubKeyPassesTheSameCheckAsStartup(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	raw := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	row, err := MergeGitHub(nil, "123456", string(raw), "whsec", "0.0.0.0:8091")
	require.NoError(t, err)
	cfg := Overlay(config.Config{
		Listen:  "0.0.0.0:8090",
		TaskURL: config.DefaultTaskURL,
	}, nil, &row)
	require.NoError(t, cfg.ValidateConnectionFields())
	assert.True(t, cfg.GitHubAppEnabled())
	assert.True(t, cfg.GitHubSourceEnabled())
}

func TestGitHubDiffersWhenTheProcessHasNotRestarted(t *testing.T) {
	env := config.Config{}
	running := env
	saved := &GitHub{AppID: "123", PrivateKey: "not-a-key"}
	assert.True(t, GitHubDiffers(env, running, saved))
	applied := Overlay(running, nil, saved)
	assert.False(t, GitHubDiffers(env, applied, saved))
}
