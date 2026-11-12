package connections

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
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
	got, err := MergeGitHub(saved, "123", "", "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "pem", got.PrivateKey)
	assert.Empty(t, got.WebhookSecret)
	assert.Empty(t, got.WebhookListen)
}

func TestMergeGitHubRejectsAListenAddressWithoutAPort(t *testing.T) {
	_, err := MergeGitHub(nil, "123", "pem", "secret", "8091", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host:port")
}

func TestMergeGitHubKeepsTheClientSecretAndDropsItWithTheClientID(t *testing.T) {
	saved := &GitHub{AppID: "123", PrivateKey: "pem", ClientID: "Iv23abc", ClientSecret: "csec"}
	got, err := MergeGitHub(saved, "123", "", "", "", "Iv23abc", "")
	require.NoError(t, err)
	assert.Equal(t, "csec", got.ClientSecret)
	assert.True(t, got.UserAuth())

	got, err = MergeGitHub(saved, "123", "", "", "", "", "")
	require.NoError(t, err)
	assert.Empty(t, got.ClientID)
	assert.Empty(t, got.ClientSecret)
	assert.False(t, got.UserAuth())

	_, err = MergeGitHub(nil, "123", "pem", "", "", "Iv23abc", "")
	require.ErrorContains(t, err, "client secret")
}

func TestOverlayReplacesTheKeyFile(t *testing.T) {
	cfg := config.Config{GitHubAppID: "1", GitHubAppKeyFile: "/etc/podium/github-app.pem"}
	got := Overlay(cfg, nil, &GitHub{AppID: "99", PrivateKey: "pem", WebhookListen: "0.0.0.0:9"}, nil)
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

	row, err := MergeGitHub(nil, "123456", string(raw), "whsec", "0.0.0.0:8091", "", "")
	require.NoError(t, err)
	cfg := Overlay(config.Config{
		Listen:  "0.0.0.0:8090",
		TaskURL: config.DefaultTaskURL,
	}, nil, &row, nil)
	require.NoError(t, cfg.ValidateConnectionFields())
	assert.True(t, cfg.GitHubAppEnabled())
	assert.True(t, cfg.GitHubSourceEnabled())
}

func TestGitHubDiffersWhenTheProcessHasNotRestarted(t *testing.T) {
	env := config.Config{}
	running := env
	saved := &GitHub{AppID: "123", PrivateKey: "not-a-key"}
	assert.True(t, GitHubDiffers(env, running, saved))
	applied := Overlay(running, nil, saved, nil)
	assert.False(t, GitHubDiffers(env, applied, saved))
}

// ---------------------------------------------------------------------------
// the Linear agent app
// ---------------------------------------------------------------------------

const fakeLinearWebhookSecret = "lin_wh_not-a-real-secret"

func savedLinear() *Linear {
	return &Linear{
		ClientID: "client-1", ClientSecret: "secret-1",
		WebhookSecret: fakeLinearWebhookSecret, WebhookListen: "0.0.0.0:8091",
		AppUserID: "app-user-1", AppUserName: "Podium",
		OrganizationID: "org-1", OrganizationName: "Acme", OrganizationURLKey: "acme",
	}
}

func TestMergeLinearRequiresEverythingOnTheFirstSave(t *testing.T) {
	_, err := MergeLinear(nil, "", "secret", fakeLinearWebhookSecret, "0.0.0.0:8091")
	require.ErrorContains(t, err, "client id")
	_, err = MergeLinear(nil, "client", "", fakeLinearWebhookSecret, "0.0.0.0:8091")
	require.ErrorContains(t, err, "client secret")
	_, err = MergeLinear(nil, "client", "secret", "", "0.0.0.0:8091")
	require.ErrorContains(t, err, "webhook signing secret")
	_, err = MergeLinear(nil, "client", "secret", fakeLinearWebhookSecret, "")
	require.ErrorContains(t, err, "listen address")

	got, err := MergeLinear(nil, " client ", " secret ", " "+fakeLinearWebhookSecret+" ", " 0.0.0.0:8091 ")
	require.NoError(t, err)
	assert.Equal(t, Linear{
		ClientID: "client", ClientSecret: "secret",
		WebhookSecret: fakeLinearWebhookSecret, WebhookListen: "0.0.0.0:8091",
	}, got)
}

func TestMergeLinearChecksTheShapeOfWhatItCan(t *testing.T) {
	_, err := MergeLinear(nil, "client", "secret", "whsec-from-github", "0.0.0.0:8091")
	require.ErrorContains(t, err, "lin_wh_")
	_, err = MergeLinear(nil, "client", "secret", fakeLinearWebhookSecret, "8091")
	require.ErrorContains(t, err, "host:port")
}

// A blank field keeps the saved value, and what Settings learned about the app stays with
// it, so moving the listen address does not forget who the app is.
func TestMergeLinearKeepsABlankField(t *testing.T) {
	got, err := MergeLinear(savedLinear(), "", "", "", "0.0.0.0:9000")
	require.NoError(t, err)
	want := *savedLinear()
	want.WebhookListen = "0.0.0.0:9000"
	assert.Equal(t, want, got)
}

// A new client id is a different app: the saved secrets and identity belong to the old one.
func TestMergeLinearDoesNotCarrySecretsToANewApp(t *testing.T) {
	_, err := MergeLinear(savedLinear(), "client-2", "", "", "")
	require.ErrorContains(t, err, "needs its client secret")
	_, err = MergeLinear(savedLinear(), "client-2", "secret-2", "", "")
	require.ErrorContains(t, err, "webhook signing secret")

	got, err := MergeLinear(savedLinear(), "client-2", "secret-2", "lin_wh_two", "")
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:8091", got.WebhookListen, "the listen address is this host's, not the app's")
	assert.Empty(t, got.AppUserID)
	assert.Empty(t, got.OrganizationName)
}

func TestOverlayWritesTheLinearAppAndLeavesThePollerAlone(t *testing.T) {
	env := config.Config{LinearAPIKey: "lin_api_poller", LinearURL: config.DefaultLinearURL}
	got := Overlay(env, nil, nil, savedLinear())
	assert.Equal(t, "client-1", got.LinearClientID)
	assert.Equal(t, "secret-1", got.LinearClientSecret)
	assert.Equal(t, fakeLinearWebhookSecret, got.LinearWebhookSecret)
	assert.Equal(t, "0.0.0.0:8091", got.LinearWebhookListen)
	assert.True(t, got.LinearAppEnabled())
	assert.True(t, got.LinearEnabled(), "the poller's key is the environment's, and it stays")
	assert.Empty(t, env.LinearClientID, "overlay does not mutate the env config")

	assert.False(t, Overlay(env, nil, nil, nil).LinearAppEnabled(), "the app is not read from the environment")
}

func TestLinearDiffersWhenTheProcessHasNotPickedItUp(t *testing.T) {
	running := config.Config{}
	saved := savedLinear()
	assert.True(t, LinearDiffers(config.Config{}, running, saved))
	applied := Overlay(running, nil, nil, saved)
	assert.False(t, LinearDiffers(config.Config{}, applied, saved))

	rotated := *saved
	rotated.WebhookSecret = "lin_wh_rotated"
	assert.True(t, LinearDiffers(config.Config{}, applied, &rotated))
	// What Settings learned about the app is not configuration.
	renamed := *saved
	renamed.AppUserName = "Renamed"
	assert.False(t, LinearDiffers(config.Config{}, applied, &renamed))

	assert.True(t, LinearDiffers(config.Config{}, applied, nil), "a cleared app is owed until the process lets go")
	assert.False(t, LinearDiffers(config.Config{}, config.Config{}, nil))
}

func TestASavedLinearAppPassesTheSameCheckAsStartup(t *testing.T) {
	row, err := MergeLinear(nil, "client", "secret", fakeLinearWebhookSecret, "0.0.0.0:8091")
	require.NoError(t, err)
	cfg := Overlay(config.Config{Listen: "0.0.0.0:8090"}, nil, nil, &row)
	require.NoError(t, cfg.ValidateConnectionFields())
}

// memVault is the secret store in memory.
type memVault struct {
	set map[string][]byte
}

func (v *memVault) SetSecret(_ context.Context, name string, value []byte) (int32, error) {
	if v.set == nil {
		v.set = map[string][]byte{}
	}
	v.set[name] = append([]byte(nil), value...)
	return 1, nil
}

func (v *memVault) DeleteSecret(_ context.Context, name string) error {
	if _, ok := v.set[name]; !ok {
		return connect.NewError(connect.CodeNotFound, errors.New("no such secret"))
	}
	delete(v.set, name)
	return nil
}

func (v *memVault) ReadSecret(_ context.Context, _, name string) ([]byte, error) {
	value, ok := v.set[name]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such secret"))
	}
	return value, nil
}

