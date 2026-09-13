// Package github is the conductor's client of a GitHub App: it turns an App id and a private
// key into the short-lived installation tokens a turn clones, fetches and pushes with.
//
// Why an App rather than the personal access token a playbook's `secrets:` can already
// carry: an installation token lasts an hour, is scoped to named repositories, belongs to no
// human, and is revoked centrally by uninstalling the App. A PAT is none of those — it is
// long-lived, it carries one person's access, and revoking it is somebody remembering to.
//
// The cost is that a token has to be MINTED, and minting needs the private key. That key can
// mint a token for every repository the App is installed on, so it is strictly more valuable
// than the PAT it replaces and it lives in exactly one place: the conductor's own host,
// beside the master key. Nothing hands it to a task. See docs/security.md.
//
// Nothing here caches a token past its usefulness and nothing here logs one.
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
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is github.com's API. GitHub Enterprise Server lives on another host and is
// not supported: Podium speaks to one GitHub, and pretending otherwise would be a
// configuration knob with no tested path behind it.
const DefaultBaseURL = "https://api.github.com"

// DefaultTimeout bounds one call. Minting is two round trips at worst and both are small.
const DefaultTimeout = 15 * time.Second

// jwtTTL is how long an App JWT is good for. GitHub's ceiling is ten minutes and it refuses
// one issued in its own future, so this is nine minutes with a minute of backdated iat to
// pay for clock skew between here and GitHub.
const jwtTTL = 9 * time.Minute

// jwtBackdate is how far iat is moved into the past, for the same clock skew.
const jwtBackdate = time.Minute

// renewBefore is how long before expiry a cached token stops being handed out. GitHub gives
// an hour; a push that starts with two minutes left and takes three loses the push to a 401
// halfway up, which is a failure a human then has to read a git error to understand.
//
// It MUST stay above the runtime's own refresh margin — gitcred.ts's renewBeforeMs, ten
// minutes — and that is the constraint that sets the number, not the paragraph above.
//
// The runtime keeps one token in GH_TOKEN for the `gh` CLI, which reads a plain variable and
// cannot ask again for itself, and replaces it renewBeforeMs before it expires. If this
// margin were the smaller of the two, that request would land inside this cache and be
// answered with the SAME token the runtime was trying to replace — ten minutes from death —
// and the refresh would have achieved nothing. Fifteen minutes against ten means the ask
// always misses the cache and always gets a new token.
//
// Paying for it is a few more mints per long turn: a cached token is used for about
// forty-five minutes rather than fifty-five. Installation tokens are not rate limited
// anywhere near that, so the trade is free.
const renewBefore = 15 * time.Minute

// maxErrorBody is how much of a failure response is quoted in an error. GitHub answers with
// {"message": …}, which is short; something else in front of it may not be.
const maxErrorBody = 2 << 10

// ErrUnauthorized is GitHub refusing the App's own credential: a wrong app id, a key that
// does not match it, or an App that has been deleted. It is separated because it is the one
// failure an operator fixes by editing configuration rather than by reading a log.
var ErrUnauthorized = errors.New("github refused the app credential")

// ErrNotInstalled is a repository owner the App is not installed on. It means the operator
// created the App and never installed it, or installed it on another account — the most
// common way this is set up wrong, and unrecoverable without a human.
var ErrNotInstalled = errors.New("the app is not installed on that account")

// Token is one installation token and the moment it stops working.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Identity is the App's bot account as git and GitHub see it.
//
// The email is the whole point of the type. GitHub links a commit to an ACCOUNT by the
// author's email, and a bot account's address is `<user id>+<slug>[bot]@users.noreply.
// github.com` — an id nothing but the API will tell you. Get it wrong and every commit is
// attributed to nobody, which is the failure this whole path exists to avoid.
type Identity struct {
	Name  string
	Email string
}

// Options builds a Client.
type Options struct {
	// AppID is the App's numeric id, as GitHub's settings page shows it.
	AppID string
	// PrivateKeyPEM is the PEM the App's "generate a private key" button produced.
	PrivateKeyPEM []byte
	// BaseURL overrides DefaultBaseURL. It exists for tests.
	BaseURL string
	// HTTPClient overrides the default. It exists for tests.
	HTTPClient *http.Client
	// Now overrides the clock. It exists for tests.
	Now func() time.Time
}

