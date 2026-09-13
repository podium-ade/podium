package github

import (
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
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAppID is an obvious fake. Nothing in this package has ever seen a real one.
const testAppID = "123456"

// key is one RSA key for the whole suite: generating a 2048-bit key per test dominates the
// run time and buys nothing, because no test cares which key it is.
var key = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

func pkcs1PEM(t *testing.T) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key()),
	})
}

// call is one request the fake GitHub saw.
type call struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

// fakeGitHub answers the three endpoints this package uses. routes maps "METHOD /path" to a
// status and a body; anything unrouted is a 404, which is what GitHub answers too.
type fakeGitHub struct {
	mu     sync.Mutex
	calls  []call
	routes map[string]func() (int, string)
}

func newFake() *fakeGitHub {
	return &fakeGitHub{routes: map[string]func() (int, string){}}
}

func (f *fakeGitHub) route(key string, status int, body string) {
	f.routes[key] = func() (int, string) { return status, body }
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call{
		Method: r.Method, Path: r.URL.Path,
		Auth: r.Header.Get("Authorization"), Body: string(raw),
	})
	fn, ok := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	status, body := fn()
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeGitHub) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

// client wires a Client to a fake, with a fixed clock so expiry is testable.
func client(t *testing.T, f *fakeGitHub, now time.Time) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		AppID:         testAppID,
		PrivateKeyPEM: pkcs1PEM(t),
		BaseURL:       srv.URL,
		Now:           func() time.Time { return now },
	})
	require.NoError(t, err)
	return c
}

func TestParsePrivateKey(t *testing.T) {
	t.Run("reads the PKCS#1 GitHub hands out", func(t *testing.T) {
		got, err := ParsePrivateKey(pkcs1PEM(t))
		require.NoError(t, err)
		assert.Equal(t, key().N, got.N)
	})

	t.Run("reads PKCS#8, which is what a round trip through openssl produces", func(t *testing.T) {
		der, err := x509.MarshalPKCS8PrivateKey(key())
		require.NoError(t, err)
		got, err := ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		require.NoError(t, err)
		assert.Equal(t, key().N, got.N)
	})

	t.Run("says what it wanted when the value is not PEM at all", func(t *testing.T) {
		_, err := ParsePrivateKey([]byte("ghp_not_a_key"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BEGIN RSA PRIVATE KEY")
	})

	t.Run("refuses a PEM that is not a key", func(t *testing.T) {
		_, err := ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")}))
		require.Error(t, err)
	})
}

func TestNew(t *testing.T) {
	t.Run("needs an app id", func(t *testing.T) {
		_, err := New(Options{PrivateKeyPEM: pkcs1PEM(t)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app id")
	})

	t.Run("needs a key it can parse", func(t *testing.T) {
		_, err := New(Options{AppID: testAppID, PrivateKeyPEM: []byte("nope")})
		require.Error(t, err)
	})
}

// The JWT is what authenticates every call, so it is checked against the public key rather
// than merely for shape: a token GitHub would reject is a failure no test of ours would see.
func TestJWT(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	c := client(t, newFake(), now)

	token, err := c.jwt()
	require.NoError(t, err)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	var header map[string]string
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &header))
	assert.Equal(t, "RS256", header["alg"], "GitHub accepts RS256 and nothing else")

	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	raw, err = base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &claims))
	assert.Equal(t, testAppID, claims.Iss)
	assert.Equal(t, now.Add(-jwtBackdate).Unix(), claims.Iat, "iat is backdated for clock skew")
	assert.LessOrEqual(t, claims.Exp-claims.Iat, int64(10*60), "GitHub refuses a JWT living over ten minutes")

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.NoError(t, rsa.VerifyPKCS1v15(&key().PublicKey, crypto.SHA256, digest[:], sig),
		"the signature must verify against the app's own key")
}

