package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "podium"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "podium", "config.yaml"),
		[]byte("server: http://from-file:8080\ntoken: from-file\n"), 0o600))

	cfg, err := LoadConfig("", "")
	require.NoError(t, err)
	require.Equal(t, "http://from-file:8080", cfg.Server)
	require.Equal(t, "from-file", cfg.Token)

	t.Setenv("PODIUM_SERVER", "http://from-env:8080")
	t.Setenv("PODIUM_TOKEN", "from-env")
	cfg, err = LoadConfig("", "")
	require.NoError(t, err)
	require.Equal(t, "http://from-env:8080", cfg.Server)
	require.Equal(t, "from-env", cfg.Token)

	cfg, err = LoadConfig("http://from-flag:8080", "from-flag")
	require.NoError(t, err)
	require.Equal(t, "http://from-flag:8080", cfg.Server, "flags win over the environment")
	require.Equal(t, "from-flag", cfg.Token)
}

func TestLoadConfigNeedsAToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := LoadConfig("", "")
	require.ErrorContains(t, err, "no token")

	cfg, err := LoadConfig("", "t")
	require.NoError(t, err)
	require.Equal(t, DefaultServer, cfg.Server, "the loopback server is the default")
}

// Over the tailnet the server serves HTTPS on its MagicDNS name and Tailscale names the caller,
// so `podium --server https://podium.<tailnet>.ts.net nodes` must work with no token at all.
func TestLoadConfigNeedsNoTokenOverTheTailnet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := LoadConfig("https://podium.taila79bf6.ts.net", "")
	require.NoError(t, err)
	require.Empty(t, cfg.Token)
	require.True(t, cfg.Tailnet())

	// And the client it builds carries no Authorization header.
	rec := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := httpClientFor(cfg)
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := client.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	require.Empty(t, <-rec)
}

func TestLoadConfigStillRequiresATokenOverPlainHTTP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := LoadConfig("http://127.0.0.1:8080", "")
	require.ErrorContains(t, err, "no token")
}
