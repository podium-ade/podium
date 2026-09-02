package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaultsWithoutAFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	require.Error(t, err, "a config file that was asked for by name must exist")

	// The default path is allowed to be missing: environment-only nodes are supported.
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	if _, statErr := os.Stat(DefaultConfigPath); statErr == nil {
		t.Skipf("this host has a real %s; the defaults cannot be asserted", DefaultConfigPath)
	}
	require.Equal(t, DefaultServer, cfg.Server)
	require.Equal(t, DefaultTransport, cfg.Transport)
	require.Equal(t, DefaultDataDir, cfg.DataDir)
	require.Equal(t, DefaultMaxTasks, cfg.MaxTasks)
	require.Equal(t, DefaultMetricsListen, cfg.MetricsListen)
	require.InDelta(t, DefaultImageCacheHighWatermark, cfg.ImageCacheHighWatermark, 1e-9)
}

func TestLoadConfigFileThenEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"server: http://from-file:8080\n"+
			"transport: dev\n"+
			"data_dir: /var/lib/from-file\n"+
			"labels: [linux/arm64, browser]\n"+
			"max_tasks: 2\n"+
			"dev_token: from-file\n"), 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "http://from-file:8080", cfg.Server)
	require.Equal(t, []string{"linux/arm64", "browser"}, cfg.Labels)
	require.Equal(t, 2, cfg.MaxTasks)

	t.Setenv("PODIUM_NODE_SERVER", "http://from-env:9090")
	t.Setenv("PODIUM_NODE_LABELS", "gpu, linux/arm64 ,")
	t.Setenv("PODIUM_NODE_MAX_TASKS", "7")
	t.Setenv("PODIUM_NODE_DEV_TOKEN", "from-env")

	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "http://from-env:9090", cfg.Server, "the environment overlays the file")
	require.Equal(t, []string{"gpu", "linux/arm64"}, cfg.Labels)
	require.Equal(t, 7, cfg.MaxTasks)
	require.Equal(t, "from-env", cfg.DevToken)
	require.Equal(t, "/var/lib/from-file", cfg.DataDir, "an unset variable leaves the file alone")
}

// TestEnvironmentOnlyConfiguration is the shape the MVP-0 acceptance script uses: the
// five PODIUM_NODE_* variables and no config file at all.
func TestEnvironmentOnlyConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PODIUM_NODE_SERVER", "http://127.0.0.1:8080")
	t.Setenv("PODIUM_NODE_TRANSPORT", "dev")
	t.Setenv("PODIUM_NODE_DEV_TOKEN", "devtoken")
	t.Setenv("PODIUM_NODE_ENROLL_TOKEN", "etok-fixture")
	t.Setenv("PODIUM_NODE_DATA_DIR", dir)

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", cfg.Server)
	require.Equal(t, "dev", cfg.Transport)
	require.Equal(t, "devtoken", cfg.DevToken)
	require.Equal(t, "etok-fixture", cfg.EnrollToken)
	require.Equal(t, dir, cfg.DataDir)
	require.NoError(t, cfg.Validate())
}

func TestValidateRefusesToStart(t *testing.T) {
	base := func() Config {
		c := DefaultConfig()
		c.DataDir = t.TempDir()
		c.DevToken = "devtoken"
		return c
	}

	t.Run("empty server", func(t *testing.T) {
		c := base()
		c.Server = ""
		err := c.Validate()
		require.ErrorContains(t, err, "server is empty")
		require.ErrorContains(t, err, "PODIUM_NODE_SERVER")
	})

	t.Run("server without a scheme", func(t *testing.T) {
		c := base()
		c.Server = "127.0.0.1:8080"
		require.ErrorContains(t, c.Validate(), "http:// or https://")
	})

	t.Run("empty data dir", func(t *testing.T) {
		c := base()
		c.DataDir = ""
		err := c.Validate()
		require.ErrorContains(t, err, "data_dir is empty")
		require.ErrorContains(t, err, "PODIUM_NODE_DATA_DIR")
	})

	t.Run("data dir that cannot be written", func(t *testing.T) {
		parent := t.TempDir()
		require.NoError(t, os.Chmod(parent, 0o500))
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

		c := base()
		c.DataDir = filepath.Join(parent, "podium")
		err := c.Validate()
		require.ErrorContains(t, err, "not usable")
		require.ErrorContains(t, err, "PODIUM_NODE_DATA_DIR")
	})

	t.Run("no dev token", func(t *testing.T) {
		c := base()
		c.DevToken = ""
		require.ErrorContains(t, c.Validate(), "PODIUM_NODE_DEV_TOKEN")
	})

	t.Run("no slots", func(t *testing.T) {
		c := base()
		c.MaxTasks = 0
		require.ErrorContains(t, c.Validate(), "never gets work")
	})

	t.Run("tailnet is not implemented", func(t *testing.T) {
		c := base()
		c.Transport = TransportTailnet
		require.ErrorContains(t, c.Validate(), "step 11")
	})
}

func TestIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()

	_, ok, err := LoadIdentity(dir)
	require.NoError(t, err)
	require.False(t, ok, "a fresh data dir has no identity")

	want := Identity{NodeID: "node_01jfixture", NodeKey: "not-a-real-key"}
	require.NoError(t, SaveIdentity(dir, want))

	info, err := os.Stat(filepath.Join(dir, identityFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the node key is on disk; it must not be world readable")

	got, ok, err := LoadIdentity(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, got)

	require.NoError(t, os.WriteFile(filepath.Join(dir, identityFile), []byte(`{"node_id":"node_x"}`), 0o600))
	_, _, err = LoadIdentity(dir)
	require.ErrorContains(t, err, "incomplete")
}
