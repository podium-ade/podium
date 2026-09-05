//go:build integration

package api

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// xaiFixture is a conductor wired to a fake xAI and, optionally, a fake issuer.
type xaiFixture struct {
	svc     *AgentService
	secrets *fakeSecrets
	log     *bytes.Buffer
	idp     *fakeIDP
}

func newXAIFixture(t *testing.T, idp *fakeIDP) xaiFixture {
	t.Helper()
	// The tokens the fake issuer hands out are credentials for this fake API too: a
	// subscription access token is a bearer for the same endpoint an API key is.
	api := fakeXAI(t, fakeXAIKey, "access-1", "access-2")
	f := xaiFixture{secrets: newFakeSecrets(), log: &bytes.Buffer{}, idp: idp}

	opts := AgentServiceOptions{
		Store:      newStore(t),
		Secrets:    f.secrets,
		Model:      "grok-4.6",
		XAIBaseURL: api.URL,
		HTTPClient: api.Client(),
		Logger:     slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if idp != nil {
		opts.XAIOAuthIssuer = idp.srv.URL
		opts.XAIOAuthClientID = "podium-test"
		// One client reaches both fakes. The issuer is TLS — discovery refuses a plaintext
		// endpoint — so it has to be the issuer's client, which trusts that certificate;
		// the fake API is plain HTTP, which any client can reach.
		opts.HTTPClient = idp.srv.Client()
	}
	f.svc = NewAgentService(opts)
	return f
}

func TestSetProviderKeyStoresTheXAIKeyUnderItsOwnSecret(t *testing.T) {
	f := newXAIFixture(t, nil)
	res, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderXAI, Key: fakeXAIKey,
	}))
	require.NoError(t, err)
	assert.Equal(t, ProviderXAI, res.Msg.GetProvider().GetProvider())
	assert.Equal(t, AuthAPIKey, res.Msg.GetProvider().GetAuthKind())
	assert.Equal(t, []string{"grok-4.6", "grok-4.5"}, res.Msg.GetModels())

	// Its own secret, and not the Anthropic one: a Grok turn and a Claude turn are handed
	// different credentials, and mixing them up would send one provider's key to the other.
	assert.Equal(t, fakeXAIKey, string(f.secrets.set[profiles.XAIKeySecret]))
	assert.NotContains(t, f.secrets.set, profiles.AnthropicKeySecret)
	assert.NotContains(t, f.log.String(), fakeXAIKey)
}

func TestGetSettingsReportsEveryProvider(t *testing.T) {
	f := newXAIFixture(t, nil)
	_, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderXAI, Key: fakeXAIKey,
	}))
	require.NoError(t, err)

	res, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetProviders(), 2)

	byName := map[string]*agentv1.ProviderSettings{}
	for _, p := range res.Msg.GetProviders() {
		byName[p.GetProvider()] = p
	}
	assert.True(t, byName[ProviderXAI].GetKeySet())
	assert.False(t, byName[ProviderAnthropic].GetKeySet())
	// The old single-provider field still answers about Anthropic, so a client written
	// before there was more than one keeps working.
	assert.Equal(t, ProviderAnthropic, res.Msg.GetProvider().GetProvider())
}

func TestAnUnknownProviderIsRefusedByName(t *testing.T) {
	f := newXAIFixture(t, nil)
	_, err := f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: "openai", Key: "sk-whatever",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "anthropic")
	assert.Contains(t, err.Error(), "xai")
	assert.Empty(t, f.secrets.set)
}

func TestListAgentsSaysWhichBackendsHaveACredential(t *testing.T) {
	f := newXAIFixture(t, nil)
	res, err := f.svc.ListAgents(loginCtx("alice"), connect.NewRequest(&agentv1.ListAgentsRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetAgents(), len(profiles.Backends))
	for _, a := range res.Msg.GetAgents() {
		assert.False(t, a.GetReady(), "nothing is stored yet")
		assert.NotEmpty(t, a.GetModels())
	}

	_, err = f.svc.SetProviderKey(loginCtx("alice"), connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderXAI, Key: fakeXAIKey,
	}))
	require.NoError(t, err)

	res, err = f.svc.ListAgents(loginCtx("alice"), connect.NewRequest(&agentv1.ListAgentsRequest{}))
	require.NoError(t, err)
	for _, a := range res.Msg.GetAgents() {
		assert.Equal(t, a.GetProvider() == ProviderXAI, a.GetReady(),
			"only the backend whose credential was stored is ready")
	}
}

