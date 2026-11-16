package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AppScopes are what the agent app asks for. read and write are the GraphQL API;
// app:assignable lets an issue be delegated to the app and app:mentionable lets it be
// @mentioned. Linear takes them comma-separated.
const AppScopes = "read,write,app:assignable,app:mentionable"

// tokenExpiryMargin is how long before its expiry a token is replaced. A token is fetched
// for each request, so the margin only has to outlast one request and a clock that is a
// little off. Linear's tokens last 30 days, so ten minutes costs nothing.
const tokenExpiryMargin = 10 * time.Minute

// Token is an OAuth access token and when it stops working. A zero ExpiresAt is a token
// whose lifetime Linear did not say, which is used until Linear refuses it.
type Token struct {
	// AccessToken is SENSITIVE: never log it.
	AccessToken string
	ExpiresAt   time.Time
}

// TokenError is the token endpoint refusing the client credentials: a wrong client id or
// secret, or an app whose "Client credentials" switch is off. Code and Description are
// Linear's error and error_description, which name the problem without the secret.
type TokenError struct {
	Status      int
	Code        string
	Description string
}

func (e *TokenError) Error() string {
	switch {
	case e.Code != "" && e.Description != "":
		return fmt.Sprintf("linear: the token endpoint refused the client credentials: %s: %s (HTTP %d)",
			e.Code, e.Description, e.Status)
	case e.Code != "":
		return fmt.Sprintf("linear: the token endpoint refused the client credentials: %s (HTTP %d)",
			e.Code, e.Status)
	}
	return fmt.Sprintf("linear: the token endpoint refused the client credentials: %s (HTTP %d)",
		e.Description, e.Status)
}

// TokenSourceOptions is what NewTokenSource needs.
type TokenSourceOptions struct {
	// TokenURL is the OAuth token endpoint. Required, like ClientOptions.Endpoint, so a
	// test can point it at a stub.
	TokenURL string
	// ClientID is the OAuth application's client id. Required.
	ClientID string
	// ClientSecret is the OAuth application's client secret. Required. SENSITIVE.
	ClientSecret string
	// HTTPClient lets a test point at an httptest server. Nil means one with DefaultTimeout.
	HTTPClient *http.Client
	// Load, when set, reads a token saved by Store, perhaps by another conductor on the
	// same database. A zero Token with a nil error means there is none. Several conductors
	// share one token this way instead of each minting their own.
	Load func(ctx context.Context) (Token, error)
	// Store, when set, saves a token just minted so Load can find it.
	Store func(ctx context.Context, tok Token) error
	// Logger hears about a Load or Store that failed. Neither stops a token from being
	// used: a token minted without being saved still works, and the next conductor mints
	// its own. Nil discards.
	Logger *slog.Logger
}

// TokenSource is an app-actor access token from Linear's client-credentials grant. It
// mints one when it has none, keeps it in memory, and mints again shortly before it
// expires. Linear gives no refresh token for this grant: a new token is a new grant, which
// needs only the client id and secret.
type TokenSource struct {
	tokenURL     string
	clientID     string
	clientSecret string
	http         *http.Client
	load         func(context.Context) (Token, error)
	store        func(context.Context, Token) error
	logger       *slog.Logger
	now          func() time.Time

	// mu is held across a mint, so callers that all find the token stale wait for one
	// mint rather than each sending their own.
	mu  sync.Mutex
	tok Token
	// refused is the last token Invalidate was told Linear refused. Load may still return
	// it, from a conductor that has not noticed yet, and it must not be used again.
	refused string
}

