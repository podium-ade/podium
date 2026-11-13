package linear

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Obvious fixtures. Nothing here is the shape of a real credential beyond its prefix.
const (
	fakeClientID     = "not-a-real-client-id"
	fakeClientSecret = "not-a-real-client-secret"
	appUserID        = "app-user-0001"
	orgID            = "org-0001"
)

func newTokenSource(t *testing.T, s *stub, opts TokenSourceOptions) (*TokenSource, *fakeClock) {
	t.Helper()
	opts.TokenURL = s.tokenURL()
	if opts.ClientID == "" {
		opts.ClientID = fakeClientID
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = fakeClientSecret
	}
	ts, err := NewTokenSource(opts)
	require.NoError(t, err)
	clock := &fakeClock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	ts.now = clock.Now
	return ts, clock
}

func appIdentityData() map[string]any {
	return map[string]any{
		"viewer": map[string]any{
			"id": appUserID, "name": "podium", "displayName": "Podium", "app": true,
		},
		"organization": map[string]any{"id": orgID, "name": "Acme", "urlKey": "acme"},
	}
}

// TestATokenIsMintedWithClientCredentials pins the request Linear's token endpoint expects:
// the client id and secret in HTTP Basic auth, never in the form, and the four agent
// scopes comma-separated.
func TestATokenIsMintedWithClientCredentials(t *testing.T) {
	s := newStub(t)
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})

	tok, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lin_oauth_stub_1", tok)

	grants := s.seenGrants()
	require.Len(t, grants, 1)
	assert.True(t, grants[0].BasicAuth)
	assert.Equal(t, fakeClientID, grants[0].ClientID)
	assert.Equal(t, fakeClientSecret, grants[0].ClientSecret)
	assert.Equal(t, "client_credentials", grants[0].Form.Get("grant_type"))
	assert.Equal(t, "read,write,app:assignable,app:mentionable", grants[0].Form.Get("scope"))
	assert.NotContains(t, grants[0].Form.Encode(), fakeClientSecret)
}

func TestATokenIsKeptUntilShortlyBeforeItExpires(t *testing.T) {
	s := newStub(t)
	s.token = func(n int) stubToken {
		return stubToken{AccessToken: fmt.Sprintf("lin_oauth_stub_%d", n), ExpiresIn: 3600}
	}
	ts, clock := newTokenSource(t, s, TokenSourceOptions{})
	ctx := context.Background()

	first, err := ts.Token(ctx)
	require.NoError(t, err)
	clock.advance(45 * time.Minute)
	again, err := ts.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, first, again, "a token with a while left to run is reused")
	assert.Len(t, s.seenGrants(), 1)

	clock.advance(6 * time.Minute) // nine minutes left, inside the margin
	next, err := ts.Token(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first, next)
	assert.Len(t, s.seenGrants(), 2)
}

func TestAnInvalidatedTokenIsReplaced(t *testing.T) {
	s := newStub(t)
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})
	ctx := context.Background()

	first, err := ts.Token(ctx)
	require.NoError(t, err)
	ts.Invalidate("some-older-token")
	same, err := ts.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, first, same, "invalidating a token that is no longer current keeps the current one")

	ts.Invalidate(first)
	next, err := ts.Token(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first, next)
	assert.Len(t, s.seenGrants(), 2)
}

// TestASavedTokenIsSharedAndARefusedOneIsNotReused is the several-conductors case: a token
// another conductor saved is used without minting, and once Linear refuses it, the saved
// copy is passed over rather than handed out again.
func TestASavedTokenIsSharedAndARefusedOneIsNotReused(t *testing.T) {
	s := newStub(t)
	var mu sync.Mutex
	saved := Token{AccessToken: "lin_oauth_saved", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	var stored []Token
	ts, _ := newTokenSource(t, s, TokenSourceOptions{
		Load: func(context.Context) (Token, error) {
			mu.Lock()
			defer mu.Unlock()
			return saved, nil
		},
		Store: func(_ context.Context, tok Token) error {
			mu.Lock()
			defer mu.Unlock()
			stored = append(stored, tok)
			return nil
		},
	})
	ctx := context.Background()

	tok, err := ts.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, "lin_oauth_saved", tok)
	assert.Empty(t, s.seenGrants(), "a saved token is used without minting")

	ts.Invalidate(tok)
	tok, err = ts.Token(ctx)
	require.NoError(t, err)
	assert.Equal(t, "lin_oauth_stub_1", tok)
	require.Len(t, stored, 1)
	assert.Equal(t, "lin_oauth_stub_1", stored[0].AccessToken)
	assert.Equal(t, time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).Add(2591999*time.Second), stored[0].ExpiresAt)
}

func TestAStaleOrUnreadableSavedTokenIsReplaced(t *testing.T) {
	s := newStub(t)
	ts, _ := newTokenSource(t, s, TokenSourceOptions{
		Load: func(context.Context) (Token, error) {
			return Token{AccessToken: "lin_oauth_old", ExpiresAt: time.Date(2026, 11, 12, 9, 5, 0, 0, time.UTC)}, nil
		},
		Store: func(context.Context, Token) error { return errors.New("the vault is down") },
	})
	tok, err := ts.Token(context.Background())
	require.NoError(t, err, "a token that could not be saved still works")
	assert.Equal(t, "lin_oauth_stub_1", tok)

	broken, _ := newTokenSource(t, s, TokenSourceOptions{
		Load: func(context.Context) (Token, error) { return Token{}, errors.New("the vault is down") },
	})
	tok, err = broken.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lin_oauth_stub_2", tok)
}

