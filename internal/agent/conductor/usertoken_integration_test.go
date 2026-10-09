//go:build integration

package conductor_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/pkg/spec"
)

// fakeVault is the control plane's secret store, keyed by owner and name.
type fakeVault struct {
	podiumv1connect.UnimplementedSecretServiceHandler
	mu     sync.Mutex
	values map[string][]byte
}

func (v *fakeVault) key(owner, name string) string { return owner + "/" + name }

func (v *fakeVault) SetSecret(_ context.Context, req *connect.Request[podiumv1.SetSecretRequest]) (*connect.Response[podiumv1.SetSecretResponse], error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[v.key(req.Msg.GetOwner(), req.Msg.GetName())] = append([]byte(nil), req.Msg.GetValue()...)
	return connect.NewResponse(&podiumv1.SetSecretResponse{Secret: &podiumv1.Secret{Name: req.Msg.GetName(), Version: 1}}), nil
}

func (v *fakeVault) DeleteSecret(_ context.Context, req *connect.Request[podiumv1.DeleteSecretRequest]) (*connect.Response[podiumv1.DeleteSecretResponse], error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	k := v.key(req.Msg.GetOwner(), req.Msg.GetName())
	if _, ok := v.values[k]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such secret"))
	}
	delete(v.values, k)
	return connect.NewResponse(&podiumv1.DeleteSecretResponse{}), nil
}

func (v *fakeVault) ReadSecret(_ context.Context, req *connect.Request[podiumv1.ReadSecretRequest]) (*connect.Response[podiumv1.ReadSecretResponse], error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[v.key(req.Msg.GetOwner(), req.Msg.GetName())]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such secret"))
	}
	return connect.NewResponse(&podiumv1.ReadSecretResponse{Value: value}), nil
}

func (v *fakeVault) has(owner, name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.values[v.key(owner, name)]
	return ok
}

// fakeUserTokens is GitHub's scoped-token and revoke endpoints for one OAuth client.
type fakeUserTokens struct {
	mu      sync.Mutex
	issued  int
	revoked []string
}

func (f *fakeUserTokens) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /applications/Iv23abc/token/scoped", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		var body struct {
			AccessToken  string   `json:"access_token"`
			Target       string   `json:"target"`
			Repositories []string `json:"repositories"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if user != "Iv23abc" || pass != "csec" || body.AccessToken != "ghu_ada" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, "podium-ade", body.Target)
		assert.Equal(t, []string{"podium"}, body.Repositories)
		f.mu.Lock()
		f.issued++
		n := f.issued
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghu_scoped_" + string(rune('0'+n)), "expires_at": nil})
	})
	mux.HandleFunc("DELETE /applications/Iv23abc/token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AccessToken string `json:"access_token"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		f.mu.Lock()
		f.revoked = append(f.revoked, body.AccessToken)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// A person's turn gets a token GitHub scoped to its repositories, never their own. Every
// token it was issued is revoked when it ends, including by a conductor that restarted.
func TestAPersonsTurnGetsScopedTokensThatAreRevoked(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	vault := &fakeVault{values: map[string][]byte{"ada@acme.com/github.token": []byte("ghu_ada")}}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewSecretServiceHandler(vault))
	plane := httptest.NewServer(mux)
	t.Cleanup(plane.Close)
	pc := podium.New(plane.URL, "agent-token")

	gh := &fakeUserTokens{}
	ghSrv := httptest.NewServer(gh.handler(t))
	t.Cleanup(ghSrv.Close)

	require.NoError(t, connections.SaveGitHub(ctx, st, pc, connections.GitHub{
		AppID: "1", ClientID: "Iv23abc", ClientSecret: "csec",
	}, nil))
	require.NoError(t, st.PutGitHubAccount(ctx, store.GitHubAccount{
		Login: "ada@acme.com", GitHubID: 4242, GitHubLogin: "ada-gh", Name: "Ada L", ConnectedAt: time.Now(),
	}))
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.SourceChat, SourceKey: "chat:1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := st.CreateTurn(ctx, sess.ID, "chat_1", store.Backend{})
	require.NoError(t, err)

	newConductor := func() *conductor.Conductor {
		c, err := conductor.New(conductor.Options{
			Store: st, Podium: pc, Profiles: profiles.NewLive(testProfile(t)), Logger: quietLogger(),
			MintSecret: "conductor-bearer", GitTaskURL: "http://conductor.test:8090", GitHubAPIURL: ghSrv.URL,
		})
		require.NoError(t, err)
		return c
	}
	first := newConductor()
	capability, err := first.MintCapabilityForTest(turn.ID, conductor.GitScope{
		Owner: "podium-ade", Repos: []string{"podium"}, Login: "ada@acme.com",
	})
	require.NoError(t, err)

	cred, err := first.MintGitToken(ctx, capability)
	require.NoError(t, err)
	assert.Equal(t, "ghu_scoped_1", cred.Token, "the turn gets a scoped token, not the person's")
	assert.Equal(t, "4242+ada-gh@users.noreply.github.com", cred.Identity.Email)
	assert.WithinDuration(t, time.Now().Add(time.Hour), cred.ExpiresAt, time.Minute,
		"a token GitHub gave no expiry gets the conductor's own")

	again, err := first.MintGitToken(ctx, capability)
	require.NoError(t, err)
	assert.Equal(t, cred.Token, again.Token, "a fresh token is reused rather than issuing another")
	assert.True(t, vault.has("", spec.UserGitTokenSecretPrefix+turn.ID), "issued tokens are recorded")

	// A restarted conductor still revokes what the first one issued.
	second := newConductor()
	playbook := profiles.Playbook{UserSecrets: []spec.SecretRef{{
		Name: profiles.GitHubTokenSecret, Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN",
	}}}
	second.DropGitCapabilityForTest(ctx, turn.ID, playbook)
	assert.Equal(t, []string{"ghu_scoped_1"}, gh.revoked)
	assert.False(t, vault.has("", spec.UserGitTokenSecretPrefix+turn.ID))

	_, err = second.MintGitToken(ctx, capability)
	require.ErrorIs(t, err, conductor.ErrBadCapability, "a turn whose tokens were revoked mints no more")
}