// The access token is one secret holding the token, its expiry and the app it is for, so
// every conductor on the database finds the same one and a different app finds none.
func TestTheLinearAccessTokenIsSharedThroughTheVault(t *testing.T) {
	ctx := t.Context()
	v := &memVault{}
	none, err := LoadLinearToken(ctx, v, "client-1")
	require.NoError(t, err)
	assert.Zero(t, none)

	at := time.Date(2026, 12, 12, 9, 0, 0, 0, time.UTC)
	require.NoError(t, StoreLinearToken(ctx, v, LinearToken{ClientID: "client-1", AccessToken: "lin_oauth_x", ExpiresAt: at}))
	assert.Contains(t, v.set, LinearAccessTokenSecret)

	got, err := LoadLinearToken(ctx, v, "client-1")
	require.NoError(t, err)
	assert.Equal(t, "lin_oauth_x", got.AccessToken)
	assert.True(t, at.Equal(got.ExpiresAt))

	other, err := LoadLinearToken(ctx, v, "client-2")
	require.NoError(t, err)
	assert.Zero(t, other, "a token minted for another app is not this one's")

	v.set[LinearAccessTokenSecret] = []byte("not json")
	_, err = LoadLinearToken(ctx, v, "client-1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not json", "the error names the secret, not its value")
}
