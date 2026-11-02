package conductor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatConductor(t *testing.T) *Conductor {
	t.Helper()
	return &Conductor{
		host: &HostRuntime{Credential: func(_ context.Context, provider string) (string, error) {
			return "real-key-for-" + provider, nil
		}},
		chatSlots:   make(chan struct{}, 2),
		chatPerUser: 1,
	}
}

// TestTheGatewaySpendsTheConductorsCredentialOnlyDuringATurn: a conversation's token reaches
// its provider with the conductor's key in the provider's own header, nothing opencode added
// about the session, and only while the conversation is running a turn.
func TestTheGatewaySpendsTheConductorsCredentialOnlyDuringATurn(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	c := chatConductor(t)
	cv, err := c.chatConvFor("sess_1")
	require.NoError(t, err)
	cv.provider, cv.upstream = "anthropic", upstream.URL+"/v1"
	gw := httptest.NewServer(c.ChatHostGateway())
	t.Cleanup(gw.Close)

	send := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, gw.URL+chatGatewayPath+token+"/messages?key=leak", strings.NewReader("{}"))
		req.Header.Set("x-api-key", "podium")
		req.Header.Set("x-opencode-session-id", "ses_x")
		req.Header.Set("traceparent", "00-x")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusUnauthorized, send(cv.token), "between turns the conversation spends nothing")
	assert.Equal(t, http.StatusUnauthorized, send("not-a-token"))
	assert.Nil(t, got)

	cv.turn = &chatTurn{id: "turn_1"}
	assert.Equal(t, http.StatusOK, send(cv.token))
	require.NotNil(t, got)
	assert.Equal(t, "/v1/messages", got.URL.Path)
	assert.Empty(t, got.URL.Query().Get("key"))
	assert.Equal(t, "real-key-for-anthropic", got.Header.Get("x-api-key"))
	assert.Empty(t, got.Header.Get("x-opencode-session-id"))
	assert.Empty(t, got.Header.Get("traceparent"))
}

func TestEachProviderGetsItsKeyItsOwnWay(t *testing.T) {
	for provider, header := range map[string]string{"anthropic": "x-api-key", "google": "x-goog-api-key", "xai": "Authorization"} {
		h := http.Header{}
		setProviderAuth(h, provider, "k")
		assert.NotEmpty(t, h.Get(header), provider)
	}
	claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_1"}})
	jwt := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
	h := http.Header{}
	setProviderAuth(h, "openai", jwt)
	assert.Equal(t, "Bearer "+jwt, h.Get("Authorization"))
	assert.Equal(t, "acct_1", h.Get("ChatGPT-Account-Id"), "a ChatGPT sign-in names the account Codex bills")
	h = http.Header{}
	setProviderAuth(h, "openai", "sk-plain")
	assert.Empty(t, h.Get("ChatGPT-Account-Id"))
}

func mcpCall(t *testing.T, srv *httptest.Server, path, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return map[string]any{"status": resp.StatusCode}
	}
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// TestTheToolsActForTheRunningTurnAndNoOther: a tool call names its conversation by token, is
// refused between turns, and the title it sets is the running turn's.
func TestTheToolsActForTheRunningTurnAndNoOther(t *testing.T) {
	c := chatConductor(t)
	cv, err := c.chatConvFor("sess_1")
	require.NoError(t, err)
	srv := httptest.NewServer(c.ChatHostMCP(nil))
	t.Cleanup(srv.Close)
	titlePath := chatMCPPath + cv.token + "/" + chatTitleServer

	assert.Equal(t, http.StatusUnauthorized, mcpCall(t, srv, titlePath, "tools/list", nil)["status"])

	cv.turn = &chatTurn{id: "turn_1"}
	list := mcpCall(t, srv, titlePath, "tools/list", nil)
	assert.Contains(t, list["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)["name"], "set")
	mcpCall(t, srv, titlePath, "tools/call", map[string]any{"name": "set",
		"arguments": map[string]any{"title": "\"A very long title that goes on and on well past the limit\"\nsecond line"}})
	assert.Equal(t, "A very long title that goes on and on well past ", cv.turn.title)

	assert.Equal(t, http.StatusNotFound, mcpCall(t, srv, chatMCPPath+cv.token+"/"+chatDelegateServer, "tools/list", nil)["status"],
		"a turn that may not delegate has no delegation server")
	assert.Equal(t, http.StatusUnauthorized, mcpCall(t, srv, chatMCPPath+"someone-else/"+chatTitleServer, "tools/list", nil)["status"])
}

// TestOnePersonCannotHoldEverySlot: past their own limit a person's turn waits, and frees the
// moment one of theirs ends; another person is not held up by them.
func TestOnePersonCannotHoldEverySlot(t *testing.T) {
	c := chatConductor(t)
	release, ok := c.acquireUserSlot(context.Background(), "alice")
	require.True(t, ok)

	waiting, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, ok = c.acquireUserSlot(waiting, "alice")
	assert.False(t, ok, "alice is at her limit")

	other, ok := c.acquireUserSlot(context.Background(), "bob")
	require.True(t, ok, "bob is not")
	other()

	release()
	again, ok := c.acquireUserSlot(context.Background(), "alice")
	require.True(t, ok)
	again()
}

func TestALocationNameIsSafeAsADirectory(t *testing.T) {
	assert.Equal(t, "conv-sess_01ab-cx", chatLocationName("sess_01ab-c/../x"), "no separator or dot survives")
}

// TestTwoPeopleOnOneHostAreBilledToTheirOwnKeys: a person who set their own key for the
// provider pays for their turns with it; one who did not is on the company's.
func TestTwoPeopleOnOneHostAreBilledToTheirOwnKeys(t *testing.T) {
	var keys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-api-key"))
	}))
	t.Cleanup(upstream.Close)
	c := chatConductor(t)
	c.host.PersonalCredential = func(_ context.Context, login, provider string) (string, error) {
		if login == "ada@acme.com" {
			return "ada-own-" + provider, nil
		}
		return "", nil
	}
	gw := httptest.NewServer(c.ChatHostGateway())
	t.Cleanup(gw.Close)

	for _, login := range []string{"ada@acme.com", "bob@acme.com"} {
		cv, err := c.chatConvFor("sess_" + login)
		require.NoError(t, err)
		cv.provider, cv.upstream = "anthropic", upstream.URL
		cv.turn = &chatTurn{id: "turn", login: login}
		resp, err := http.Post(gw.URL+chatGatewayPath+cv.token+"/messages", "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	assert.Equal(t, []string{"ada-own-anthropic", "real-key-for-anthropic"}, keys)
}
