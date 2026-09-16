package ghreview

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotInstalled is GET /repos/{o}/{r}/installation answering 404: the App is not on
// that repository. A Slack-started review still runs; GitHub posts are skipped.
var ErrNotInstalled = errors.New("github app is not installed on this repository")

const (
	defaultAPIURL  = "https://api.github.com"
	apiVersion     = "2022-11-28"
	restErrorBody  = 4 << 10
	tokenFreshness = 1 * time.Minute
	httpTimeout    = 30 * time.Second
	listPageSize   = 100
	listPageCap    = 20
)

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

// restClient is the review source's GitHub REST client: JWT for /app, installation
// tokens for comments. The minting Client in github.go is the one turns clone with.
type restClient struct {
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

type restOptions struct {
	BaseURL    string
	AppID      string
	PrivateKey *rsa.PrivateKey
	HTTPClient *http.Client
	Clock      func() time.Time
}

func newRest(opts restOptions) (*restClient, error) {
	switch {
	case opts.AppID == "":
		return nil, errors.New("github: an app id is required")
	case opts.PrivateKey == nil:
		return nil, errors.New("github: a private key is required")
	}
	base := opts.BaseURL
	if base == "" {
		base = defaultAPIURL
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
	return &restClient{
		baseURL: strings.TrimRight(base, "/"),
		appID:   opts.AppID,
		key:     opts.PrivateKey,
		http:    httpClient,
		now:     now,
		tokens:  map[int64]cachedToken{},
	}, nil
}

// App is GET /app, used at boot to learn the slug mentions are matched against.
func (c *restClient) App(ctx context.Context) (App, error) {
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
func (c *restClient) RepoInstallation(ctx context.Context, owner, repo string) (int64, error) {
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
func (c *restClient) CreateIssueComment(ctx context.Context, installID int64, owner, repo string, number int, body string) (Comment, error) {
	var out Comment
	err := c.do(ctx, installID, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number),
		map[string]string{"body": body}, &out)
	return out, err
}

// EditIssueComment replaces a comment this App posted.
func (c *restClient) EditIssueComment(ctx context.Context, installID int64, owner, repo string, commentID int64, body string) error {
	return c.do(ctx, installID, http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID),
		map[string]string{"body": body}, nil)
}

// CreateReviewReply posts in an inline review thread.
func (c *restClient) CreateReviewReply(ctx context.Context, installID int64, owner, repo string, number int, inReplyTo int64, body string) (Comment, error) {
	var out Comment
	err := c.do(ctx, installID, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number),
		map[string]any{"body": body, "in_reply_to": inReplyTo}, &out)
	return out, err
}

// ListIssueComments pages the PR conversation comments, oldest first as GitHub returns them.
func (c *restClient) ListIssueComments(ctx context.Context, installID int64, owner, repo string, number int) ([]Comment, error) {
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
func (c *restClient) ListReviewComments(ctx context.Context, installID int64, owner, repo string, number int) ([]Comment, error) {
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
func (c *restClient) ListReviews(ctx context.Context, installID int64, owner, repo string, number int) ([]Review, error) {
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

func (c *restClient) pages(ctx context.Context, installID int64, path string, consume func([]byte) (int, error)) error {
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

func (c *restClient) installationToken(ctx context.Context, installID int64) (string, error) {
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

func (c *restClient) jwt() (string, error) {
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

func (c *restClient) do(ctx context.Context, installID int64, method, path string, body, out any) error {
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, restErrorBody+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(raw)
		if len(raw) > restErrorBody {
			msg = msg[:restErrorBody] + "…"
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
