//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/artifacts/fakes3"
)

// The Connect procedures the settings screen calls, through podium-server's proxy.
const (
	getSettingsPath      = "/podium.agent.v1.AgentService/GetSettings"
	setProviderKeyPath   = "/podium.agent.v1.AgentService/SetProviderKey"
	clearProviderKeyPath = "/podium.agent.v1.AgentService/ClearProviderKey"
	whoAmIPath           = "/podium.v1.IdentityService/WhoAmI"
)

// goodKey and badKey are obvious fakes. The fake Anthropic below knows the first and refuses
// everything else; no real provider key exists anywhere in this repository.
const (
	goodKey = "sk-ant-not-a-real-key-good"
	badKey  = "sk-ant-not-a-real-key-bad"
)

// startFakeAnthropic answers GET /v1/models the way the live API does — including the part
// that matters most here: an unknown key gets **400**, not 401.
func startFakeAnthropic(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != goodKey {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":` +
				`{"type":"authentication_error","message":"API key is invalid."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5"},{"id":"claude-sonnet-5"}],` +
			`"has_more":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// connectCall is one unary Connect-over-JSON call against the control plane, with whatever
// headers the caller wants — which is how the spoofing case is exercised.
func connectCall(t *testing.T, url, procedure, body string, header http.Header) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+procedure, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+devToken)
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, "", err.Error()
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), string(raw)
}

// agentProfileDir is the example profile docs/agent.md points at.
func agentProfileDir(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	require.NoError(t, err)
	return filepath.Join(root, "examples", "agent")
}

// startAgentForSettings brings up the conductor on a known address with key validation
// pointed at a fake, so podium-server can be configured to proxy to it before it starts.
func startAgentForSettings(t *testing.T, h *harness, addr, anthropicURL string) *agentProc {
	t.Helper()
	a := &agentProc{
		t:           t,
		h:           h,
		addr:        addr,
		databaseURL: newAgentDatabase(t),
		profileDir:  agentProfileDir(t),
		extraEnv:    []string{"PODIUM_AGENT_ANTHROPIC_BASE_URL=" + anthropicURL},
	}
	a.start()
	t.Cleanup(a.stop)
	a.awaitReady()
	return a
}

// ---------------------------------------------------------------------------
// The proxy and the settings RPCs
// ---------------------------------------------------------------------------

// TestAgentSettingsThroughTheServerProxy is the whole of step 18's server surface: the
// browser talks to podium-server, podium-server talks to the conductor, and the key ends up
// in the control plane's own secret store. No Docker and no node — nothing here runs a task.
func TestAgentSettingsThroughTheServerProxy(t *testing.T) {
	anthropic := startFakeAnthropic(t)
	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	agent := startAgentForSettings(t, h, agentAddr, anthropic.URL)

	// WhoAmI tells the UI whether to show the Agent screen at all.
	code, _, body := connectCall(t, h.url(), whoAmIPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"agentEnabled":true`)

	// Nothing is set yet, and GetSettings says so without touching the secret store.
	code, _, body = connectCall(t, h.url(), getSettingsPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, `"keySet":true`)
	assert.Contains(t, body, `"provider":"anthropic"`)

	// A key the provider refuses is refused here, and nothing is written anywhere.
	code, _, body = connectCall(t, h.url(), setProviderKeyPath, `{"key":"`+badKey+`"}`, nil)
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Contains(t, body, `"permission_denied"`)
	assert.Contains(t, body, "Anthropic rejected this key")
	// And *why*, in the provider's own words, all the way through the proxy: a Connect error
	// detail, which the proxy copies byte for byte because it copies the whole body. Without
	// it the operator is told a fixable key is dead.
	assert.Contains(t, body, "podium.agent.v1.ProviderKeyError")
	assert.Contains(t, body, "API key is invalid.")
	assert.NotContains(t, body, badKey, "the refused key must never come back in the error")
	assert.NotContains(t, h.podiumOK("secret", "ls"), anthropicKeySecret)
	code, _, body = connectCall(t, h.url(), getSettingsPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, `"keySet":true`)

	// A key the provider accepts is validated, stored, and proved with a model list. The
	// client also tries to say it is somebody else; the proxy strips that.
	spoof := http.Header{"X-Podium-Login": []string{"root"}}
	code, _, body = connectCall(t, h.url(), setProviderKeyPath, `{"key":"  `+goodKey+`  "}`, spoof)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"keySet":true`)
	assert.Contains(t, body, `"keyHint":"good"`)
	assert.Contains(t, body, "claude-opus-5")
	assert.Contains(t, body, `"setBy":"local"`, "the local transport has no per-user identity")
	assert.NotContains(t, body, "root", "a client-supplied X-Podium-Login must be dropped")
	assert.NotContains(t, body, goodKey, "the response must never carry the key back")

	// The key is now a Podium secret under the reserved name, and only its metadata is
	// visible: `podium secret ls` has no value column at all.
	secretList := h.podiumOK("secret", "ls")
	assert.Contains(t, secretList, anthropicKeySecret)
	assert.NotContains(t, secretList, goodKey)

	// It survives a reload of the page.
	code, _, body = connectCall(t, h.url(), getSettingsPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"keySet":true`)
	assert.Contains(t, body, `"keyHint":"good"`)

	// A direct call to the conductor with no bearer is refused: the proxy's token is the
	// only reason it trusts the login header at all.
	code, rawBody := agent.post(getSettingsPath, "", "{}")
	assert.Equal(t, http.StatusUnauthorized, code, rawBody)
	code, rawBody = agent.post(getSettingsPath, "wrong-token", "{}")
	assert.Equal(t, http.StatusUnauthorized, code, rawBody)

	// Removing it removes both halves, and doing it twice is not an error.
	for i := range 2 {
		code, _, body = connectCall(t, h.url(), clearProviderKeyPath, "{}", nil)
		require.Equal(t, http.StatusOK, code, "call %d: %s", i+1, body)
	}
	assert.NotContains(t, h.podiumOK("secret", "ls"), anthropicKeySecret)
	code, _, body = connectCall(t, h.url(), getSettingsPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, `"keySet":true`)

	// And with the conductor stopped the proxy answers a Connect error, not a bare 502:
	// the page renders "podium-agent is not reachable" instead of a parse failure.
	agent.stop()
	waitFor(t, 30*time.Second, "the proxy to report the conductor is down", func() bool {
		code, _, _ := connectCall(t, h.url(), getSettingsPath, "{}", nil)
		return code == http.StatusServiceUnavailable
	})
	code, ct, body := connectCall(t, h.url(), getSettingsPath, "{}", nil)
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	assert.Equal(t, "application/json", ct)
	var connectErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &connectErr), "body: %s", body)
	assert.Equal(t, "unavailable", connectErr.Code)
	assert.Equal(t, "podium-agent is not reachable", connectErr.Message)
}

