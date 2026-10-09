//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

type mcpFixture struct {
	svc     *AgentService
	secrets *fakeSecrets
}

// newMcpFixture is a conductor with a real database and a fake control plane. The `general`
// playbook names `linear`, so the "which playbooks would this break" reporting has something
// to report.
func newMcpFixture(t *testing.T) mcpFixture {
	t.Helper()
	profile := fileProfile()
	playbook := profile.Playbooks["general"]
	playbook.MCPServers = []string{"linear"}
	profile.Playbooks["general"] = playbook

	secrets := newFakeSecrets()
	return mcpFixture{
		svc: NewAgentService(AgentServiceOptions{
			Store:    newStore(t),
			Secrets:  secrets,
			Model:    "claude-opus-5",
			Profiles: profiles.NewLive(profile),
		}),
		secrets: secrets,
	}
}

func linearReq(over func(*agentv1.McpServer)) *connect.Request[agentv1.CreateMcpServerRequest] {
	srv := &agentv1.McpServer{
		Name:        "linear",
		Url:         "https://mcp.linear.app/mcp",
		Description: "Issues and projects.",
		Enabled:     true,
	}
	if over != nil {
		over(srv)
	}
	return connect.NewRequest(&agentv1.CreateMcpServerRequest{Server: srv})
}

// The token goes to the control plane's encrypted store and four characters stay here.
// Nothing in the API ever answers with more than that.
func TestRegisteringAServerStoresItsTokenAsAPodiumSecret(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"

	got, err := f.svc.CreateMcpServer(t.Context(), req)
	require.NoError(t, err)
	srv := got.Msg.GetServer()
	assert.Equal(t, "linear", srv.GetName())
	assert.True(t, srv.GetTokenSet())
	assert.Equal(t, "6789", srv.GetTokenHint())
	assert.Equal(t, mcp.TokenSecret("linear"), srv.GetTokenSecret())
	assert.Equal(t, mcp.TokenEnv("linear"), srv.GetTokenEnv())
	// The playbook that names it, so an operator can see where the token will be spent.
	assert.Equal(t, []string{"general"}, srv.GetPlaybooks())

	// The value is in the control plane and in no response.
	assert.Equal(t, []byte("lin_api_0123456789"), f.secrets.set[mcp.TokenSecret("linear")])
	assert.NotContains(t, srv.String(), "lin_api_0123456789")

}

// A signed-in person's server stores its token as their secret, not a company secret.
func TestAPersonalServerStoresItsTokenOnThatPersonsSecrets(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"

	got, err := f.svc.CreateMcpServer(loginCtx("ada@acme.com"), req)
	require.NoError(t, err)
	srv := got.Msg.GetServer()
	assert.Equal(t, "ada@acme.com", srv.GetOwner())
	assert.True(t, srv.GetTokenSet())
	assert.Equal(t, mcp.PersonalTokenSecret("linear"), srv.GetTokenSecret())
	assert.Equal(t, []byte("lin_api_0123456789"), f.secrets.set[mcp.PersonalTokenSecret("linear")])
	assert.Equal(t, "ada@acme.com", f.secrets.owners[mcp.PersonalTokenSecret("linear")])
	assert.NotContains(t, f.secrets.set, mcp.TokenSecret("linear"))
	assert.NotContains(t, f.secrets.set, mcp.LegacyPersonalTokenSecret("ada@acme.com", "linear"))
}

// A server that needs no credential is a supported registration, not a half-finished one.
func TestAServerCanBeRegisteredWithNoToken(t *testing.T) {
	f := newMcpFixture(t)
	got, err := f.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Name = "wiki"
		s.Url = "http://wiki:9000/mcp"
	}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetServer().GetTokenSet())
	assert.Empty(t, got.Msg.GetServer().GetTokenHint())
	assert.NotContains(t, f.secrets.set, mcp.TokenSecret("wiki"))
}

