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
			"local_token: from-file\n"), 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "http://from-file:8080", cfg.Server)
	require.Equal(t, []string{"linux/arm64", "browser"}, cfg.Labels)
	require.Equal(t, 2, cfg.MaxTasks)

	t.Setenv("PODIUM_NODE_SERVER", "http://from-env:9090")
	t.Setenv("PODIUM_NODE_LABELS", "gpu, linux/arm64 ,")
	t.Setenv("PODIUM_NODE_MAX_TASKS", "7")
	t.Setenv("PODIUM_NODE_LOCAL_TOKEN", "from-env")

	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "http://from-env:9090", cfg.Server, "the environment overlays the file")
	require.Equal(t, []string{"gpu", "linux/arm64"}, cfg.Labels)
	require.Equal(t, 7, cfg.MaxTasks)
	require.Equal(t, "from-env", cfg.LocalToken)
	require.Equal(t, "/var/lib/from-file", cfg.DataDir, "an unset variable leaves the file alone")
}

// TestEnvironmentOnlyConfiguration is the shape the MVP-0 acceptance script uses: the
// five PODIUM_NODE_* variables and no config file at all.
func TestEnvironmentOnlyConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PODIUM_NODE_SERVER", "http://127.0.0.1:8080")
	t.Setenv("PODIUM_NODE_TRANSPORT", "dev")
	t.Setenv("PODIUM_NODE_LOCAL_TOKEN", "devtoken")
	t.Setenv("PODIUM_NODE_ENROLL_TOKEN", "etok-fixture")
	t.Setenv("PODIUM_NODE_DATA_DIR", dir)

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", cfg.Server)
	require.Equal(t, "dev", cfg.Transport)
	require.Equal(t, "devtoken", cfg.LocalToken)
	require.Equal(t, "etok-fixture", cfg.EnrollToken)
	require.Equal(t, dir, cfg.DataDir)
	require.NoError(t, cfg.Validate())
}

func TestValidateRefusesToStart(t *testing.T) {
	base := func() Config {
		c := DefaultConfig()
		c.DataDir = t.TempDir()
		c.LocalToken = "devtoken"
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
		c.LocalToken = ""
		require.ErrorContains(t, c.Validate(), "PODIUM_NODE_LOCAL_TOKEN")
	})

	t.Run("no slots", func(t *testing.T) {
		c := base()
		c.MaxTasks = 0
		require.ErrorContains(t, c.Validate(), "never gets work")
	})

	t.Run("tailnet needs an https server", func(t *testing.T) {
		c := base()
		c.Transport = TransportTailnet
		err := c.Validate()
		require.ErrorContains(t, err, "MagicDNS")
		require.ErrorContains(t, err, "https://podium.")
	})

	t.Run("tailnet with an https server is accepted", func(t *testing.T) {
		c := base()
		c.Transport = TransportTailnet
		c.Server = "https://podium.taila79bf6.ts.net"
		c.LocalToken = ""
		require.NoError(t, c.Validate(), "the tailnet transport needs no dev token")
	})

	t.Run("host with an https server is accepted", func(t *testing.T) {
		c := base()
		c.Transport = TransportHost
		c.Server = "https://podium.taila79bf6.ts.net"
		c.LocalToken = ""
		require.NoError(t, c.Validate())
	})

	t.Run("dev refuses an https server", func(t *testing.T) {
		c := base()
		c.Server = "https://podium.taila79bf6.ts.net"
		require.ErrorContains(t, c.Validate(), "transport: tailnet")
	})

	t.Run("an unknown transport names the three that exist", func(t *testing.T) {
		c := base()
		c.Transport = "carrier-pigeon"
		err := c.Validate()
		require.ErrorContains(t, err, "is not a transport")
		require.ErrorContains(t, err, "tailnet")
		require.ErrorContains(t, err, "host")
	})
}

func TestTailnetEnvironment(t *testing.T) {
	t.Setenv("PODIUM_NODE_TRANSPORT", "tailnet")
	t.Setenv("PODIUM_NODE_SERVER", "https://podium.taila79bf6.ts.net")
	t.Setenv("PODIUM_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PODIUM_NODE_TS_HOSTNAME", "podium-node-podiumbot1")
	t.Setenv("TS_AUTHKEY", "tskey-auth-fallback")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, TransportTailnet, cfg.Transport)
	require.Equal(t, "podium-node-podiumbot1", cfg.TSHostname)
	require.Equal(t, "tskey-auth-fallback", cfg.TSAuthKey, "TS_AUTHKEY is honoured as a fallback")
	require.NoError(t, cfg.Validate())
	require.Equal(t, filepath.Join(cfg.DataDir, "ts"), cfg.TSStateDir())

	// The PODIUM_NODE_ prefixed name wins when both are set.
	t.Setenv("PODIUM_NODE_TS_AUTHKEY", "tskey-auth-specific")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, "tskey-auth-specific", cfg.TSAuthKey)
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

// TestAllowPrivilegedSidecarsIsOffUnlessAskedFor. The default matters more than the
// override: a node that had it on by accident would run a spec's docker-in-docker sidecar
// as root on its own kernel.
func TestAllowPrivilegedSidecarsIsOffUnlessAskedFor(t *testing.T) {
	require.False(t, DefaultConfig().AllowPrivilegedSidecars)

	path := filepath.Join(t.TempDir(), "node.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server: http://127.0.0.1:8080\n"), 0o600))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.False(t, cfg.AllowPrivilegedSidecars, "a file that says nothing means no")

	t.Setenv("PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS", "true")
	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.True(t, cfg.AllowPrivilegedSidecars)

	require.NoError(t, os.WriteFile(path, []byte(
		"server: http://127.0.0.1:8080\nallow_privileged_sidecars: true\n"), 0o600))
	t.Setenv("PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS", "false")
	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.False(t, cfg.AllowPrivilegedSidecars, "the environment overlays the file both ways")
}
