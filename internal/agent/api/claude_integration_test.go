//go:build integration

package api

import (
	"io"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func newClaudeService(t *testing.T, f *fakeClaude, secrets *fakeSecrets) *AgentService {
	t.Helper()
	return NewAgentService(AgentServiceOptions{
		Store:                      newStore(t),
		Secrets:                    secrets,
		AnthropicBaseURL:           f.srv.URL,
		AnthropicOAuthClientID:     "claude-test",
		AnthropicOAuthAuthorizeURL: "https://claude.example.test/oauth/authorize",
		AnthropicOAuthTokenURL:     f.srv.URL + "/v1/oauth/token",
		AnthropicOAuthRedirectURI:  "https://platform.example.test/oauth/code/callback",
		AnthropicOAuthScopes:       "user:profile user:inference",
		ClaudeAgent:                true,
		HTTPClient:                 f.srv.Client(),
		Logger:                     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestTheClaudeSignInStoresAPersonalCredentialFromThePastedCode(t *testing.T) {
	f := newFakeClaude(t)
	secrets := newFakeSecrets()
	svc := newClaudeService(t, f, secrets)
	ctx := loginCtx("alice")

	start, err := svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic,
	}))
	require.NoError(t, err)
	assert.True(t, start.Msg.GetCodePaste())
	assert.Empty(t, start.Msg.GetUserCode())
	assert.Contains(t, start.Msg.GetVerificationUri(), "https://claude.example.test/oauth/authorize?")

	poll := func(code string) *agentv1.PollProviderOAuthResponse {
		res, perr := svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
			Provider: ProviderAnthropic, FlowId: start.Msg.GetFlowId(), Code: code,
		}))
		require.NoError(t, perr)
		return res.Msg
	}
	assert.Equal(t, OAuthPending, poll("").GetState(), "nothing pasted yet")
	assert.Empty(t, secrets.set)

	done := poll("good-code")
	require.Equal(t, OAuthDone, done.GetState())
	assert.Equal(t, AuthOAuth, done.GetProvider().GetAuthKind())
	assert.Equal(t, "alice@example.test", done.GetProvider().GetAccount())
	assert.True(t, done.GetProvider().GetRefreshable())
	assert.False(t, done.GetProvider().GetGlobal())

	mine := credTarget{p: providerSpecs[0], owner: "alice"}
	assert.Equal(t, "sk-ant-oat01-access-1", string(secrets.set[mine.secret()]))
	assert.Equal(t, "alice", secrets.owners[mine.secret()])
	assert.NotContains(t, secrets.set, profiles.AnthropicKeySecret, "the global key is untouched")
	assert.Equal(t, "sk-ant-ort01-refresh-1", string(secrets.set[mine.refreshName()]))
}

// An admin's global Claude sign-in is kept beside the global API key, never in it: every
// turn but the assistant's chats still needs the key.
func TestAGlobalClaudeSignInIsKeptBesideTheGlobalKey(t *testing.T) {
	secrets := newFakeSecrets()
	f := newClaudeService(t, newFakeClaude(t), secrets)
	ctx := adminCtx("admin")

	start, err := f.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic, Global: true,
	}))
	require.NoError(t, err)
	res, err := f.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderAnthropic, FlowId: start.Msg.GetFlowId(), Code: "good-code",
	}))
	require.NoError(t, err)
	require.Equal(t, OAuthDone, res.Msg.GetState())
	assert.True(t, res.Msg.GetProvider().GetGlobal())

	assert.Equal(t, "sk-ant-oat01-access-1", string(secrets.set[profiles.ClaudeSubscriptionSecret]))
	assert.Empty(t, secrets.owners[profiles.ClaudeSubscriptionSecret], "it is on the global list")
	assert.Equal(t, "sk-ant-ort01-refresh-1", string(secrets.set[claudeTarget().refreshName()]))
	assert.NotContains(t, secrets.set, profiles.AnthropicKeySecret, "the global key is untouched")

	settings, err := f.GetSettings(loginCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	sub := settings.Msg.GetClaudeSubscription()
	assert.True(t, sub.GetKeySet())
	assert.Equal(t, AuthOAuth, sub.GetAuthKind())
	assert.Equal(t, "alice@example.test", sub.GetAccount())
	assert.False(t, settings.Msg.GetProvider().GetKeySet(), "the global Anthropic key is still unset")

	// Nobody has a key, but the assistant's chats can run on the subscription.
	agents, err := f.ListAgents(loginCtx("bob"), connect.NewRequest(&agentv1.ListAgentsRequest{}))
	require.NoError(t, err)
	for _, a := range agents.Msg.GetAgents() {
		if a.GetProvider() == ProviderAnthropic {
			assert.Equal(t, CredentialGlobal, a.GetCredentialSource())
		}
	}

	// The chat host's claude-agent asks for it by that name.
	tok, err := f.HostCredential(ctx, ClaudeAgentCredential)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-access-1", tok)

	// Signing it out removes it and its refresh token, and nothing else.
	_, err = f.ClearProviderKey(ctx, connect.NewRequest(&agentv1.ClearProviderKeyRequest{
		Provider: ProviderAnthropic, Global: true, ClaudeSubscription: true,
	}))
	require.NoError(t, err)
	assert.NotContains(t, secrets.set, profiles.ClaudeSubscriptionSecret)
	assert.NotContains(t, secrets.set, claudeTarget().refreshName())
	settings, err = f.GetSettings(ctx, connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, settings.Msg.GetClaudeSubscription().GetKeySet())
}

func TestOnlyAnAdminCanSignInAClaudeSubscriptionForEveryone(t *testing.T) {
	f := newClaudeService(t, newFakeClaude(t), newFakeSecrets())
	_, err := f.StartProviderOAuth(loginCtx("bob"), connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic, Global: true,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = f.ClearProviderKey(loginCtx("bob"), connect.NewRequest(&agentv1.ClearProviderKeyRequest{
		Provider: ProviderAnthropic, Global: true, ClaudeSubscription: true,
	}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// claude_subscription names the global sign-in and nothing else.
	_, err = f.ClearProviderKey(adminCtx("admin"), connect.NewRequest(&agentv1.ClearProviderKeyRequest{
		Provider: ProviderXAI, Global: true, ClaudeSubscription: true,
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