func TestARegistrationIsHeldToItsRules(t *testing.T) {
	f := newMcpFixture(t)
	for _, tc := range []struct {
		name string
		over func(*agentv1.McpServer)
	}{
		{"no name", func(s *agentv1.McpServer) { s.Name = "" }},
		{"upper case", func(s *agentv1.McpServer) { s.Name = "Linear" }},
		{"reserved", func(s *agentv1.McpServer) { s.Name = "memory" }},
		{"no url", func(s *agentv1.McpServer) { s.Url = "" }},
		{"relative url", func(s *agentv1.McpServer) { s.Url = "mcp.linear.app" }},
		{"not http", func(s *agentv1.McpServer) { s.Url = "ftp://mcp.linear.app/mcp" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.CreateMcpServer(t.Context(), linearReq(tc.over))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestANameCanOnlyBeRegisteredOnce(t *testing.T) {
	f := newMcpFixture(t)
	_, err := f.svc.CreateMcpServer(t.Context(), linearReq(nil))
	require.NoError(t, err)

	_, err = f.svc.CreateMcpServer(t.Context(), linearReq(nil))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// Editing the address is not the same decision as replacing the credential, and an update
// does not touch one.
func TestUpdatingAServerLeavesItsTokenAlone(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"
	_, err := f.svc.CreateMcpServer(t.Context(), req)
	require.NoError(t, err)

	got, err := f.svc.UpdateMcpServer(t.Context(), connect.NewRequest(&agentv1.UpdateMcpServerRequest{
		Server: &agentv1.McpServer{
			Name:    "linear",
			Url:     "https://mcp.linear.app/sse",
			Enabled: false,
		},
	}))
	require.NoError(t, err)
	srv := got.Msg.GetServer()
	assert.Equal(t, "https://mcp.linear.app/sse", srv.GetUrl())
	assert.False(t, srv.GetEnabled())
	assert.True(t, srv.GetTokenSet())
	assert.Equal(t, "6789", srv.GetTokenHint())
}

func TestATokenIsOnlyStoredForAServerThatIsRegistered(t *testing.T) {
	f := newMcpFixture(t)
	_, err := f.svc.SetMcpServerToken(t.Context(), connect.NewRequest(&agentv1.SetMcpServerTokenRequest{
		Name: "linear", Token: "lin_api_0123456789",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	// No secret for a registration that does not exist: it would be a credential with
	// nothing to describe it and nothing to delete it.
	assert.Empty(t, f.secrets.set)
}

// Clearing leaves the registration and removes the credential, so a server can go back to
// being unauthenticated without being re-added.
func TestClearingATokenKeepsTheRegistration(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"
	_, err := f.svc.CreateMcpServer(t.Context(), req)
	require.NoError(t, err)

	got, err := f.svc.ClearMcpServerToken(t.Context(),
		connect.NewRequest(&agentv1.ClearMcpServerTokenRequest{Name: "linear"}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetServer().GetTokenSet())
	assert.Empty(t, got.Msg.GetServer().GetTokenHint())
	assert.Empty(t, got.Msg.GetServer().GetTokenSetBy())
	assert.NotContains(t, f.secrets.set, mcp.TokenSecret("linear"))

	list, err := f.svc.ListMcpServers(t.Context(), connect.NewRequest(&agentv1.ListMcpServersRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetServers(), 1)
	assert.Equal(t, "linear", list.Msg.GetServers()[0].GetName())
}

// A row claiming a credential the control plane no longer holds is the one state an operator
// cannot diagnose: the screen would say "connected" and every turn would disagree. So
// token_set is asked of the control plane, not read off the row.
func TestASecretRemovedWithTheCLIIsReportedAsGone(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"
	_, err := f.svc.CreateMcpServer(t.Context(), req)
	require.NoError(t, err)

	require.NoError(t, f.secrets.DeleteSecret(t.Context(), mcp.TokenSecret("linear")))

	list, err := f.svc.ListMcpServers(t.Context(), connect.NewRequest(&agentv1.ListMcpServersRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetServers(), 1)
	assert.False(t, list.Msg.GetServers()[0].GetTokenSet())
	assert.Empty(t, list.Msg.GetServers()[0].GetTokenHint())
}

// The secret goes with the row. A registration that is gone with a credential left behind is
// a credential nothing in the UI can see, name or remove.
func TestDeletingAServerRemovesItsToken(t *testing.T) {
	f := newMcpFixture(t)
	req := linearReq(nil)
	req.Msg.Token = "lin_api_0123456789"
	_, err := f.svc.CreateMcpServer(t.Context(), req)
	require.NoError(t, err)

	_, err = f.svc.DeleteMcpServer(t.Context(),
		connect.NewRequest(&agentv1.DeleteMcpServerRequest{Name: "linear"}))
	require.NoError(t, err)
	assert.NotContains(t, f.secrets.set, mcp.TokenSecret("linear"))
	assert.Contains(t, f.secrets.deleted, mcp.TokenSecret("linear"))

	list, err := f.svc.ListMcpServers(t.Context(), connect.NewRequest(&agentv1.ListMcpServersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetServers())

	_, err = f.svc.DeleteMcpServer(t.Context(),
		connect.NewRequest(&agentv1.DeleteMcpServerRequest{Name: "linear"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// newSignInFixture is a conductor whose HTTP client trusts the fake authorization server, so
// the sign-in handlers can be driven end to end against a real database.
func newSignInFixture(t *testing.T, f *fakeMcpServer) mcpFixture {
	t.Helper()
	profile := fileProfile()
	playbook := profile.Playbooks["general"]
	playbook.MCPServers = []string{"linear"}
	profile.Playbooks["general"] = playbook

	secrets := newFakeSecrets()
	return mcpFixture{
		svc: NewAgentService(AgentServiceOptions{
			Store:      newStore(t),
			Secrets:    secrets,
			Profiles:   profiles.NewLive(profile),
			HTTPClient: f.srv.Client(),
		}),
		secrets: secrets,
	}
}

const testRedirect = "https://podium.example.ts.net" + mcpCallbackPath

// The whole sign-in, end to end: discovery, dynamic registration, the authorize URL, the
// code exchange, and what is left in the database afterwards.
func TestSigningInToAnMcpServerEndToEnd(t *testing.T) {
	f := newFakeMcpServer(t)
	f.scopes = []string{"read", "write"}
	fx := newSignInFixture(t, f)

	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)

	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, start.Msg.GetFlowId())
	assert.NotEmpty(t, start.Msg.GetState())
	assert.Equal(t, f.srv.URL, start.Msg.GetIssuer())
	assert.Equal(t, "read write", start.Msg.GetScope())
	// The client was registered against THIS install's callback, which is the thing that
	// makes a redirect flow work on an install nobody configured in advance.
	assert.Equal(t, testRedirect, f.lastRedirect.Load())
	assert.Contains(t, start.Msg.GetAuthorizeUrl(), "code_challenge_method=S256")

	done, err := fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)
	srv := done.Msg.GetServer()
	assert.Equal(t, mcp.AuthOAuth, srv.GetAuthKind())
	assert.True(t, srv.GetTokenSet())
	assert.True(t, srv.GetRefreshable())
	// An access token is not a thing to show four characters of.
	assert.Empty(t, srv.GetTokenHint())
	assert.True(t, srv.GetExpiresAt().IsValid())

	// The access token went to the control plane's secret store — the same secret a pasted
	// token uses, which is why nothing downstream can tell the two apart.
	assert.Equal(t, []byte("mcp-access-authorization_code"),
		fx.secrets.set[mcp.TokenSecret("linear")])

	// The refresh token is in the secret store too, and the row only says there is one.
	row, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	require.NotNil(t, row.OAuth)
	assert.Empty(t, row.OAuth.RefreshToken)
	assert.True(t, row.OAuth.Refreshable)
	assert.Equal(t, "mcp-refresh-2", mcpRefreshIn(t, fx.secrets, "linear"))
	assert.Equal(t, "client-abc", row.OAuth.ClientID)
	assert.Equal(t, f.url(), row.OAuth.Resource)
	assert.NotContains(t, srv.String(), "mcp-refresh-2")
	assert.NotContains(t, srv.String(), "mcp-access-authorization_code")
}

// A callback that does not carry the flow's own state is refused, and nothing is exchanged.
func TestASignInCallbackWithTheWrongStateIsRefused(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)

	_, err = fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: "not-the-state",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Empty(t, fx.secrets.set)
}

// An authorization code is single-use at the authorization server, so a flow is spent on the
// first attempt whatever the outcome — a second try could only ever be refused, and would
// read as "the sign-in failed" when it had worked.
func TestASignInFlowIsUsableOnlyOnce(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	req := connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	})
	_, err = fx.svc.CompleteMcpOAuth(t.Context(), req)
	require.NoError(t, err)

	_, err = fx.svc.CompleteMcpOAuth(t.Context(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err))
}

// A redirect_uri the browser has no business nominating is refused before anything is
// discovered or registered.
func TestASignInRefusesARedirectSomewhereElse(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)

	_, err = fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: "https://evil.test/steal",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, int32(0), f.registrations.Load())
}

// A server advertising no OAuth is not a broken server: it is one whose credential has to be
// pasted, and the code says which of the two it is.
func TestASignInToAServerWithNoOAuthIsUnimplemented(t *testing.T) {
	bare := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bare.Close)
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = bare.URL + "/mcp"
	}))
	require.NoError(t, err)

	_, err = fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.Error(t, err)
	assert.Contains(t,
		[]connect.Code{connect.CodeUnimplemented, connect.CodeFailedPrecondition},
		connect.CodeOf(err))
}

// Pasting a token over a sign-in has to take the sign-in with it. Leaving the refresh token
// behind would let the background pass overwrite the token somebody just pasted.
func TestPastingATokenOverASignInClearsTheSignIn(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	_, err = fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)

	got, err := fx.svc.SetMcpServerToken(t.Context(), connect.NewRequest(&agentv1.SetMcpServerTokenRequest{
		Name: "linear", Token: "lin_api_0123456789",
	}))
	require.NoError(t, err)
	assert.Equal(t, mcp.AuthToken, got.Msg.GetServer().GetAuthKind())
	assert.Equal(t, "6789", got.Msg.GetServer().GetTokenHint())
	assert.False(t, got.Msg.GetServer().GetRefreshable())

	row, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	assert.Nil(t, row.OAuth)
}

// Signing out removes the credential AND the refresh token. Leaving the latter would let the
// background pass mint a new access token for a server the operator just disconnected.
func TestClearingASignInRemovesTheRefreshToken(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	_, err = fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)

	got, err := fx.svc.ClearMcpServerToken(t.Context(),
		connect.NewRequest(&agentv1.ClearMcpServerTokenRequest{Name: "linear"}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetServer().GetTokenSet())
	assert.Empty(t, got.Msg.GetServer().GetAuthKind())

	row, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	assert.Nil(t, row.OAuth)
	assert.NotContains(t, fx.secrets.set, mcp.TokenSecret("linear"))
	assert.NotContains(t, fx.secrets.set, mcp.OAuthSecret(mcp.Server{Name: "linear"}))
}

// mcpRefreshIn is the refresh token a bot server's sign-in keeps in the secret store.
func mcpRefreshIn(t *testing.T, f *fakeSecrets, name string) string {
	t.Helper()
	var got mcpOAuthSecrets
	require.NoError(t, json.Unmarshal(f.set[mcp.OAuthSecret(mcp.Server{Name: name})], &got))
	return got.RefreshToken
}

// The background pass is what stops a token issued for an hour becoming a turn failing an
// hour later. A rotated refresh token has to be kept, or the next refresh fails.
func TestTheBackgroundPassRefreshesASignInAndKeepsTheRotatedToken(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	_, err = fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)

	// A token with an hour on it is not due: the lead is 45 minutes, so nothing happens.
	before := fx.secrets.versions[mcp.TokenSecret("linear")]
	fx.svc.refreshMcpOnce(t.Context())
	assert.Equal(t, before, fx.secrets.versions[mcp.TokenSecret("linear")])

	// Bring the expiry inside the lead and it is.
	row, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	o := *row.OAuth
	o.ExpiresAt = time.Now().UTC().Add(time.Minute)
	require.NoError(t, fx.svc.store.RefreshMcpServerOAuth(t.Context(), "", "linear", row.TokenSecretVersion, o))

	fx.svc.refreshMcpOnce(t.Context())
	assert.Equal(t, []byte("mcp-access-refresh_token"), fx.secrets.set[mcp.TokenSecret("linear")])

	after, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	require.NotNil(t, after.OAuth)
	assert.Empty(t, after.OAuth.RefreshToken)
	assert.Equal(t, "mcp-refresh-2", mcpRefreshIn(t, fx.secrets, "linear"))
	assert.True(t, after.OAuth.ExpiresAt.After(time.Now().UTC().Add(30*time.Minute)))
	// The human who signed in is still the human who signed in: a background pass does not
	// write its own name over theirs.
	assert.Equal(t, after.TokenSetBy, row.TokenSetBy)
}

// A refresh that fails keeps the stored token: one with thirty minutes left on it is more
// use than none, and the next tick tries again.
func TestAFailedRefreshKeepsTheStoredToken(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	_, err := fx.svc.CreateMcpServer(t.Context(), linearReq(func(s *agentv1.McpServer) {
		s.Url = f.url()
	}))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(t.Context(), connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	_, err = fx.svc.CompleteMcpOAuth(t.Context(), connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)

	row, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	o := *row.OAuth
	o.ExpiresAt = time.Now().UTC().Add(time.Minute)
	require.NoError(t, fx.svc.store.RefreshMcpServerOAuth(t.Context(), "", "linear", row.TokenSecretVersion, o))

	f.tokenErr = "invalid_grant"
	fx.svc.refreshMcpOnce(t.Context())

	assert.Equal(t, []byte("mcp-access-authorization_code"), fx.secrets.set[mcp.TokenSecret("linear")])
	after, err := fx.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	require.NotNil(t, after.OAuth)
	assert.Equal(t, "mcp-refresh-2", mcpRefreshIn(t, fx.secrets, "linear"))
}

// A sign-in stored before the secret store answered the conductor keeps its refresh token and
// client secret in the row. The start-up pass moves them.
func TestAnMcpSignInInClearIsMovedIntoTheSecretStore(t *testing.T) {
	f := newMcpFixture(t)
	_, err := f.svc.CreateMcpServer(t.Context(), linearReq(nil))
	require.NoError(t, err)
	require.NoError(t, f.svc.store.SetMcpServerOAuth(t.Context(), "", "linear", "ada", 1, mcp.OAuth{
		Issuer: "https://auth.example", ClientID: "client-abc", ClientSecret: "shhh", RefreshToken: "refresh-old",
	}))

	f.svc.MoveCredentialsIntoStore(t.Context())

	row, err := f.svc.store.McpServer(t.Context(), "linear")
	require.NoError(t, err)
	require.NotNil(t, row.OAuth)
	assert.Empty(t, row.OAuth.RefreshToken)
	assert.Empty(t, row.OAuth.ClientSecret)
	assert.True(t, row.Refreshable())
	assert.Equal(t, "refresh-old", mcpRefreshIn(t, f.secrets, "linear"))

	var got mcpOAuthSecrets
	require.NoError(t, json.Unmarshal(f.secrets.set[mcp.OAuthSecret(mcp.Server{Name: "linear"})], &got))
	assert.Equal(t, "shhh", got.ClientSecret)
}

// A person signing in to their own server keeps the sign-in's secrets on their own list,
// not as a company secret, and the refresh pass and sign-out find them there.
func TestAPersonsSignInKeepsItsSecretsOnTheirList(t *testing.T) {
	f := newFakeMcpServer(t)
	fx := newSignInFixture(t, f)
	ada := loginCtx("ada@acme.com")
	_, err := fx.svc.CreateMcpServer(ada, linearReq(func(s *agentv1.McpServer) { s.Url = f.url() }))
	require.NoError(t, err)
	start, err := fx.svc.StartMcpOAuth(ada, connect.NewRequest(&agentv1.StartMcpOAuthRequest{
		Name: "linear", RedirectUri: testRedirect,
	}))
	require.NoError(t, err)
	_, err = fx.svc.CompleteMcpOAuth(ada, connect.NewRequest(&agentv1.CompleteMcpOAuthRequest{
		FlowId: start.Msg.GetFlowId(), Code: "code-1", State: start.Msg.GetState(),
	}))
	require.NoError(t, err)

	personal := mcp.OAuthSecret(mcp.Server{Owner: "ada@acme.com", Name: "linear"})
	assert.Equal(t, "mcp.linear.oauth", personal)
	assert.Equal(t, "ada@acme.com", fx.secrets.owners[personal], "the sign-in is Ada's secret")
	assert.NotContains(t, fx.secrets.set, mcp.OAuthSecret(mcp.Server{Name: "linear"}), "and not a company one")

	row, err := fx.svc.store.McpServerOwned(t.Context(), "ada@acme.com", "linear")
	require.NoError(t, err)
	assert.Empty(t, row.OAuth.RefreshToken)
	o := *row.OAuth
	o.ExpiresAt = time.Now().UTC().Add(time.Minute)
	require.NoError(t, fx.svc.store.RefreshMcpServerOAuth(t.Context(), "ada@acme.com", "linear", row.TokenSecretVersion, o))
	fx.svc.refreshMcpOnce(t.Context())
	assert.Equal(t, []byte("mcp-access-refresh_token"), fx.secrets.set[mcp.PersonalTokenSecret("linear")],
		"the refresh pass read Ada's refresh token from her list")

	_, err = fx.svc.ClearMcpServerToken(ada, connect.NewRequest(&agentv1.ClearMcpServerTokenRequest{Name: "linear"}))
	require.NoError(t, err)
	assert.NotContains(t, fx.secrets.set, personal)
}
