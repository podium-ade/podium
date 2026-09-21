package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><html><head>\n<title>Podium</title>\n</head><body></body></html>")},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

func TestUIHandlerServesTheBundle(t *testing.T) {
	h := &uiHandler{assets: testAssets()}

	res := get(t, h, "/")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, indexCache, res.Header.Get("Cache-Control"))
	require.Contains(t, res.Header.Get("Content-Type"), "text/html")
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	requireCSPNonce(t, res.Header.Get("Content-Security-Policy"), body)

	res = get(t, h, "/assets/index-abc123.js")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, immutableCache, res.Header.Get("Cache-Control"),
		"hashed assets are immutable; index.html never is")
	require.Equal(t, contentSecurityPolicy(""), res.Header.Get("Content-Security-Policy"),
		"hashed assets do not mint a style nonce; they cannot inject a <style> tag")
}

func TestUIHandlerMintsAFreshStyleNoncePerIndex(t *testing.T) {
	h := &uiHandler{assets: testAssets()}
	a := get(t, h, "/")
	b := get(t, h, "/nodes")
	bodyA, err := io.ReadAll(a.Body)
	require.NoError(t, err)
	bodyB, err := io.ReadAll(b.Body)
	require.NoError(t, err)
	nonceA := requireCSPNonce(t, a.Header.Get("Content-Security-Policy"), bodyA)
	nonceB := requireCSPNonce(t, b.Header.Get("Content-Security-Policy"), bodyB)
	require.NotEqual(t, nonceA, nonceB)
}

func TestInjectStyleNonceLeavesHTMLAloneWhenHeadIsMissing(t *testing.T) {
	in := []byte("<!doctype html><title>Podium</title>")
	require.Equal(t, in, injectStyleNonce(in, "abc"))
}

func TestUIHandlerFallsBackToIndexForClientRoutes(t *testing.T) {
	h := &uiHandler{assets: testAssets()}

	for _, path := range []string{"/nodes", "/tasks/task_01j", "/tasks/task_01j/anything"} {
		res := get(t, h, path)
		require.Equal(t, http.StatusOK, res.StatusCode, path)
		require.Equal(t, indexCache, res.Header.Get("Cache-Control"), path)
		require.Contains(t, res.Header.Get("Content-Type"), "text/html", path)
	}
}

func TestUIHandlerDoesNotFallBackForMissingAssets(t *testing.T) {
	h := &uiHandler{assets: testAssets()}
	// Answering a missing script with index.html would hand the browser HTML where it asked
	// for JavaScript, which fails silently in the console.
	require.Equal(t, http.StatusNotFound, get(t, h, "/assets/gone-000000.js").StatusCode)
}

func TestUIHandlerRejectsWrites(t *testing.T) {
	h := &uiHandler{assets: testAssets()}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}

func TestUIHandlerWithoutAssetsSaysSo(t *testing.T) {
	h := newUIHandler(fstest.MapFS{}, slog.New(slog.DiscardHandler))
	res := get(t, h, "/")
	require.Equal(t, http.StatusNotFound, res.StatusCode)
}

var styleNonceRe = regexp.MustCompile(`style-src 'self' 'nonce-([A-Za-z0-9_-]+)'`)

func requireCSPNonce(t *testing.T, csp string, body []byte) string {
	t.Helper()
	require.Contains(t, csp, "script-src 'self'")
	require.NotContains(t, csp, "unsafe-inline")
	require.NotContains(t, csp, "unsafe-eval")
	m := styleNonceRe.FindStringSubmatch(csp)
	require.Len(t, m, 2, "index responses mint a style nonce for CodeMirror's <style> tag")
	nonce := m[1]
	require.Contains(t, string(body), `meta name="`+cspNonceMetaName+`" content="`+nonce+`"`)
	return nonce
}
