package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAPIURL is GitHub's REST API. Overridable so a test can point at httptest and so
// an install behind an egress proxy can name one; it is not a "which GitHub" knob.
const DefaultAPIURL = "https://api.github.com"

const (
	apiVersion     = "2022-11-28"
	maxErrorBody   = 4 << 10
	tokenFreshness = 1 * time.Minute
	httpTimeout    = 30 * time.Second
	listPageSize   = 100
	listPageCap    = 20
)

// ErrNotInstalled is GET /repos/{o}/{r}/installation answering 404: the App is not on
// that repository. A Slack-started review still runs; GitHub posts are skipped.
var ErrNotInstalled = errors.New("github app is not installed on this repository")

// User is a GitHub account as the API reports it on a comment or sender.
type User struct {
	Login string `json:"login"`
	Type  string `json:"type"`
	ID    int64  `json:"id"`
}

// App is GET /app.
type App struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Comment is an issue comment or a pull-request review comment.
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	DiffHunk  string    `json:"diff_hunk"`
	InReplyTo int64     `json:"in_reply_to_id"`
	User      User      `json:"user"`
}

// Review is a submitted pull-request review.
type Review struct {
	ID          int64     `json:"id"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        User      `json:"user"`
}

// APIError is one GitHub refusal.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Client is the GitHub App REST client: JWT for /app, installation tokens for everything
// else. Hand-written because the operations are few and the JWT is ours to mint.
type Client struct {
	baseURL string
	appID   string
	key     *rsa.PrivateKey
	http    *http.Client
	now     func() time.Time

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

// ClientOptions is what NewClient needs.
type ClientOptions struct {
	BaseURL    string
	AppID      string
	PrivateKey *rsa.PrivateKey
	HTTPClient *http.Client
	Clock      func() time.Time
}

// NewClient validates the options. It makes no request.
func NewClient(opts ClientOptions) (*Client, error) {
	switch {
	case opts.AppID == "":
		return nil, errors.New("github: an app id is required")
	case opts.PrivateKey == nil:
		return nil, errors.New("github: a private key is required")
	}
	base := opts.BaseURL
	if base == "" {
		base = DefaultAPIURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("github: parse %q: %w", base, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("github: %q is not an absolute http:// or https:// URL", base)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: httpTimeout}
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		appID:   opts.AppID,
		key:     opts.PrivateKey,
		http:    httpClient,
		now:     now,
		tokens:  map[int64]cachedToken{},
	}, nil
}

// LoadPrivateKey reads a PEM PKCS#1 or PKCS#8 RSA key from path.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("github: read private key %s: %w", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("github: %s is not a PEM private key", path)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("github: parse PKCS#1 key %s: %w", path, err)
		}
		return key, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("github: parse PKCS#8 key %s: %w", path, err)
		}
		key, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("github: %s is not an RSA private key", path)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("github: %s has PEM type %q, want RSA PRIVATE KEY or PRIVATE KEY", path, block.Type)
	}
}

// App is GET /app, used at boot to learn the slug mentions are matched against.
func (c *Client) App(ctx context.Context) (App, error) {
	var app App
	if err := c.do(ctx, 0, http.MethodGet, "/app", nil, &app); err != nil {
		return App{}, err
	}
	if app.Slug == "" {
		return App{}, errors.New("github: GET /app returned no slug")
	}
	return app, nil
}

// RepoInstallation is GET /repos/{o}/{r}/installation. 404 is ErrNotInstalled.
func (c *Client) RepoInstallation(ctx context.Context, owner, repo string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do(ctx, 0, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/installation", nil, &out)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return 0, ErrNotInstalled
		}
		return 0, err
	}
	if out.ID == 0 {
		return 0, ErrNotInstalled
	}
	return out.ID, nil
}

// CreateIssueComment posts on the PR conversation tab.
func (c *Client) CreateIssueComment(ctx context.Context, installID int64, owner, repo string, number int, body string) (Comment, error) {
	var out Comment
	err := c.do(ctx, installID, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number),
		map[string]string{"body": body}, &out)
	return out, err
}

// EditIssueComment replaces a comment this App posted.
func (c *Client) EditIssueComment(ctx context.Context, installID int64, owner, repo string, commentID int64, body string) error {
	return c.do(ctx, installID, http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID),
		map[string]string{"body": body}, nil)
}

// CreateReviewReply posts in an inline review thread.
func (c *Client) CreateReviewReply(ctx context.Context, installID int64, owner, repo string, number int, inReplyTo int64, body string) (Comment, error) {
	var out Comment
	err := c.do(ctx, installID, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number),
		map[string]any{"body": body, "in_reply_to": inReplyTo}, &out)
	return out, err
}

// ListIssueComments pages the PR conversation comments, oldest first as GitHub returns them.
func (c *Client) ListIssueComments(ctx context.Context, installID int64, owner, repo string, number int) ([]Comment, error) {
	var all []Comment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	err := c.pages(ctx, installID, path, func(raw []byte) (int, error) {
		var page []Comment
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

// ListReviewComments pages the inline review comments.
func (c *Client) ListReviewComments(ctx context.Context, installID int64, owner, repo string, number int) ([]Comment, error) {
	var all []Comment
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number)
	err := c.pages(ctx, installID, path, func(raw []byte) (int, error) {
		var page []Comment
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

// ListReviews pages submitted reviews.
func (c *Client) ListReviews(ctx context.Context, installID int64, owner, repo string, number int) ([]Review, error) {
	var all []Review
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	err := c.pages(ctx, installID, path, func(raw []byte) (int, error) {
		var page []Review
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

func (c *Client) pages(ctx context.Context, installID int64, path string, consume func([]byte) (int, error)) error {
	for page := 1; page <= listPageCap; page++ {
		q := path + "?per_page=" + strconv.Itoa(listPageSize) + "&page=" + strconv.Itoa(page)
		var raw json.RawMessage
		if err := c.do(ctx, installID, http.MethodGet, q, nil, &raw); err != nil {
			return err
		}
		n, err := consume(raw)
		if err != nil {
			return err
		}
		if n < listPageSize {
			return nil
		}
	}
	return nil
}

func (c *Client) installationToken(ctx context.Context, installID int64) (string, error) {
	if installID <= 0 {
		return "", errors.New("github: an installation id is required")
	}
	now := c.now()
	c.mu.Lock()
	if tok, ok := c.tokens[installID]; ok && tok.expires.After(now.Add(tokenFreshness)) {
		c.mu.Unlock()
		return tok.token, nil
	}
	c.mu.Unlock()

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installID)
	if err := c.do(ctx, 0, http.MethodPost, path, map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("github: installation %d returned no token", installID)
	}
	c.mu.Lock()
	c.tokens[installID] = cachedToken{token: out.Token, expires: out.ExpiresAt}
	c.mu.Unlock()
	return out.Token, nil
}

func (c *Client) jwt() (string, error) {
	now := c.now().Add(-30 * time.Second)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"iat": now.Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.appID,
	})
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *Client) do(ctx context.Context, installID int64, method, path string, body, out any) error {
	var auth string
	var err error
	if installID > 0 {
		auth, err = c.installationToken(ctx, installID)
	} else {
		auth, err = c.jwt()
	}
	if err != nil {
		return err
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(raw)
		if len(raw) > maxErrorBody {
			msg = msg[:maxErrorBody] + "…"
		}
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(msg)}
	}
	if out == nil || len(raw) == 0 || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: decode %s %s: %w", method, path, err)
	}
	return nil
}
