package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

func restCall(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec.Code, out
}

// Each resource reaches the service method it stands for, with the bearer as the turn's
// credential and the path's id as the message's.
func TestTheTurnSurfaceAsResources(t *testing.T) {
	fake := &fakeDelegator{dlg: running(), list: nil, prog: "running the tests"}
	minter := &fakeMinter{cred: conductor.GitCredential{Token: "ghs_minted"}}
	h := NewTurnREST(NewTurnService(fake, quietLogger()), NewGitCredentialService(minter, quietLogger()), nil)

	code, out := restCall(t, h, http.MethodPost, "/v1/delegations", "tok", `{"playbook":"review","instruction":"read it","readOnly":true}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "review", out["delegation"].(map[string]any)["playbook"])
	assert.True(t, fake.opts[0].ReadOnly)

	code, out = restCall(t, h, http.MethodGet, "/v1/delegations/dlg_7", "tok", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "dlg_7", out["delegation"].(map[string]any)["id"])
	assert.Equal(t, "running the tests", out["progress"])

	code, _ = restCall(t, h, http.MethodGet, "/v1/delegations", "tok", "")
	assert.Equal(t, http.StatusOK, code)

	code, out = restCall(t, h, http.MethodPost, "/v1/delegations/dlg_8/cancel", "tok", `{"reason":"no longer needed"}`)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "dlg_8", out["delegation"].(map[string]any)["id"])

	code, out = restCall(t, h, http.MethodPost, "/v1/delegations/dlg_9/messages", "tok", `{"text":"also check the docs"}`)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "dlg_9", out["delegation"].(map[string]any)["id"])

	code, out = restCall(t, h, http.MethodDelete, "/v1/delegations/dlg_10/preview", "tok", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "dlg_10", out["delegation"].(map[string]any)["id"])

	code, out = restCall(t, h, http.MethodPost, "/v1/chats", "tok",
		`{"title":"Triage","opening":"Starting a triage.","share_with":["dana@acme.com"],"access":"members"}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "chat_new", out["chatId"])
	assert.Equal(t, "https://podium.example/agent/chat/chat_new", out["url"])
	assert.Equal(t, []string{"dana@acme.com"}, fake.chats[0].ShareWith)

	code, out = restCall(t, h, http.MethodPost, "/v1/routine-runs/finish", "tok",
		`{"status":"idle","summary":"nothing new","stateJson":"{\"seen\":[1]}","proposeNote":"skip drafts"}`)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Recorded as idle.", out["message"])
	require.Len(t, fake.finishes, 1)
	assert.Equal(t, "idle", fake.finishes[0].Status)
	assert.JSONEq(t, `{"seen":[1]}`, string(fake.finishes[0].State))
	assert.Equal(t, "skip drafts", fake.finishes[0].ProposeNote)

	code, out = restCall(t, h, http.MethodPost, "/v1/routines", "tok",
		`{"name":"PR digest","instruction":"Report failing PRs.","schedule":"0 9 * * 1-5","timezone":"America/Toronto"}`)
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "Created.", out["message"])
	require.Len(t, fake.routines, 1)
	assert.Equal(t, "0 9 * * 1-5", fake.routines[0].Schedule)
	assert.Equal(t, "America/Toronto", fake.routines[0].Timezone)

	for _, tok := range fake.tokens {
		assert.Equal(t, "tok", tok)
	}

	code, out = restCall(t, h, http.MethodPost, "/v1/git-tokens", "cap", "")
	assert.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "ghs_minted", out["token"])
	assert.Equal(t, []string{"cap"}, minter.seen)
}

func TestTheTurnSurfaceResourcesRefuseInHTTPsTerms(t *testing.T) {
	h := NewTurnREST(NewTurnService(&fakeDelegator{dlg: running()}, quietLogger()), nil, nil)

	code, out := restCall(t, h, http.MethodPost, "/v1/delegations", "", `{"playbook":"review","instruction":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "unauthenticated", out["code"])

	code, out = restCall(t, h, http.MethodPost, "/v1/delegations", "tok", `{not json`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "invalid_argument", out["code"])

	code, _ = restCall(t, h, http.MethodPut, "/v1/delegations/dlg_1", "tok", "")
	assert.Equal(t, http.StatusMethodNotAllowed, code)

	code, _ = restCall(t, h, http.MethodPost, "/v1/git-tokens", "cap", "")
	assert.Equal(t, http.StatusNotFound, code, "no GitHub App, no route")

	refusing := NewTurnREST(NewTurnService(&fakeDelegator{err: conductor.ErrDelegationNotFound}, quietLogger()), nil, nil)
	code, out = restCall(t, refusing, http.MethodGet, "/v1/delegations/dlg_x", "tok", "")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", out["code"])
}
