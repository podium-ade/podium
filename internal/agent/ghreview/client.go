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
	defaultAPIURL = "https://api.github.com"
	apiVersion    = "2022-11-28"
	restErrorBody = 4 << 10
	// A successful pull or comment page is far larger than an error message. The
	// pull carries the head repository before head.sha, and one comment page
	// carries up to 100 bodies. Cutting either off makes Unmarshal fail, and the
	// review then has no commit to pin its inline notes to.
	restSuccessBody = 16 << 20
	tokenFreshness  = 1 * time.Minute
	httpTimeout     = 30 * time.Second
	listPageSize    = 100
	listPageCap     = 20
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
	UpdatedAt time.Time `json:"updated_at"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	// OriginalLine is where the comment was made. Line is where that line is now, and zero
	// once the code under it changed.
	OriginalLine int    `json:"original_line"`
	DiffHunk     string `json:"diff_hunk"`
	InReplyTo    int64  `json:"in_reply_to_id"`
	User         User   `json:"user"`
}

// Review is a submitted pull-request review.
type Review struct {
	ID          int64     `json:"id"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
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

// CreateReview submits one pull-request review. event is APPROVE or COMMENT.
// comments are the inline notes on that same review; GitHub requires the head
// commit when any are set, and rejects the whole review if one line is not in
// the diff. A body-only review sends no commit id.
func (c *restClient) CreateReview(ctx context.Context, installID int64, owner, repo string, number int, event, body, commit string, comments []pullComment) (Review, error) {
	if event == "" {
		event = eventComment
	}
	payload := map[string]any{"body": body, "event": event}
	if len(comments) > 0 {
		payload["commit_id"] = commit
		payload["comments"] = comments
	}
	var out Review
	err := c.do(ctx, installID, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number),
		payload, &out)
	return out, err
}

// pullInfo is the pull request as the status comment and the inline notes need it.
type pullInfo struct {
	OK        bool
	Title     string
	Head      string
	Base      string
	SHA       string
	Commits   int
	Files     int
	Additions int
	Deletions int
	// Fork is a pull request whose head branch is in another repository. Locked is a fork
	// whose author has not let maintainers push to that branch, so nothing here can.
	Fork   bool
	Locked bool
}

// Pull reads the pull request. The head commit is what inline comments pin to,
// and the counts are what the status comment shows.
func (c *restClient) Pull(ctx context.Context, installID int64, owner, repo string, number int) (pullInfo, error) {
	var out struct {
		Title               string `json:"title"`
		Commits             int    `json:"commits"`
		Additions           int    `json:"additions"`
		Deletions           int    `json:"deletions"`
		ChangedFiles        int    `json:"changed_files"`
		MaintainerCanModify bool   `json:"maintainer_can_modify"`
		Head                struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	err := c.do(ctx, installID, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number), nil, &out)
	if err != nil {
		return pullInfo{}, err
	}
	// A head repository that was deleted reads as null: still a fork, and nobody can push.
	fork := out.Head.Repo == nil ||
		(out.Base.Repo.FullName != "" && !strings.EqualFold(out.Head.Repo.FullName, out.Base.Repo.FullName))
	return pullInfo{
		Title:     out.Title,
		Head:      out.Head.Ref,
		Base:      out.Base.Ref,
		SHA:       out.Head.SHA,
		Commits:   out.Commits,
		Files:     out.ChangedFiles,
		Additions: out.Additions,
		Deletions: out.Deletions,
		Fork:      fork,
		Locked:    fork && (out.Head.Repo == nil || !out.MaintainerCanModify),
	}, nil
}

// PullHeadSHA is the commit a review's inline comments attach to.
func (c *restClient) PullHeadSHA(ctx context.Context, installID int64, owner, repo string, number int) (string, error) {
	info, err := c.Pull(ctx, installID, owner, repo, number)
	if err != nil {
		return "", err
	}
	if info.SHA == "" {
		return "", errors.New("github: pull request has no head sha")
	}
	return info.SHA, nil
}

