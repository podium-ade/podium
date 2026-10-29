package ghreview

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mcpRequest(t *testing.T, h http.Handler, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, MCPPath, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json" {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec.Code, out
}

func toolText(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	require.True(t, ok, "not a result: %v", res)
	content := result["content"].([]any)
	return content[0].(map[string]any)["text"].(string), result["isError"].(bool)
}

// The turn's own token, and only its pull request: a token that names no live turn on a pull
// request reads nothing.
func TestThePullRequestServerAnswersOnlyItsOwnTurn(t *testing.T) {
	h := newHarness(t)
	srv := MCPHandler(func(token string) (string, bool) {
		if token == "tok" {
			return "github:acme/repo#12", true
		}
		return "", false
	}, func() *Source { return h.src })

	code, _ := mcpRequest(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, code)
	code, _ = mcpRequest(t, srv, "other", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, code)

	code, res := mcpRequest(t, srv, "tok", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "2025-03-26", res["result"].(map[string]any)["protocolVersion"])

	code, _ = mcpRequest(t, srv, "tok", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	assert.Equal(t, http.StatusAccepted, code)

	_, res = mcpRequest(t, srv, "tok", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tool := range res["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"files", "diff", "file"}, names)
}

func TestThePullRequestServerReadsTheDiffAndTheHead(t *testing.T) {
	h := newHarness(t)
	h.stub.mu.Lock()
	h.stub.pullFiles = []PullFile{
		{Filename: "a.go", Status: "modified", Additions: 2, Deletions: 1, Patch: "@@ -1 +1,2 @@\n-x\n+y\n+z"},
		{Filename: "logo.png", Status: "added"},
	}
	h.stub.files = map[string]string{"a.go@0123456789abcdef": "package a\n\nfunc Y() {}\nfunc Z() {}\n"}
	h.stub.mu.Unlock()
	srv := MCPHandler(func(string) (string, bool) { return "github:acme/repo#12", true }, func() *Source { return h.src })
	call := func(name, args string) (string, bool) {
		_, res := mcpRequest(t, srv, "tok",
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		return toolText(t, res)
	}

	text, failed := call("files", `{}`)
	require.False(t, failed)
	assert.Equal(t, "modified a.go +2 -1\nadded logo.png +0 -0\n", text)

	text, failed = call("diff", `{"path":"a.go"}`)
	require.False(t, failed)
	assert.Contains(t, text, "diff --git a/a.go b/a.go\n@@ -1 +1,2 @@")
	assert.NotContains(t, text, "logo.png")
	text, _ = call("diff", `{}`)
	assert.Contains(t, text, "binary, or too large")

	text, failed = call("file", `{"path":"a.go","start_line":3,"end_line":4}`)
	require.False(t, failed)
	assert.Equal(t, "3: func Y() {}\n4: func Z() {}\n", text, "read at the pull request's head commit")

	text, failed = call("file", `{"path":"gone.go"}`)
	assert.True(t, failed, "a tool that fails says why, as a result the model reads")
	assert.Contains(t, text, "does not exist at the head commit")

	_, failed = call("delete_branch", `{}`)
	assert.True(t, failed, "there is nothing here that writes")
}

func TestThePullRequestServerCutsALargeAnswer(t *testing.T) {
	out := capped(strings.Repeat("x", mcpMaxResult+10), "ask for one path")
	assert.True(t, strings.HasSuffix(out, "(cut: 10 more bytes — ask for one path)\n"))
	assert.Equal(t, "(the file has 2 lines)\n", numbered("a\nb\n", 5, 0))
}
