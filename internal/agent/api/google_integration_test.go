//go:build integration

package api

import (
	"bytes"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func newGoogleService(t *testing.T) (*AgentService, *fakeSecrets, *bytes.Buffer) {
	t.Helper()
	api := fakeGoogle(t, fakeGoogleKey)
	secrets, log := newFakeSecrets(), &bytes.Buffer{}
	svc := NewAgentService(AgentServiceOptions{
		Store:         newStore(t),
		Secrets:       secrets,
		Model:         "gemini-3.8-flash",
		GoogleBaseURL: api.URL,
		HTTPClient:    api.Client(),
		Logger:        slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return svc, secrets, log
}

func TestSetProviderKeyStoresTheGoogleKeyUnderItsOwnSecret(t *testing.T) {
	svc, secrets, log := newGoogleService(t)
	res, err := svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderGoogle, Key: fakeGoogleKey,
	}))
	require.NoError(t, err)
	assert.Equal(t, ProviderGoogle, res.Msg.GetProvider().GetProvider())
	assert.Equal(t, AuthAPIKey, res.Msg.GetProvider().GetAuthKind())
	assert.Equal(t, []string{"gemini-3.8-flash", "gemini-3.1-pro-preview"}, res.Msg.GetModels())
	assert.Empty(t, res.Msg.GetStatus())
	assert.Equal(t, fakeGoogleKey, string(secrets.set[profiles.GoogleKeySecret]))
	assert.NotContains(t, secrets.set, profiles.AnthropicKeySecret)
	assert.NotContains(t, log.String(), fakeGoogleKey)
}

func TestSetProviderKeyRefusesABadGoogleKeyAndStoresNothing(t *testing.T) {
	svc, secrets, _ := newGoogleService(t)
	_, err := svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderGoogle, Key: "AIzaWrong00000000",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "Google rejected this key")
	assert.NotContains(t, secrets.set, profiles.GoogleKeySecret)
}

// Gemini is keys only. Asking it for a sign-in is an answer, not a crash.
func TestGoogleOffersNoSubscriptionSignIn(t *testing.T) {
	svc, _, _ := newGoogleService(t)
	_, err := svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderGoogle,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListAgentsMarksGeminiReadyOnceAKeyIsStored(t *testing.T) {
	svc, _, _ := newGoogleService(t)
	_, err := svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderGoogle, Key: fakeGoogleKey,
	}))
	require.NoError(t, err)

	res, err := svc.ListAgents(loginCtx("alice"), connect.NewRequest(&agentv1.ListAgentsRequest{}))
	require.NoError(t, err)
	var seen bool
	for _, a := range res.Msg.GetAgents() {
		assert.Equal(t, a.GetProvider() == ProviderGoogle, a.GetReady(),
			"only the backend whose credential was stored is ready")
		seen = seen || a.GetId() == profiles.AgentGemini
	}
	assert.True(t, seen, "the gemini backend is in the catalogue")
}
