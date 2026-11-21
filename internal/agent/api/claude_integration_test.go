//go:build integration

package api

import (
	"context"
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

// A Claude plan is one person's, so nobody signs one in for everyone: not an admin, whose
// request is refused before Claude is asked for anything, and not a member.
func TestAClaudeSubscriptionCannotBeSignedInForEveryone(t *testing.T) {
	secrets := newFakeSecrets()
	f := newClaudeService(t, newFakeClaude(t), secrets)

	_, err := f.StartProviderOAuth(adminCtx("admin"), connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic, Global: true,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "belongs to one person")
	assert.Empty(t, secrets.set)

	_, err = f.StartProviderOAuth(loginCtx("bob"), connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderAnthropic, Global: true,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	settings, err := f.GetSettings(adminCtx("admin"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, settings.Msg.GetProvider().GetKeySet())

	// An older client's "sign the global Claude subscription out" removes nothing, least of
	// all the global API key the same request would otherwise name.
	secrets.setBehindTheUI(profiles.AnthropicKeySecret, "sk-ant-api03-company")
	_, err = f.ClearProviderKey(adminCtx("admin"), connect.NewRequest(&agentv1.ClearProviderKeyRequest{
		Provider: ProviderAnthropic, Global: true, ClaudeSubscription: true, //nolint:staticcheck // the deprecated field is what is being refused
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, secrets.set, profiles.AnthropicKeySecret)
}

// A control plane that once let an admin sign a Claude plan in for everyone signs it out when
// it starts: the access token, the refresh token and the row, and nothing else.
func TestAClaudeSubscriptionSignedInForEveryoneIsSignedOutAtStart(t *testing.T) {
	ctx := context.Background()
	secrets := newFakeSecrets()
	f := newClaudeService(t, newFakeClaude(t), secrets)
	p, err := findProvider(ProviderAnthropic)
	require.NoError(t, err)

	// Nothing stored: nothing is done, and the global key is not touched.
	secrets.setBehindTheUI(profiles.AnthropicKeySecret, "sk-ant-api03-company")
	f.RemoveGlobalClaudeSubscription(ctx)
	assert.Contains(t, secrets.set, profiles.AnthropicKeySecret)

	secrets.setBehindTheUI(profiles.ClaudeSubscriptionSecret, "sk-ant-oat01-company")
	secrets.setBehindTheUI(refreshSecret(p), "sk-ant-ort01-company")
	require.NoError(t, f.store.PutSetting(ctx, profiles.ClaudeSubscriptionSettingKey,
		providerRow{AuthKind: AuthOAuth, Account: "admin@example.test", SetBy: "admin", Refreshable: true}))
	// Alice's own sign-in is hers and stays.
	mine := credTarget{p: p, owner: "alice"}
	_, err = secrets.SetPersonalSecret(ctx, "alice", mine.secret(), []byte("sk-ant-oat01-alice"))
	require.NoError(t, err)

	f.RemoveGlobalClaudeSubscription(ctx)
	assert.NotContains(t, secrets.set, profiles.ClaudeSubscriptionSecret)
	assert.NotContains(t, secrets.set, refreshSecret(p))
	var row providerRow
	assert.Error(t, f.store.GetSetting(ctx, profiles.ClaudeSubscriptionSettingKey, &row), "the row is gone")
	assert.Equal(t, "sk-ant-api03-company", string(secrets.set[profiles.AnthropicKeySecret]),
		"the global key is what those chats spend now")
	assert.Equal(t, "sk-ant-oat01-alice", string(secrets.set[mine.secret()]))

	// The refresh pass has nothing of it left to renew, and a second start finds nothing.
	for _, tg := range f.signedInTargets(ctx) {
		assert.False(t, tg.owner == "" && tg.p.name == ProviderAnthropic, "no global Claude sign-in is refreshed")
	}
	f.RemoveGlobalClaudeSubscription(ctx)
}

// Why the sign-in is off is the conductor's to know (no chat host, no Claude Code, one too
// old), and whoever tries it is told that, not a list of variables to go and read about.
func TestAClaudeSignInThatIsOffSaysWhy(t *testing.T) {
	const why = "Claude Code 2.1.100 at /usr/local/bin/claude is too old: update it to 2.1.248 or later"
	svc := NewAgentService(AgentServiceOptions{
		Store:          newStore(t),
		Secrets:        newFakeSecrets(),
		ClaudeAgentOff: why,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	_, err := svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.StartProviderOAuthRequest{Provider: ProviderAnthropic}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), why)
}
