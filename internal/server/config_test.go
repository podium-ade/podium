package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/transport/dev"
	tsnet "github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("PODIUM_DATABASE_URL", "postgres://podium:podium@127.0.0.1:5432/podium")
	t.Setenv("PODIUM_TRANSPORT", "")
	t.Setenv("PODIUM_DEV_LISTEN", "")
	t.Setenv("PODIUM_DEV_TOKEN", "devtoken")

	cfg := ConfigFromEnv()
	require.Equal(t, TransportDev, cfg.Transport)
	require.Equal(t, dev.DefaultListen, cfg.DevListen)
	require.Equal(t, tsnet.DefaultHostname, cfg.TSHostname)
	require.Equal(t, tsnet.DefaultStateDir, cfg.TSStateDir)
	require.Equal(t, tsnet.DefaultNodeTag, cfg.TSRequiredNodeTag)
	require.False(t, cfg.TSAllowUntaggedNodes)
	require.NoError(t, cfg.Validate())
}

func TestConfigFromEnvTailnet(t *testing.T) {
	t.Setenv("PODIUM_DATABASE_URL", "postgres://podium:podium@127.0.0.1:5432/podium")
	t.Setenv("PODIUM_TRANSPORT", "tailnet")
	t.Setenv("PODIUM_TS_HOSTNAME", "podium-staging")
	t.Setenv("PODIUM_TS_STATE_DIR", "/srv/podium/ts")
	t.Setenv("TS_AUTHKEY", "tskey-auth-notreal")
	t.Setenv("PODIUM_TS_REQUIRED_NODE_TAG", "tag:worker")
	t.Setenv("PODIUM_TS_ALLOW_UNTAGGED_NODES", "true")

	cfg := ConfigFromEnv()
	require.Equal(t, TransportTailnet, cfg.Transport)
	require.Equal(t, "podium-staging", cfg.TSHostname)
	require.Equal(t, "/srv/podium/ts", cfg.TSStateDir)
	require.Equal(t, "tskey-auth-notreal", cfg.TSAuthKey)
	require.Equal(t, "tag:worker", cfg.TSRequiredNodeTag)
	require.True(t, cfg.TSAllowUntaggedNodes)
	// The tailnet transport needs no dev token, and does not want one.
	require.NoError(t, cfg.Validate())
}

// A knob that weakens authentication must not turn itself on because someone wrote "yes".
func TestAllowUntaggedNodesOnlyAcceptsABoolean(t *testing.T) {
	t.Setenv("PODIUM_DATABASE_URL", "postgres://x")
	for _, v := range []string{"", "yes", "on", "sure", "0", "false"} {
		t.Setenv("PODIUM_TS_ALLOW_UNTAGGED_NODES", v)
		require.False(t, ConfigFromEnv().TSAllowUntaggedNodes, "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		t.Setenv("PODIUM_TS_ALLOW_UNTAGGED_NODES", v)
		require.True(t, ConfigFromEnv().TSAllowUntaggedNodes, "value %q", v)
	}
}

func TestConfigValidate(t *testing.T) {
	base := Config{DatabaseURL: "postgres://x", Transport: TransportDev, DevToken: "t"}

	require.NoError(t, base.Validate())

	noURL := base
	noURL.DatabaseURL = ""
	require.ErrorContains(t, noURL.Validate(), "PODIUM_DATABASE_URL")

	noToken := base
	noToken.DevToken = ""
	require.ErrorContains(t, noToken.Validate(), "PODIUM_DEV_TOKEN")

	ts := base
	ts.Transport = TransportTailnet
	ts.TSHostname = "podium"
	ts.TSStateDir = "/var/lib/podium/tsnet"
	ts.DevToken = ""
	require.NoError(t, ts.Validate())

	noHostname := ts
	noHostname.TSHostname = ""
	require.ErrorContains(t, noHostname.Validate(), "PODIUM_TS_HOSTNAME")

	noStateDir := ts
	noStateDir.TSStateDir = ""
	require.ErrorContains(t, noStateDir.Validate(), "PODIUM_TS_STATE_DIR")

	host := base
	host.Transport = TransportHost
	host.DevToken = ""
	require.NoError(t, host.Validate())

	bogus := base
	bogus.Transport = "carrier-pigeon"
	require.ErrorContains(t, bogus.Validate(), "is not a transport")
}

func TestAgentProxyConfig(t *testing.T) {
	base := Config{DatabaseURL: "postgres://x", Transport: TransportDev, DevToken: "t"}

	// Unset is the normal case: a control plane with no conductor.
	require.NoError(t, base.Validate())
	require.False(t, base.AgentEnabled())

	ok := base
	ok.AgentURL = "http://127.0.0.1:8090"
	ok.AgentToken = "agenttoken"
	require.NoError(t, ok.Validate())
	require.True(t, ok.AgentEnabled())

	// A trailing slash is a host, not a path.
	slash := ok
	slash.AgentURL = "http://127.0.0.1:8090/"
	require.NoError(t, slash.Validate())

	// A proxy that forwards an unauthenticated request into the conductor is worse than no
	// proxy: the bearer is the conductor's only reason to trust the login header.
	noToken := ok
	noToken.AgentToken = ""
	require.ErrorContains(t, noToken.Validate(), "PODIUM_AGENT_TOKEN is required")

	for _, bad := range []string{
		"127.0.0.1:8090",              // no scheme
		"ftp://127.0.0.1:8090",        // not HTTP
		"http://",                     // no host
		"http://127.0.0.1:8090/agent", // the proxy owns the path
		"http://127.0.0.1:8090/?x=1",  // and the query
	} {
		c := ok
		c.AgentURL = bad
		require.Error(t, c.Validate(), "PODIUM_AGENT_URL=%q must be refused", bad)
	}

	// With no URL the token is ignored rather than being a half-configuration.
	tokenOnly := base
	tokenOnly.AgentToken = "agenttoken"
	require.NoError(t, tokenOnly.Validate())
	require.False(t, tokenOnly.AgentEnabled())
}

func TestConfigFromEnvReadsTheAgentProxy(t *testing.T) {
	t.Setenv("PODIUM_AGENT_URL", "http://127.0.0.1:8090")
	t.Setenv("PODIUM_AGENT_TOKEN", "agenttoken")
	cfg := ConfigFromEnv()
	require.Equal(t, "http://127.0.0.1:8090", cfg.AgentURL)
	require.Equal(t, "agenttoken", cfg.AgentToken)
}
