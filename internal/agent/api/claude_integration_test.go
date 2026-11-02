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

func TestAClaudeSignInIsNeverTheGlobalCredential(t *testing.T) {
	f := newFakeClaude(t)
	svc := newClaudeService(t, f, newFakeSecrets())
	// The dev token is no person: its sign-ins would be the global credential.
	_, err := svc.StartProviderOAuth(loginCtx(""), connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
