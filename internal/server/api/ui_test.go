package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>Podium</title>")},
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
	require.Equal(t, contentSecurityPolicy, res.Header.Get("Content-Security-Policy"))
	require.Contains(t, res.Header.Get("Content-Type"), "text/html")

	res = get(t, h, "/assets/index-abc123.js")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, immutableCache, res.Header.Get("Cache-Control"),
		"hashed assets are immutable; index.html never is")
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
