//go:build integration

package conductor_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// fakeApp is GitHub as an App sees it: an installation on podium-ade, a token for it, and
// the collaborator permission of whoever push names.
func fakeApp(t *testing.T, push map[string]string) *github.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orgs/podium-ade/installation", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42}`))
	})
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_app","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
	})
	mux.HandleFunc("GET /repos/podium-ade/podium/collaborators/{login}/permission", func(w http.ResponseWriter, r *http.Request) {
		level, ok := push[r.PathValue("login")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"permission":"` + level + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	gh, err := github.New(github.Options{
		AppID:         "1",
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		BaseURL:       srv.URL,
	})
	require.NoError(t, err)
	return gh
}

func pushesAsPerson() profiles.Playbook {
	return profiles.Playbook{
		Name:  "coder",
		Repos: []profiles.Repo{{Name: "podium", URL: "https://github.com/podium-ade/podium", DefaultBranch: "main"}},
		UserSecrets: []spec.SecretRef{{
			Name: profiles.GitHubTokenSecret, Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN",
		}},
	}
}

func fallbackConductor(t *testing.T, st *store.Store, app *github.Client) *conductor.Conductor {
	t.Helper()
	c, err := conductor.New(conductor.Options{
		Store: st, Podium: podium.New("http://podium.test", "agent-token"),
		Profiles: profiles.NewLive(testProfile(t)), Logger: quietLogger(),
		MintSecret: "conductor-bearer", GitTaskURL: "http://conductor.test:8090",
	})
	require.NoError(t, err)
	if app != nil {
		require.NoError(t, c.SetGitHub(app))
	}
	return c
}

// A playbook that pushes as the person who asked writes as the App on their behalf when the
// person cannot be written as, and refuses only when there is no App to write as.
func TestAPersonWhoCannotPushIsWrittenForByTheApp(t *testing.T) {
	ctx := context.Background()
	connect := func(t *testing.T, st *store.Store, reconnect bool) {
		t.Helper()
		require.NoError(t, st.PutGitHubAccount(ctx, store.GitHubAccount{
			Login: "ada@acme.com", GitHubID: 4242, GitHubLogin: "ada-gh", Name: "Ada L", ConnectedAt: time.Now(),
		}))
		if reconnect {
			require.NoError(t, st.MarkGitHubAccountNeedsReconnect(ctx, "ada@acme.com"))
		}
	}

	t.Run("a person who can push pushes as themselves", func(t *testing.T) {
		st := newStore(t)
		connect(t, st, false)
		c := fallbackConductor(t, st, fakeApp(t, map[string]string{"ada-gh": "write"}))
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "4242+ada-gh@users.noreply.github.com", got.Email)
	})

	t.Run("a person who can only read is written for by the app", func(t *testing.T) {
		st := newStore(t)
		connect(t, st, false)
		c := fallbackConductor(t, st, fakeApp(t, map[string]string{"ada-gh": "read"}))
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a person who is not a collaborator is written for by the app", func(t *testing.T) {
		st := newStore(t)
		connect(t, st, false)
		c := fallbackConductor(t, st, fakeApp(t, map[string]string{}))
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a person who never connected github is written for by the app", func(t *testing.T) {
		st := newStore(t)
		c := fallbackConductor(t, st, fakeApp(t, nil))
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a person who must reconnect is written for by the app", func(t *testing.T) {
		st := newStore(t)
		connect(t, st, true)
		c := fallbackConductor(t, st, fakeApp(t, map[string]string{"ada-gh": "write"}))
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("with no app, a person who never connected is refused", func(t *testing.T) {
		st := newStore(t)
		c := fallbackConductor(t, st, nil)
		_, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.ErrorContains(t, err, "you have not connected GitHub")
	})

	t.Run("with no app, a person who cannot push still pushes as themselves", func(t *testing.T) {
		st := newStore(t)
		connect(t, st, false)
		c := fallbackConductor(t, st, nil)
		got, err := c.GitHubPersonaForTest(ctx, "ada@acme.com", pushesAsPerson())
		require.NoError(t, err)
		assert.NotNil(t, got, "nothing could write for them, so the push is theirs to fail")
	})
}