func TestToken(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)

	setup := func(t *testing.T) (*Client, *fakeGitHub) {
		t.Helper()
		f := newFake()
		f.route("GET /orgs/affiniti-finance/installation", 200, `{"id":42}`)
		f.route("POST /app/installations/42/access_tokens", 201,
			`{"token":"ghs_minted","expires_at":"`+expires.Format(time.RFC3339)+`"}`)
		return client(t, f, now), f
	}

	t.Run("mints a token scoped to the named repositories", func(t *testing.T) {
		c, f := setup(t)
		got, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.NoError(t, err)
		assert.Equal(t, "ghs_minted", got.Value)
		assert.Equal(t, expires, got.ExpiresAt.UTC())

		calls := f.seen()
		require.Len(t, calls, 2)
		var sent tokenRequest
		require.NoError(t, json.Unmarshal([]byte(calls[1].Body), &sent))
		assert.Equal(t, []string{"monorepo"}, sent.Repositories)
		// The App may be granted more than this in its settings; a token minted here is
		// narrowed regardless, so a wider App is not a wider task.
		assert.Equal(t, "write", sent.Permissions.Contents)
		assert.Equal(t, "write", sent.Permissions.PullRequests)
	})

	t.Run("authenticates with the app JWT and never with an installation token", func(t *testing.T) {
		c, f := setup(t)
		_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.NoError(t, err)
		for _, got := range f.seen() {
			assert.True(t, strings.HasPrefix(got.Auth, "Bearer "), got.Path)
			assert.NotContains(t, got.Auth, "ghs_minted")
		}
	})

	t.Run("reuses a live token rather than minting per call", func(t *testing.T) {
		c, f := setup(t)
		for range 3 {
			_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
			require.NoError(t, err)
		}
		assert.Len(t, f.seen(), 2, "one installation lookup and one mint, however many asks")
	})

	t.Run("orders the scope so the same two repos are one cache entry", func(t *testing.T) {
		c, f := setup(t)
		_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo", "podium"})
		require.NoError(t, err)
		_, err = c.Token(t.Context(), "affiniti-finance", []string{"podium", "monorepo"})
		require.NoError(t, err)
		assert.Len(t, f.seen(), 2)
	})

	t.Run("mints again for a different scope, because the token is not valid for it", func(t *testing.T) {
		c, f := setup(t)
		_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.NoError(t, err)
		_, err = c.Token(t.Context(), "affiniti-finance", []string{"other"})
		require.NoError(t, err)
		assert.Len(t, f.seen(), 3, "the installation is cached; the token is not shared across scopes")
	})

	// The whole reason this package exists: a two-hour turn pushes at the end, long after a
	// one-hour token was minted. A cached token inside renewBefore of expiry must not be
	// handed out.
	t.Run("re-mints a token that is about to expire", func(t *testing.T) {
		f := newFake()
		f.route("GET /orgs/affiniti-finance/installation", 200, `{"id":42}`)
		f.route("POST /app/installations/42/access_tokens", 201,
			`{"token":"ghs_minted","expires_at":"`+expires.Format(time.RFC3339)+`"}`)
		srv := httptest.NewServer(f)
		t.Cleanup(srv.Close)

		clock := now
		c, err := New(Options{
			AppID: testAppID, PrivateKeyPEM: pkcs1PEM(t), BaseURL: srv.URL,
			Now: func() time.Time { return clock },
		})
		require.NoError(t, err)

		_, err = c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.NoError(t, err)
		require.Len(t, f.seen(), 2)

		clock = expires.Add(-renewBefore + time.Second)
		_, err = c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.NoError(t, err)
		assert.Len(t, f.seen(), 3, "a token inside renewBefore of expiry is replaced, not reused")
	})

	t.Run("refuses an empty scope, which GitHub would read as every repository", func(t *testing.T) {
		c, _ := setup(t)
		_, err := c.Token(t.Context(), "affiniti-finance", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "every repository")
	})

	t.Run("refuses a missing owner", func(t *testing.T) {
		c, _ := setup(t)
		_, err := c.Token(t.Context(), "  ", []string{"monorepo"})
		require.Error(t, err)
	})

	t.Run("falls back to the user endpoint when the owner is not an organisation", func(t *testing.T) {
		f := newFake()
		f.route("GET /users/acme/installation", 200, `{"id":7}`)
		f.route("POST /app/installations/7/access_tokens", 201,
			`{"token":"ghs_user","expires_at":"`+expires.Format(time.RFC3339)+`"}`)
		c := client(t, f, now)

		got, err := c.Token(t.Context(), "acme", []string{"podium"})
		require.NoError(t, err)
		assert.Equal(t, "ghs_user", got.Value)
	})

	t.Run("names the account when the app is installed nowhere", func(t *testing.T) {
		c := client(t, newFake(), now)
		_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.ErrorIs(t, err, ErrNotInstalled)
		assert.Contains(t, err.Error(), "affiniti-finance")
	})

	t.Run("separates a refused app credential from every other failure", func(t *testing.T) {
		f := newFake()
		f.route("GET /orgs/affiniti-finance/installation", 401, `{"message":"A JSON web token could not be decoded"}`)
		c := client(t, f, now)
		_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
		require.ErrorIs(t, err, ErrUnauthorized)
	})
}

