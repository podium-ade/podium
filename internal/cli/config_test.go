package cli

import (
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
