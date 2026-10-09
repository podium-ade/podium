package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UserTokens narrows a person's connected-account token, as the App's OAuth client. A turn
// never holds the person's own token: it gets one scoped to its playbook's repositories,
// which the conductor revokes when the turn is over.
type UserTokens struct {
	ClientID     string
	ClientSecret string
	// BaseURL overrides DefaultBaseURL. It exists for tests.
	BaseURL string
	// HTTPClient overrides a client with DefaultTimeout. It exists for tests.
	HTTPClient *http.Client
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
	client := u.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
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