// The subscription sign-in, end to end: a code, a poll that is still waiting, and the one
// poll that stores a credential.
func TestTheSubscriptionSignInStoresACredential(t *testing.T) {
	idp := newFakeIDP(t)
	f := newXAIFixture(t, idp)
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)
	assert.Equal(t, "WDJB-MJHT", start.Msg.GetUserCode())
	assert.NotEmpty(t, start.Msg.GetFlowId())
	// The device code stays here. A browser that is shown this response cannot finish the
	// sign-in on its own, which is the whole reason the flow is named rather than handed out.
	assert.NotContains(t, start.Msg.String(), "dev-code-1")
	assert.Empty(t, f.secrets.set, "starting a sign-in stores nothing")

	poll := func() *agentv1.PollProviderOAuthResponse {
		res, perr := f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
			Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
		}))
		require.NoError(t, perr)
		return res.Msg
	}

	assert.Equal(t, OAuthPending, poll().GetState())
	assert.Empty(t, f.secrets.set)

	idp.approved.Store(true)
	done := poll()
	require.Equal(t, OAuthDone, done.GetState())
	assert.Equal(t, AuthOAuth, done.GetProvider().GetAuthKind())
	assert.Equal(t, "someone@example.test", done.GetProvider().GetAccount())
	assert.True(t, done.GetProvider().GetRefreshable())
	assert.Empty(t, done.GetProvider().GetKeyHint(), "an access token has no hint worth showing")

	// The access token is the credential a turn gets, under the same secret an API key
	// would use: from here on the two auth kinds are one bearer token.
	assert.Equal(t, "access-1", string(f.secrets.set[profiles.XAIKeySecret]))
	// The refresh token is NOT a Podium secret and is never handed to a turn.
	assert.NotContains(t, f.secrets.set, profiles.XAIRefreshSecret)

	// A finished flow is over: the device code is single-use, so polling it again is an
	// expired flow rather than a second credential.
	assert.Equal(t, OAuthExpired, poll().GetState())
}

func TestPollingAFlowThatWasNeverStartedIsExpiredRatherThanAnError(t *testing.T) {
	f := newXAIFixture(t, newFakeIDP(t))
	res, err := f.svc.PollProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.PollProviderOAuthRequest{Provider: ProviderXAI, FlowId: "nope"}))
	require.NoError(t, err)
	assert.Equal(t, OAuthExpired, res.Msg.GetState())
}

func TestSlowDownBacksOff(t *testing.T) {
	idp := newFakeIDP(t)
	idp.tokenErr.Store("slow_down")
	f := newXAIFixture(t, idp)
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)

	first, err := f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
	}))
	require.NoError(t, err)
	require.Equal(t, OAuthSlowDown, first.Msg.GetState())
	assert.Greater(t, first.Msg.GetInterval(), start.Msg.GetInterval(),
		"RFC 8628 says back off, so the next poll has to be told to wait longer")
}

func TestSignInIsRefusedWithNoOAuthClientConfigured(t *testing.T) {
	f := newXAIFixture(t, nil)
	_, err := f.svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.StartProviderOAuthRequest{Provider: ProviderXAI}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	// The message has to name the knob: "not configured" on its own sends somebody hunting.
	assert.Contains(t, err.Error(), "PODIUM_AGENT_XAI_OAUTH_CLIENT_ID")
}

func TestAnthropicOffersNoSubscriptionSignIn(t *testing.T) {
	f := newXAIFixture(t, newFakeIDP(t))
	_, err := f.svc.StartProviderOAuth(loginCtx("alice"), connect.NewRequest(
		&agentv1.StartProviderOAuthRequest{Provider: ProviderAnthropic}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "takes an API key")
}

// The background pass is the only thing keeping a signed-in provider working past its first
// hour, and a rotated refresh token has to replace the one that was used.
func TestRefreshReplacesAnExpiringToken(t *testing.T) {
	idp := newFakeIDP(t)
	idp.approved.Store(true)
	f := newXAIFixture(t, idp)
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)
	_, err = f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
	}))
	require.NoError(t, err)
	require.Equal(t, "access-1", string(f.secrets.set[profiles.XAIKeySecret]))

	// An hour of life left is more than the lead, so nothing is due.
	f.svc.refreshOnce(ctx)
	assert.Zero(t, idp.refreshes.Load())
	assert.Equal(t, "access-1", string(f.secrets.set[profiles.XAIKeySecret]))

	// Bring the expiry inside the lead, the way the clock would.
	spec := providerSpecs[1]
	require.Equal(t, ProviderXAI, spec.name)
	row, ok, err := f.svc.providerRow(ctx, spec)
	require.NoError(t, err)
	require.True(t, ok)
	row.ExpiresAt = time.Now().UTC().Add(time.Minute)
	require.NoError(t, f.svc.store.PutSetting(ctx, providerSettingKey(ProviderXAI), row))

	f.svc.refreshOnce(ctx)
	assert.Equal(t, int32(1), idp.refreshes.Load())
	assert.Equal(t, "refresh-1", idp.lastRefresh.Load(), "the stored refresh token is what is spent")
	assert.Equal(t, "access-2", string(f.secrets.set[profiles.XAIKeySecret]),
		"the new access token is what the next turn is handed")

	next, ok, err := f.svc.providerRow(ctx, spec)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "refresh-2", next.RefreshToken,
		"a provider that rotates refresh tokens invalidates the old one, so it has to be replaced")
	assert.True(t, next.ExpiresAt.After(time.Now().Add(30*time.Minute)))
	assert.NotContains(t, f.log.String(), "refresh-1")
	assert.NotContains(t, f.log.String(), "access-2")
}