// UpdateReview replaces the summary of a review this App already submitted.
func (c *restClient) UpdateReview(ctx context.Context, installID int64, owner, repo string, number int, reviewID int64, body string) error {
	return c.do(ctx, installID, http.MethodPut,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews/%d", owner, repo, number, reviewID),
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

// Role is login's role on the repository: read, triage, write, maintain or admin, or "" for
// none. role_name is the finer of GitHub's two answers. An organisation's custom role is
// named by its owner, so it falls back to the base permission it was built on.
func (c *restClient) Role(ctx context.Context, installID int64, owner, repo, login string) (string, error) {
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	err := c.do(ctx, installID, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission", owner, repo, url.PathEscape(login)), nil, &out)
	if err != nil {
		return "", err
	}
	for _, r := range []string{out.RoleName, out.Permission} {
		switch r {
		case "read", "triage", "write", "maintain", "admin":
			return r, nil
		}
	}
	return "", nil
}

// graphqlURL is where GraphQL is: /graphql beside the REST root, and /api/graphql on a GitHub
// Enterprise Server whose REST root is /api/v3.
func (c *restClient) graphqlURL() string {
	if base, ok := strings.CutSuffix(c.baseURL, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return c.baseURL + "/graphql"
}

// graphql runs one query. GitHub answers a GraphQL error with 200 and an errors array.
func (c *restClient) graphql(ctx context.Context, installID int64, query string, vars map[string]any, out any) error {
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, installID, http.MethodPost, c.graphqlURL(),
		map[string]any{"query": query, "variables": vars}, &res); err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("github: graphql: %s", res.Errors[0].Message)
	}
	if out == nil || len(res.Data) == 0 {
		return nil
	}
	return json.Unmarshal(res.Data, out)
}

const reviewThreadsQuery = `query($owner: String!, $repo: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        nodes { id isResolved comments(first: 1) { nodes { databaseId } } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

const resolveThreadMutation = `mutation($id: ID!) {
  resolveReviewThread(input: {threadId: $id}) { thread { id } }
}`

// maxThreadPages bounds the thread walk: a pull request with more than this many hundred
// review threads leaves the rest unresolved rather than paging for ever.
const maxThreadPages = 5

// ResolveThread resolves the review thread whose first comment is rootID. A thread already
// resolved is left alone; one that cannot be found is an error.
func (c *restClient) ResolveThread(ctx context.Context, installID int64, owner, repo string, number int, rootID int64) error {
	var after any
	for range maxThreadPages {
		var page struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		err := c.graphql(ctx, installID, reviewThreadsQuery,
			map[string]any{"owner": owner, "repo": repo, "number": number, "after": after}, &page)
		if err != nil {
			return err
		}
		threads := page.Repository.PullRequest.ReviewThreads
		for _, t := range threads.Nodes {
			if len(t.Comments.Nodes) == 0 || t.Comments.Nodes[0].DatabaseID != rootID {
				continue
			}
			if t.IsResolved {
				return nil
			}
			return c.graphql(ctx, installID, resolveThreadMutation, map[string]any{"id": t.ID}, nil)
		}
		if !threads.PageInfo.HasNextPage {
			break
		}
		after = threads.PageInfo.EndCursor
	}
	return fmt.Errorf("github: no review thread starts with comment %d", rootID)
}

// reactionSeen is the reaction a comment gets when a turn starts on it.
const reactionSeen = "eyes"

// CreateReaction reacts to one comment. kind is KindIssueComment for the conversation tab
// and KindReviewComment for the diff: GitHub keeps the two under different paths.
func (c *restClient) CreateReaction(ctx context.Context, installID int64, owner, repo, kind string, commentID int64, content string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", owner, repo, commentID)
	if kind == KindReviewComment {
		path = fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", owner, repo, commentID)
	}
	return c.do(ctx, installID, http.MethodPost, path, map[string]string{"content": content}, nil)
}

// ReviewComment reads one inline review comment. A reply's in_reply_to_id is the first
// comment of its thread, so this is how a reply learns who started the thread.
func (c *restClient) ReviewComment(ctx context.Context, installID int64, owner, repo string, commentID int64) (Comment, error) {
	var out Comment
	err := c.do(ctx, installID, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/pulls/comments/%d", owner, repo, commentID), nil, &out)
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

// PullFile is one file a pull request changes. Patch is its part of the diff, empty for a
// binary file or one GitHub thinks too large to show.
type PullFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

// PullFiles pages the files a pull request changes.
func (c *restClient) PullFiles(ctx context.Context, installID int64, owner, repo string, number int) ([]PullFile, error) {
	var all []PullFile
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files", owner, repo, number)
	err := c.pages(ctx, installID, path, func(raw []byte) (int, error) {
		var page []PullFile
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, err
		}
		all = append(all, page...)
		return len(page), nil
	})
	return all, err
}

// FileAt is one file's contents at ref.
func (c *restClient) FileAt(ctx context.Context, installID int64, owner, repo, path, ref string) ([]byte, error) {
	escaped := make([]string, 0, 4)
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		escaped = append(escaped, url.PathEscape(part))
	}
	var raw []byte
	err := c.doAccept(ctx, installID, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, strings.Join(escaped, "/"), url.QueryEscape(ref)),
		"application/vnd.github.raw", nil, &raw)
	return raw, err
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
	return c.doAccept(ctx, installID, method, path, "application/vnd.github+json", body, out)
}

// doAccept is do with another media type. out as a *[]byte takes the body as it came.
func (c *restClient) doAccept(ctx context.Context, installID int64, method, path, accept string, body, out any) error {
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
	target := c.baseURL + path
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		target = path
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
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
	failed := resp.StatusCode < 200 || resp.StatusCode >= 300
	limit := int64(restSuccessBody)
	if failed {
		limit = restErrorBody
	}
	raw, tooBig, err := readBody(resp.Body, limit)
	if err != nil {
		return fmt.Errorf("github: read %s %s: %w", method, path, err)
	}
	if failed {
		msg := string(raw)
		if tooBig {
			msg += "…"
		}
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(msg)}
	}
	if tooBig {
		return fmt.Errorf("github: %s %s: response exceeds %d bytes", method, path, restSuccessBody)
	}
	if out == nil || len(raw) == 0 || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if b, ok := out.(*[]byte); ok {
		*b = raw
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: decode %s %s: %w", method, path, err)
	}
	return nil
}

// readBody reads at most limit bytes, and reports whether the body continued
// past that. The extra byte is how a full buffer is told from a body that
// ended on the limit.
func readBody(r io.Reader, limit int64) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > limit {
		return raw[:limit], true, nil
	}
	return raw, false, nil
}
