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

type openaiFixture struct {
	svc     *AgentService
	secrets *fakeSecrets
	log     *bytes.Buffer
	idp     *fakeCodex
}

func newOpenAIFixture(t *testing.T, idp *fakeCodex) openaiFixture {
	t.Helper()
	api := fakeOpenAI(t, fakeOpenAIKey, "access-1", "access-2")
	f := openaiFixture{secrets: newFakeSecrets(), log: &bytes.Buffer{}, idp: idp}

	opts := AgentServiceOptions{
		Store:              newStore(t),
		Secrets:            f.secrets,
		Model:              "gpt-5.4",
		OpenAIBaseURL:      api.URL,
		OpenAICodexBaseURL: api.URL,
		HTTPClient:         api.Client(),
		Logger:             slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if idp != nil {
		opts.OpenAIOAuthIssuer = idp.srv.URL
		opts.OpenAIOAuthClientID = "podium-test"
		opts.HTTPClient = idp.srv.Client()
	}
	f.svc = NewAgentService(opts)
	return f
}

func TestSetProviderKeyStoresTheOpenAIKeyUnderItsOwnSecret(t *testing.T) {
	f := newOpenAIFixture(t, nil)
	res, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderOpenAI, Key: fakeOpenAIKey,
	}))
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAI, res.Msg.GetProvider().GetProvider())
	assert.Equal(t, AuthAPIKey, res.Msg.GetProvider().GetAuthKind())
	assert.Equal(t, []string{"gpt-5.4", "gpt-5.3-codex"}, res.Msg.GetModels())
	assert.Equal(t, fakeOpenAIKey, string(f.secrets.set[profiles.OpenAIKeySecret]))
	assert.NotContains(t, f.secrets.set, profiles.AnthropicKeySecret)
	assert.NotContains(t, f.secrets.set, profiles.XAIKeySecret)
	assert.NotContains(t, f.log.String(), fakeOpenAIKey)
}

func TestGetSettingsReportsOpenAIAlongsideTheOthers(t *testing.T) {
	f := newOpenAIFixture(t, nil)
	_, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderOpenAI, Key: fakeOpenAIKey,
	}))
	require.NoError(t, err)

	res, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetProviders(), 3)
	byName := map[string]*agentv1.ProviderSettings{}
	for _, p := range res.Msg.GetProviders() {
		byName[p.GetProvider()] = p
	}
	assert.True(t, byName[ProviderOpenAI].GetKeySet())
	assert.False(t, byName[ProviderAnthropic].GetKeySet())
	assert.False(t, byName[ProviderXAI].GetKeySet())
}

func TestOpenAISubscriptionSignInStoresTheAccessToken(t *testing.T) {
	idp := newFakeCodex(t)
	idp.approved.Store(true)
	f := newOpenAIFixture(t, idp)

	start, err := f.svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.StartProviderOAuthRequest{Provider: ProviderOpenAI}))
	require.NoError(t, err)
	assert.Equal(t, "WDJB-MJHT", start.Msg.GetUserCode())
	assert.Contains(t, start.Msg.GetVerificationUri(), "/codex/device")

	poll, err := f.svc.PollProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.PollProviderOAuthRequest{Provider: ProviderOpenAI, FlowId: start.Msg.GetFlowId()}))
	require.NoError(t, err)
	require.Equal(t, OAuthDone, poll.Msg.GetState())
	assert.Equal(t, AuthOAuth, poll.Msg.GetProvider().GetAuthKind())
	assert.Equal(t, "access-1", string(f.secrets.set[profiles.OpenAIKeySecret]))
}

func TestOpenAISignInIsRefusedWithNoOAuthClientConfigured(t *testing.T) {
	f := newOpenAIFixture(t, nil)
	_, err := f.svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.StartProviderOAuthRequest{Provider: ProviderOpenAI}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "PODIUM_AGENT_OPENAI_OAUTH_CLIENT_ID")
}

func TestListAgentsMarksOpenAIReadyOnceAKeyIsStored(t *testing.T) {
	f := newOpenAIFixture(t, nil)
	_, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderOpenAI, Key: fakeOpenAIKey,
	}))
	require.NoError(t, err)

	res, err := f.svc.ListAgents(loginCtx("alice"), connect.NewRequest(&agentv1.ListAgentsRequest{}))
	require.NoError(t, err)
	for _, a := range res.Msg.GetAgents() {
		assert.Equal(t, a.GetProvider() == ProviderOpenAI, a.GetReady(),
			"only the backend whose credential was stored is ready")
	}
}
