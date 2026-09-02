package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/transport/dev"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("PODIUM_DATABASE_URL", "postgres://podium:podium@127.0.0.1:5432/podium")
	t.Setenv("PODIUM_TRANSPORT", "")
	t.Setenv("PODIUM_DEV_LISTEN", "")
	t.Setenv("PODIUM_DEV_TOKEN", "devtoken")

	cfg := ConfigFromEnv()
	require.Equal(t, TransportDev, cfg.Transport)
	require.Equal(t, dev.DefaultListen, cfg.DevListen)
	require.NoError(t, cfg.Validate())
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

	tailnet := base
	tailnet.Transport = TransportTailnet
	require.ErrorContains(t, tailnet.Validate(), "not implemented")

	bogus := base
	bogus.Transport = "carrier-pigeon"
	require.ErrorContains(t, bogus.Validate(), "is not a transport")
}