// Signing out has to take the refresh token with it, or the background pass would mint a new
// access token for a provider the operator has just disconnected.
func TestSigningOutRemovesTheRefreshTokenToo(t *testing.T) {
	idp := newFakeIDP(t)
	idp.approved.Store(true)
	f := newXAIFixture(t, idp)
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)
	_, err = f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
	}))
	require.NoError(t, err)

	_, err = f.svc.ClearProviderKey(ctx, connect.NewRequest(&agentv1.ClearProviderKeyRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)

	_, ok, err := f.svc.providerRow(ctx, providerSpecs[1])
	require.NoError(t, err)
	assert.False(t, ok, "the row carries the refresh token, so signing out has to delete it")

	before := idp.refreshes.Load()
	f.svc.refreshOnce(ctx)
	assert.Equal(t, before, idp.refreshes.Load(), "nothing is refreshed for a disconnected provider")
	assert.NotContains(t, f.secrets.set, profiles.XAIKeySecret)
}

// Pasting a key over a sign-in has to leave no refresh token behind: the background pass
// would otherwise overwrite the key the operator just saved.
func TestAPastedKeyReplacesASignInCompletely(t *testing.T) {
	idp := newFakeIDP(t)
	idp.approved.Store(true)
	f := newXAIFixture(t, idp)
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)
	_, err = f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
	}))
	require.NoError(t, err)

	_, err = f.svc.SetProviderKey(ctx, connect.NewRequest(&agentv1.SetProviderKeyRequest{
		Provider: ProviderXAI, Key: fakeXAIKey,
	}))
	require.NoError(t, err)

	row, ok, err := f.svc.providerRow(ctx, providerSpecs[1])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, AuthAPIKey, row.authKind())
	assert.Empty(t, row.RefreshToken)

	before := idp.refreshes.Load()
	f.svc.refreshOnce(ctx)
	assert.Equal(t, before, idp.refreshes.Load())
	assert.Equal(t, fakeXAIKey, string(f.secrets.set[profiles.XAIKeySecret]))
}

// A sign-in can succeed and still produce a token the API will not accept — xAI's OAuth
// surface has its own allow-list. Finding that out now, with the provider's own sentence,
// beats finding it out on the first turn.
func TestATokenTheAPIRefusesIsNotStored(t *testing.T) {
	idp := newFakeIDP(t)
	idp.approved.Store(true)
	// A fake xAI that accepts a different credential than the one the sign-in issues.
	api := fakeXAI(t, "xai-some-other-key")
	f := xaiFixture{secrets: newFakeSecrets(), log: &bytes.Buffer{}, idp: idp}
	f.svc = NewAgentService(AgentServiceOptions{
		Store:            newStore(t),
		Secrets:          f.secrets,
		XAIBaseURL:       api.URL,
		XAIOAuthIssuer:   idp.srv.URL,
		XAIOAuthClientID: "podium-test",
		HTTPClient:       idp.srv.Client(),
		Logger:           slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	ctx := loginCtx("alice")

	start, err := f.svc.StartProviderOAuth(ctx, connect.NewRequest(&agentv1.StartProviderOAuthRequest{
		Provider: ProviderXAI,
	}))
	require.NoError(t, err)
	_, err = f.svc.PollProviderOAuth(ctx, connect.NewRequest(&agentv1.PollProviderOAuthRequest{
		Provider: ProviderXAI, FlowId: start.Msg.GetFlowId(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "Paste an API key instead")
	assert.Empty(t, f.secrets.set, "an unusable token is never stored")

	var detail *agentv1.ProviderKeyError
	for _, d := range err.(*connect.Error).Details() { //nolint:errorlint // it is one
		if v, verr := d.Value(); verr == nil {
			if pk, isPK := v.(*agentv1.ProviderKeyError); isPK {
				detail = pk
			}
		}
	}
	require.NotNil(t, detail, "the provider's own words have to reach the operator")
	assert.Contains(t, detail.GetProviderMessage(), fakeXAIRefusal)
}