// Client mints installation tokens for one GitHub App.
//
// It is safe for concurrent use and it caches: an installation id never changes, the bot
// identity never changes, and a token is reused until renewBefore of its expiry. A conductor
// running twenty turns of one playbook mints once, not twenty times, which is what keeps
// this off GitHub's rate limit.
type Client struct {
	base  *url.URL
	appID string
	key   *rsa.PrivateKey
	http  *http.Client
	now   func() time.Time

	mu            sync.Mutex
	installations map[string]int64
	tokens        map[string]Token
	identity      *Identity
}

// New returns a Client, or an error naming what is missing.
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.AppID) == "" {
		return nil, errors.New("github: an app id is required")
	}
	key, err := ParsePrivateKey(opts.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	raw := opts.BaseURL
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("github: parse %q: %w", raw, err)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		base:          base,
		appID:         strings.TrimSpace(opts.AppID),
		key:           key,
		http:          client,
		now:           now,
		installations: map[string]int64{},
		tokens:        map[string]Token{},
	}, nil
}

// ParsePrivateKey reads the PEM GitHub hands out. GitHub generates PKCS#1 ("BEGIN RSA
// PRIVATE KEY"); PKCS#8 is accepted too, because a key round-tripped through openssl or a
// secret manager often comes back in that form and refusing it would be a puzzle rather
// than a message.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github: the app private key is not PEM: expected a " +
			"-----BEGIN RSA PRIVATE KEY----- block, which is what GitHub's " +
			"\"generate a private key\" button produces")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: the app private key is neither PKCS#1 nor PKCS#8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github: the app private key is %T, and GitHub Apps sign with RSA", parsed)
	}
	return key, nil
}

// Token is an installation token for exactly the named repositories of one owner.
//
// repos are bare repository names ("monorepo"), not "owner/repo": GitHub's parameter is
// scoped to the installation, which already fixes the owner. An empty list is refused
// rather than sent, because GitHub reads "no repositories" as EVERY repository the
// installation has, and a turn that asked for nothing getting everything is the wrong way
// round for a default.
func (c *Client) Token(ctx context.Context, owner string, repos []string) (Token, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Token{}, errors.New("github: an owner is required to find the installation")
	}
	if len(repos) == 0 {
		return Token{}, errors.New("github: at least one repository is required: GitHub reads " +
			"an empty list as every repository the installation can see")
	}
	scope := slices.Clone(repos)
	slices.Sort(scope)
	scope = slices.Compact(scope)
	key := owner + "\x00" + strings.Join(scope, "\x00")

	c.mu.Lock()
	if tok, ok := c.tokens[key]; ok && c.now().Add(renewBefore).Before(tok.ExpiresAt) {
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	id, err := c.installation(ctx, owner)
	if err != nil {
		return Token{}, err
	}
	body, err := json.Marshal(tokenRequest{
		Repositories: scope,
		// Exactly what a turn does and nothing else: write a branch, open a pull request.
		// The App may be granted more in its settings; this narrows every token minted
		// here regardless, so a wider grant on the App is not a wider grant to a task.
		Permissions: tokenPermissions{Contents: "write", PullRequests: "write"},
	})
	if err != nil {
		return Token{}, fmt.Errorf("github: encode token request: %w", err)
	}
	var out tokenResponse
	path := fmt.Sprintf("/app/installations/%d/access_tokens", id)
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(out.Token) == "" {
		return Token{}, fmt.Errorf("github: POST %s answered no token", path)
	}
	tok := Token{Value: out.Token, ExpiresAt: out.ExpiresAt}
	c.mu.Lock()
	c.tokens[key] = tok
	c.mu.Unlock()
	return tok, nil
}

