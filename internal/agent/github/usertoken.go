package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultWebURL is where GitHub's OAuth endpoints live.
const DefaultWebURL = "https://github.com"

// UserToken is a person's connected account as Podium stores it, the value of their
// github.token secret. The access token lives eight hours and the refresh token six months;
// a refresh replaces both, and the old refresh token stops working.
type UserToken struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_token_expires_at,omitzero"`
}

// ErrRefreshRefused is GitHub refusing a refresh token: it expired, was already used, or the
// person revoked the App. Only reconnecting fixes it.
var ErrRefreshRefused = errors.New("github refused the refresh token")

type oauthTokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

// ParseUserToken reads GitHub's answer from /login/oauth/access_token, for a code or a
// refresh. GitHub reports a refused grant as 200 with an error field.
func ParseUserToken(res *http.Response, now time.Time) (UserToken, error) {
	var out oauthTokenResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return UserToken{}, fmt.Errorf("GitHub answered %s with something that is not JSON", res.Status)
	}
	switch {
	case out.Error == "bad_refresh_token":
		return UserToken{}, fmt.Errorf("%w: %s", ErrRefreshRefused, out.ErrorDescription)
	case out.Error != "":
		return UserToken{}, fmt.Errorf("GitHub refused: %s %s", out.Error, out.ErrorDescription)
	case res.StatusCode != http.StatusOK || out.AccessToken == "":
		return UserToken{}, fmt.Errorf("GitHub answered %s with no token", res.Status)
	}
	tok := UserToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}
	if out.ExpiresIn > 0 {
		tok.ExpiresAt = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshTokenExpiresIn > 0 {
		tok.RefreshExpiresAt = now.Add(time.Duration(out.RefreshTokenExpiresIn) * time.Second)
	}
	return tok, nil
}

// UserTokens narrows and refreshes a person's connected-account token, as the App's OAuth
// client. A turn never holds the person's own token: it gets one scoped to its playbook's
// repositories, which the conductor revokes when the turn is over.
type UserTokens struct {
	ClientID     string
	ClientSecret string
	// BaseURL overrides DefaultBaseURL, and WebURL DefaultWebURL. They exist for tests.
	BaseURL string
	WebURL  string
	// HTTPClient overrides a client with DefaultTimeout. It exists for tests.
	HTTPClient *http.Client
}

// Refresh trades a refresh token for a new access token and refresh token. The one it was
// given stops working, so the caller must store what comes back.
func (u UserTokens) Refresh(ctx context.Context, refreshToken string) (UserToken, error) {
	web := strings.TrimSuffix(u.WebURL, "/")
	if web == "" {
		web = DefaultWebURL
	}
	form := url.Values{
		"client_id":     {u.ClientID},
		"client_secret": {u.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, web+"/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return UserToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := u.client().Do(req)
	if err != nil {
		return UserToken{}, fmt.Errorf("GitHub could not be reached: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	return ParseUserToken(res, time.Now())
}

func (u UserTokens) client() *http.Client {
	if u.HTTPClient != nil {
		return u.HTTPClient
	}
	return &http.Client{Timeout: DefaultTimeout}
}

type scopedTokenRequest struct {
	AccessToken  string           `json:"access_token"`
	Target       string           `json:"target"`
	Repositories []string         `json:"repositories"`
	Permissions  tokenPermissions `json:"permissions"`
}

type scopedTokenResponse struct {
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Scope exchanges userToken for one that reaches only repos of owner, with contents and
// pull requests write. ExpiresAt is zero when GitHub gave the token no expiry, which is
// what a non-expiring user token yields: the caller has to revoke it. A user token GitHub
// no longer accepts is ErrNotFound.
func (u UserTokens) Scope(ctx context.Context, userToken, owner string, repos []string) (Token, error) {
	body, err := json.Marshal(scopedTokenRequest{
		AccessToken:  userToken,
		Target:       owner,
		Repositories: repos,
		Permissions:  tokenPermissions{Contents: "write", PullRequests: "write"},
	})
	if err != nil {
		return Token{}, err
	}
	var out scopedTokenResponse
	if err := u.do(ctx, http.MethodPost, "/token/scoped", body, &out); err != nil {
		return Token{}, err
	}
	if out.Token == "" {
		return Token{}, fmt.Errorf("github: scoping a user token returned no token")
	}
	tok := Token{Value: out.Token}
	if out.ExpiresAt != nil {
		tok.ExpiresAt = *out.ExpiresAt
	}
	return tok, nil
}

// Revoke ends one token. A token GitHub no longer knows is already revoked.
func (u UserTokens) Revoke(ctx context.Context, token string) error {
	body, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return err
	}
	err = u.do(ctx, http.MethodDelete, "/token", body, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (u UserTokens) do(ctx context.Context, method, path string, body []byte, out any) error {
	base := strings.TrimSuffix(u.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	client := u.client()
	full := base + "/applications/" + u.ClientID + path
	req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	// These endpoints authenticate the App's OAuth client, not the token in the body.
	req.SetBasicAuth(u.ClientID, u.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s /applications/…%s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		return (&Client{}).failure(method, "/applications/…"+path, res)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("github: decode %s %s: %w", method, path, err)
	}
	return nil
}