// TestTheAgentAPIIsNotMountedWithoutAConductor is the other half of the mount decision. With
// PODIUM_AGENT_URL unset the prefix is never registered, so the path falls through to the SPA
// handler — a browser deep-linked to /agent gets the app, not a 404 — and WhoAmI tells that
// app to hide the screen.
func TestTheAgentAPIIsNotMountedWithoutAConductor(t *testing.T) {
	h := newHarness(t)

	code, _, body := connectCall(t, h.url(), whoAmIPath, "{}", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, `"agentEnabled":true`)

	// A GET, which is what a browser does. The UI handler serves only GET and HEAD.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url()+getSettingsPath, nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	page := string(raw)

	contentType := res.Header.Get("Content-Type")
	assert.NotContains(t, contentType, "application/json",
		"the prefix must not be answered by anything Connect-shaped: %s", page)
	if strings.Contains(contentType, "text/html") {
		assert.Equal(t, http.StatusOK, res.StatusCode)
		assert.Contains(t, strings.ToLower(page), "<!doctype html")
	} else {
		// A checkout that has never run `make web`. Still the UI handler, still not the proxy.
		assert.Contains(t, page, "built without the web UI", "unexpected body: %s", page)
	}
}

// ---------------------------------------------------------------------------
// The key set through the UI is the key a turn runs with
// ---------------------------------------------------------------------------

// TestAKeySetThroughTheUIAdmitsATurn closes the loop the acceptance list asks about: the
// secret SetProviderKey writes is the one the conductor's task spec names, so a turn is
// admitted afterwards and refused before. Nothing else in this step needs Docker; this does,
// because admission is a control-plane behaviour and a turn is a real task.
func TestAKeySetThroughTheUIAdmitsATurn(t *testing.T) {
	requireAgentRuntimeImage(t)

	anthropic := startFakeAnthropic(t)
	fake := fakes3.Start(t)
	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.S3 = fake.Config()
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	startNode(t, h)
	agent := startAgentForSettings(t, h, agentAddr, anthropic.URL)

	// No key yet. The spec still names the reserved secret, so the task is created and then
	// failed at admission — before any node sees it — and the conversation is told a
	// credential is missing rather than left silent.
	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"before the key","dry_run":true}`)
	waitFor(t, 2*time.Minute, "the keyless turn to fail", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "failed"
	}, func() string { return "agent log:\n" + agent.logs() })
	keyless := turnRows(t, agent.databaseURL, sourceKey)[0]
	require.NotEmpty(t, keyless.TaskID, "the task was created; admission is what refused it")
	assert.Contains(t, h.podiumOK("task", "get", keyless.TaskID), anthropicKeySecret,
		"the failure has to name the secret that is missing")

	// Set it the way an operator does: through the web UI's RPC, via the proxy.
	code, _, body := connectCall(t, h.url(), setProviderKeyPath, `{"key":"`+goodKey+`"}`, nil)
	require.Equal(t, http.StatusOK, code, body)

	// The same conversation now runs a turn to completion.
	second := agent.send(`{"channel":"C2","thread":"2.1","author":"alice",
		"text":"after the key","dry_run":true}`)
	waitFor(t, 3*time.Minute, "the turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, second)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return "agent log:\n" + agent.logs()
	})
	rows := turnRows(t, agent.databaseURL, second)
	require.Len(t, rows, 1)
	assert.Equal(t, "dry run: after the key", rows[0].FinalText)
	assert.NotEmpty(t, rows[0].TaskID, "the admitted turn has a task")

	requireNoPodiumResources(t)
}