// TestRefusedClientCredentialsAreATokenError checks the error names Linear's reason and
// never the secret.
func TestRefusedClientCredentialsAreATokenError(t *testing.T) {
	s := newStub(t)
	s.token = func(int) stubToken {
		return stubToken{Error: "invalid_client", Description: "Client authentication failed", Status: http.StatusUnauthorized}
	}
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})

	_, err := ts.Token(context.Background())
	var tokErr *TokenError
	require.ErrorAs(t, err, &tokErr)
	assert.Equal(t, "invalid_client", tokErr.Code)
	assert.Equal(t, http.StatusUnauthorized, tokErr.Status)
	assert.Contains(t, err.Error(), "Client authentication failed")
	assert.NotContains(t, err.Error(), fakeClientSecret)
}

func TestATokenSourceNeedsItsOptions(t *testing.T) {
	for name, opts := range map[string]TokenSourceOptions{
		"token url": {ClientID: "id", ClientSecret: "secret"},
		"client id": {TokenURL: "https://api.linear.app/oauth/token", ClientSecret: "secret"},
		"secret":    {TokenURL: "https://api.linear.app/oauth/token", ClientID: "id"},
		"relative":  {TokenURL: "/oauth/token", ClientID: "id", ClientSecret: "secret"},
	} {
		_, err := NewTokenSource(opts)
		assert.Error(t, err, name)
	}
}

// TestAnAppClientSendsABearerToken is the other half of the bare-key test: an OAuth token
// goes out as Bearer.
func TestAnAppClientSendsABearerToken(t *testing.T) {
	s := newStub(t)
	s.reply("PodiumAppIdentity", appIdentityData())
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})
	c, err := NewClient(ClientOptions{Endpoint: s.endpoint(), Tokens: ts})
	require.NoError(t, err)

	id, err := c.AppIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, AppIdentity{
		UserID: appUserID, Name: "podium", DisplayName: "Podium", App: true,
		OrganizationID: orgID, OrganizationName: "Acme", OrganizationURLKey: "acme",
	}, id)
	assert.Equal(t, []string{"Bearer lin_oauth_stub_1"}, s.seenAuths())
}

// TestARefusedTokenIsReplacedAndTheCallSentOnceMore covers a token revoked before its 30
// days were up: the call is sent again with a new token, once.
func TestARefusedTokenIsReplacedAndTheCallSentOnceMore(t *testing.T) {
	s := newStub(t)
	s.on("PodiumAppIdentity", func(map[string]any) stubReply {
		if strings.HasSuffix(s.seenAuths()[len(s.seenAuths())-1], "_1") {
			return stubReply{Message: "Authentication required, not authenticated",
				Code: CodeAuthentication, Status: http.StatusUnauthorized}
		}
		return stubReply{Data: appIdentityData()}
	})
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})
	c, err := NewClient(ClientOptions{Endpoint: s.endpoint(), Tokens: ts})
	require.NoError(t, err)

	_, err = c.AppIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer lin_oauth_stub_1", "Bearer lin_oauth_stub_2"}, s.seenAuths())
	assert.Len(t, s.seenGrants(), 2)
}

func TestATokenRefusedTwiceIsAnError(t *testing.T) {
	s := newStub(t)
	s.on("PodiumAppIdentity", func(map[string]any) stubReply {
		return stubReply{Message: "Authentication required, not authenticated",
			Code: CodeAuthentication, Status: http.StatusUnauthorized}
	})
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})
	c, err := NewClient(ClientOptions{Endpoint: s.endpoint(), Tokens: ts})
	require.NoError(t, err)

	_, err = c.AppIdentity(context.Background())
	var gqlErr *GraphQLError
	require.ErrorAs(t, err, &gqlErr)
	assert.True(t, gqlErr.Unauthorized())
	assert.Len(t, s.seenAuths(), 2, "one retry, not a loop")
}

// TestAnAPIKeyClientNeverRetries keeps the poller exactly as it was: a 401 on the
// personal key is reported, not retried, and no token is minted.
func TestAnAPIKeyClientNeverRetries(t *testing.T) {
	s := newStub(t)
	s.on("PodiumViewer", func(map[string]any) stubReply {
		return stubReply{Message: "Authentication required, not authenticated",
			Code: CodeAuthentication, Status: http.StatusUnauthorized}
	})
	c, err := NewClient(ClientOptions{Endpoint: s.endpoint(), APIKey: fakeAPIKey})
	require.NoError(t, err)
	_, err = c.Viewer(context.Background())
	require.Error(t, err)
	assert.Len(t, s.seenAuths(), 1)
	assert.Empty(t, s.seenGrants())
}

func TestAClientSignsInOneWay(t *testing.T) {
	s := newStub(t)
	ts, _ := newTokenSource(t, s, TokenSourceOptions{})
	_, err := NewClient(ClientOptions{Endpoint: s.endpoint(), APIKey: fakeAPIKey, Tokens: ts})
	require.ErrorContains(t, err, "both set")
	_, err = NewClient(ClientOptions{Endpoint: s.endpoint()})
	require.ErrorContains(t, err, "API key is required")
}
