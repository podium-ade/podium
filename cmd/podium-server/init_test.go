package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server"
)

// requiredByCompose is every variable the compose files interpolate with `:?`, which makes
// `docker compose up` fail outright when it has no value. `init` prints "Next: docker compose
// up" as its last line, so anything in here that init does not mint is a fresh clone that
// cannot start — which is exactly what PODIUM_AGENT_TOKEN was.
//
// TS_AUTHKEY and PODIUM_NODE_TS_AUTHKEY are deliberately absent: they are Tailscale's keys,
// only the operator can produce them, and init writes them empty and says so.
var requiredByCompose = map[string][]string{
	server.TransportLocal: {
		"PODIUM_PG_PASSWORD",
		"PODIUM_LOCAL_TOKEN",
		"PODIUM_S3_SECRET_KEY",
		"PODIUM_AGENT_TOKEN",
	},
	server.TransportTailnet: {
		"PODIUM_PG_PASSWORD",
		"PODIUM_S3_SECRET_KEY",
		"PODIUM_AGENT_TOKEN",
		"PODIUM_TAILNET",
	},
}

func TestInitMintsEveryValueComposeRefusesToStartWithout(t *testing.T) {
	for transport, required := range requiredByCompose {
		t.Run(transport, func(t *testing.T) {
			env := parseEnv(t, renderEnv(initSecrets{
				Transport:   transport,
				Tailnet:     "tail0a1b2c",
				PGPassword:  "pg",
				LocalToken:  "dev",
				S3SecretKey: "s3",
				AgentToken:  "agent",
			}))
			for _, name := range required {
				require.NotEmpty(t, env[name],
					"%s is required by docker-compose with :? — `podium-server init` must mint it, "+
						"or the `docker compose up` that init tells you to run next fails", name)
			}
		})
	}
}

// TestInitLeavesTailscaleKeysEmpty guards the other half: a value only the operator can
// supply must be present and blank, not absent, so there is a line to fill in.
func TestInitLeavesTailscaleKeysEmpty(t *testing.T) {
	body := renderEnv(initSecrets{Transport: server.TransportTailnet, Tailnet: "tail0a1b2c"})
	for _, name := range []string{"TS_AUTHKEY", "PODIUM_NODE_TS_AUTHKEY", "PODIUM_NODE_ENROLL_TOKEN"} {
		require.Contains(t, body, name+"=\n", "%s should be present and empty to be filled in", name)
	}
}

// TestInitConfiguresTheCLI guards the reason the quickstart can say "source this file and
// you are done". The CLI needs an address and a token: the address is written here, and the
// token is PODIUM_LOCAL_TOKEN, which the CLI reads directly. Writing a second copy under
// PODIUM_TOKEN would only create a pair that can drift apart.
func TestInitConfiguresTheCLI(t *testing.T) {
	env := parseEnv(t, renderEnv(initSecrets{
		Transport: server.TransportLocal, PGPassword: "pg", LocalToken: "dev",
		S3SecretKey: "s3", AgentToken: "agent",
	}))
	require.NotEmpty(t, env["PODIUM_SERVER"])
	require.NotEmpty(t, env["PODIUM_LOCAL_TOKEN"])
	require.NotContains(t, env, "PODIUM_TOKEN", "the CLI reads PODIUM_LOCAL_TOKEN; one secret, one line")
}

// TestInitCommandWritesUsableCredentials runs the command itself, because the bug this
// guards was in the generation loop rather than in the rendering: renderEnv would have
// happily written PODIUM_AGENT_TOKEN= with nothing after it.
func TestInitCommandWritesUsableCredentials(t *testing.T) {
	dir := t.TempDir()
	cmd := newInitCommand()
	cmd.SetArgs([]string{"--dir", dir})
	cmd.SetErr(io.Discard)
	cmd.SetOut(io.Discard)
	require.NoError(t, cmd.Execute())

	raw, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	env := parseEnv(t, string(raw))

	for _, name := range requiredByCompose[server.TransportLocal] {
		require.NotEmpty(t, env[name], "%s was written with no value", name)
	}
	// Distinct values, not one secret reused: they guard different things.
	require.NotEqual(t, env["PODIUM_LOCAL_TOKEN"], env["PODIUM_AGENT_TOKEN"])
}

func parseEnv(t *testing.T, body string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "not a variable assignment: %q", line)
		env[name] = value
	}
	return env
}