// NewTokenSource validates the options. It makes no request: the first Token does.
func NewTokenSource(opts TokenSourceOptions) (*TokenSource, error) {
	switch {
	case opts.TokenURL == "":
		return nil, errors.New("linear: a token endpoint is required")
	case opts.ClientID == "":
		return nil, errors.New("linear: a client id is required")
	case opts.ClientSecret == "":
		return nil, errors.New("linear: a client secret is required")
	}
	u, err := url.Parse(opts.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("linear: parse %q: %w", opts.TokenURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("linear: %q is not an absolute http:// or https:// URL", opts.TokenURL)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &TokenSource{
		tokenURL:     opts.TokenURL,
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		http:         client,
		load:         opts.Load,
		store:        opts.Store,
		logger:       logger,
		now:          time.Now,
	}, nil
}

// Token is an access token that has a while left to run. It is the one in memory when
// that is still good, then a saved one, and only then a new one from Linear.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usable(s.tok) {
		return s.tok.AccessToken, nil
	}
	if s.load != nil {
		saved, err := s.load(ctx)
		switch {
		case err != nil:
			s.logger.WarnContext(ctx, "linear: the saved access token could not be read; minting a new one",
				"error", err)
		case s.usable(saved):
			s.tok = saved
			return saved.AccessToken, nil
		}
	}
	tok, err := s.mint(ctx)
	if err != nil {
		return "", err
	}
	s.tok = tok
	if s.store != nil {
		if err := s.store(ctx, tok); err != nil {
			s.logger.WarnContext(ctx, "linear: the new access token could not be saved; this conductor "+
				"keeps it in memory", "error", err)
		}
	}
	return tok.AccessToken, nil
}

// Invalidate forgets token after Linear refused it, so the next Token mints a new one.
// It names the token rather than dropping whatever is current: two requests that failed
// on the same token must not throw away the one the first of them minted afterwards.
func (s *TokenSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = token
	if s.tok.AccessToken == token {
		s.tok = Token{}
	}
}

// usable reports whether tok can be sent: it is set, it is not the one Linear refused, and
// it is not about to expire.
func (s *TokenSource) usable(tok Token) bool {
	if tok.AccessToken == "" || tok.AccessToken == s.refused {
		return false
	}
	return tok.ExpiresAt.IsZero() || s.now().Add(tokenExpiryMargin).Before(tok.ExpiresAt)
}

// tokenResponse is the token endpoint's answer, success or failure.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// mint asks Linear for a new token. The client id and secret go in HTTP Basic auth, which
// is how the OAuth spec says a confidential client authenticates, and never in the body
// or the URL where a proxy log would keep them.
func (s *TokenSource) mint(ctx context.Context) (Token, error) {
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {AppScopes},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("linear: build the token request: %w", err)
	}
	req.SetBasicAuth(s.clientID, s.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	issued := s.now()
	res, err := s.http.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("linear: request an access token: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("linear: read the token response: %w", err)
	}

	var out tokenResponse
	jsonErr := json.Unmarshal(body, &out)
	if res.StatusCode/100 != 2 {
		if jsonErr != nil || (out.Error == "" && out.ErrorDescription == "") {
			return Token{}, &TokenError{Status: res.StatusCode, Description: quote(body)}
		}
		return Token{}, &TokenError{Status: res.StatusCode, Code: out.Error, Description: out.ErrorDescription}
	}
	if jsonErr != nil {
		return Token{}, fmt.Errorf("linear: the token response is not JSON (HTTP %d): %s", res.StatusCode, quote(body))
	}
	if out.AccessToken == "" {
		return Token{}, fmt.Errorf("linear: the token response has no access token (HTTP %d)", res.StatusCode)
	}
	// Anything but Bearer would need a different Authorization header, and Linear issues
	// no other kind. Saying so beats a 401 on every request.
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return Token{}, fmt.Errorf("linear: the token endpoint issued a %q token, not a bearer token", out.TokenType)
	}
	tok := Token{AccessToken: out.AccessToken}
	if out.ExpiresIn > 0 {
		// From when the request was sent, not when it was answered: a slow answer then
		// makes the token look a little older than it is, never younger.
		tok.ExpiresAt = issued.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return tok, nil
}