// Identity is the App's bot account, as an author. It is two calls the first time and
// cached for ever after: neither the slug nor the id of a bot account changes.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	c.mu.Lock()
	if c.identity != nil {
		out := *c.identity
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	var app appResponse
	if err := c.do(ctx, http.MethodGet, "/app", nil, &app); err != nil {
		return Identity{}, err
	}
	slug := strings.TrimSpace(app.Slug)
	if slug == "" {
		return Identity{}, errors.New("github: GET /app answered no slug")
	}
	// The bot's own account, which is where the numeric id in the address comes from. The
	// login is the slug with [bot] on the end, and it has to be escaped: [ and ] are not
	// path characters.
	login := slug + "[bot]"
	var user userResponse
	if err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(login), nil, &user); err != nil {
		return Identity{}, err
	}
	if user.ID == 0 {
		return Identity{}, fmt.Errorf("github: GET /users/%s answered no id", login)
	}
	out := Identity{
		Name:  login,
		Email: fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, login),
	}
	c.mu.Lock()
	c.identity = &out
	c.mu.Unlock()
	return out, nil
}

// installation is the App's installation on one account, cached. An installation id is
// stable for the life of the installation; uninstalling and reinstalling makes a new one,
// which shows up here as a 404 on the token call and is fixed by a restart.
func (c *Client) installation(ctx context.Context, owner string) (int64, error) {
	c.mu.Lock()
	if id, ok := c.installations[owner]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	var out installationResponse
	path := "/orgs/" + url.PathEscape(owner) + "/installation"
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	if errors.Is(err, ErrNotFound) {
		// An owner can be a user rather than an organisation, and the two have different
		// endpoints. Trying the second is cheaper than making the caller say which it is.
		out = installationResponse{}
		err = c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(owner)+"/installation", nil, &out)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return 0, fmt.Errorf("%w: %s", ErrNotInstalled, owner)
	case err != nil:
		return 0, err
	case out.ID == 0:
		return 0, fmt.Errorf("github: %s answered no installation id", path)
	}
	c.mu.Lock()
	c.installations[owner] = out.ID
	c.mu.Unlock()
	return out.ID, nil
}

// ErrNotFound is a 404. It is internal to this package's own retry between the org and user
// installation endpoints; callers see ErrNotInstalled instead.
var ErrNotFound = errors.New("github: not found")

// do makes one authenticated call. Every call here is authenticated by the App JWT and not
// by an installation token: this client's whole job is to obtain the latter.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	jwt, err := c.jwt()
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode >= 300 {
		return c.failure(method, path, res)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("github: decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) failure(method, path string, res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	detail := strings.TrimSpace(string(raw))
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Message != "" {
		detail = envelope.Message
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (%s %s): %s", ErrUnauthorized, method, path, detail)
	case http.StatusNotFound:
		return fmt.Errorf("%w (%s %s): %s", ErrNotFound, method, path, detail)
	}
	return fmt.Errorf("github: %s %s: %s: %s", method, path, res.Status, detail)
}

// jwt signs an App JWT. It is not cached: signing is microseconds, and a cached JWT is a
// second expiry to reason about for no gain.
func (c *Client) jwt() (string, error) {
	now := c.now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("github: encode jwt header: %w", err)
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-jwtBackdate).Unix(),
		"exp": now.Add(jwtTTL).Unix(),
		"iss": c.appID,
	})
	if err != nil {
		return "", fmt.Errorf("github: encode jwt claims: %w", err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// SplitRepoURL takes a playbook's repos[].url apart into the owner and the bare repository
// name the token call wants.
//
// It accepts what an operator actually writes — with or without .git, with or without a
// trailing slash — and refuses anything that is not github.com, because a token minted by
// this App is worthless anywhere else and a silent mismatch would show up as a 404 on the
// clone rather than as a message about the URL.
func SplitRepoURL(raw string) (owner, repo string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("github: parse repo url %q: %w", raw, err)
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		return "", "", fmt.Errorf("github: repo url %q is not on github.com, and a GitHub App "+
			"token works nowhere else", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github: repo url %q is not <owner>/<repo>", raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// ---------------------------------------------------------------------------
// the wire, as GitHub's REST API v2022-11-28 speaks it
// ---------------------------------------------------------------------------

type tokenRequest struct {
	Repositories []string         `json:"repositories"`
	Permissions  tokenPermissions `json:"permissions"`
}

type tokenPermissions struct {
	Contents     string `json:"contents"`
	PullRequests string `json:"pull_requests"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type installationResponse struct {
	ID int64 `json:"id"`
}

type appResponse struct {
	Slug string `json:"slug"`
}

type userResponse struct {
	ID int64 `json:"id"`
}