func TestIdentity(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	t.Run("builds the address GitHub links a commit by", func(t *testing.T) {
		f := newFake()
		f.route("GET /app", 200, `{"slug":"podium-agent"}`)
		f.route("GET /users/podium-agent[bot]", 200, `{"id":987654}`)
		c := client(t, f, now)

		got, err := c.Identity(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "podium-agent[bot]", got.Name)
		assert.Equal(t, "987654+podium-agent[bot]@users.noreply.github.com", got.Email)
	})

	t.Run("asks once and remembers: neither a slug nor a bot id changes", func(t *testing.T) {
		f := newFake()
		f.route("GET /app", 200, `{"slug":"podium-agent"}`)
		f.route("GET /users/podium-agent[bot]", 200, `{"id":987654}`)
		c := client(t, f, now)

		for range 3 {
			_, err := c.Identity(t.Context())
			require.NoError(t, err)
		}
		assert.Len(t, f.seen(), 2)
	})

	t.Run("refuses to invent an address when the bot account cannot be read", func(t *testing.T) {
		f := newFake()
		f.route("GET /app", 200, `{"slug":"podium-agent"}`)
		c := client(t, f, now)
		_, err := c.Identity(t.Context())
		require.Error(t, err)
	})
}

func TestSplitRepoURL(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		owner string
		repo  string
	}{
		{"https://github.com/affiniti-finance/monorepo", "affiniti-finance", "monorepo"},
		{"https://github.com/affiniti-finance/monorepo.git", "affiniti-finance", "monorepo"},
		{"https://github.com/affiniti-finance/monorepo/", "affiniti-finance", "monorepo"},
		{"  https://github.com/acme/podium  ", "acme", "podium"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			owner, repo, err := SplitRepoURL(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.owner, owner)
			assert.Equal(t, tc.repo, repo)
		})
	}

	t.Run("refuses a host this app's token is worthless on", func(t *testing.T) {
		_, _, err := SplitRepoURL("https://gitlab.com/affiniti/monorepo")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "github.com")
	})

	t.Run("refuses a path that is not owner/repo", func(t *testing.T) {
		_, _, err := SplitRepoURL("https://github.com/affiniti-finance")
		require.Error(t, err)
	})
}

// A minted token must never reach a log or an error string. The failure path quotes
// GitHub's own message, so this checks the one place a token could be echoed back.
func TestFailureDoesNotEchoAToken(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	f := newFake()
	f.route("GET /orgs/affiniti-finance/installation", 200, `{"id":42}`)
	f.route("POST /app/installations/42/access_tokens", 500, `{"message":"boom"}`)
	c := client(t, f, now)

	_, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.NotContains(t, err.Error(), "ghs_")
	assert.False(t, errors.Is(err, ErrUnauthorized))
}

// The margin is not a matter of taste: it is set by the runtime's own refresh interval.
// gitcred.ts keeps one token in GH_TOKEN for the `gh` CLI and replaces it renewBeforeMs —
// ten minutes — before expiry. If this cache's margin were the smaller of the two, that
// request would be answered with the very token the runtime was replacing.
func TestRenewBeforeExceedsTheRuntimeRefreshMargin(t *testing.T) {
	// agent/runtime/src/gitcred.ts calls this renewBeforeMs.
	const runtimeRefreshMargin = 10 * time.Minute
	assert.Greater(t, renewBefore, runtimeRefreshMargin,
		"a token handed out must outlive the next time the runtime asks for one")
}

// A turn longer than the life of an installation token is the case this whole path exists
// for: the token is minted per ask, so the push at the end of a ninety-minute turn gets a
// different token from the clone at its start.
func TestATurnLongerThanATokenKeepsWorking(t *testing.T) {
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	clock := start
	minted := 0

	f := newFake()
	f.route("GET /orgs/affiniti-finance/installation", 200, `{"id":42}`)
	f.routes["POST /app/installations/42/access_tokens"] = func() (int, string) {
		minted++
		return 201, fmt.Sprintf(`{"token":"ghs_%d","expires_at":%q}`,
			minted, clock.Add(time.Hour).Format(time.RFC3339))
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := New(Options{
		AppID: testAppID, PrivateKeyPEM: pkcs1PEM(t), BaseURL: srv.URL,
		Now: func() time.Time { return clock },
	})
	require.NoError(t, err)

	// The clone, at the start of the turn.
	atClone, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
	require.NoError(t, err)

	// Ninety minutes in, the agent pushes. git runs the credential helper again, which is
	// what makes this work at all: the token from the clone died half an hour ago.
	clock = start.Add(90 * time.Minute)
	atPush, err := c.Token(t.Context(), "affiniti-finance", []string{"monorepo"})
	require.NoError(t, err)

	assert.NotEqual(t, atClone.Value, atPush.Value, "the push must not be handed a dead token")
	assert.True(t, atPush.ExpiresAt.After(clock), "and the one it gets must still be alive")
	assert.Equal(t, 2, minted)
}
