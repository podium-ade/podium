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
	server.TransportDev: {
		"PODIUM_PG_PASSWORD",
		"PODIUM_DEV_TOKEN",
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
				Tailnet:     "taila79bf6",
				PGPassword:  "pg",
				DevToken:    "dev",
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
	body := renderEnv(initSecrets{Transport: server.TransportTailnet, Tailnet: "taila79bf6"})
	for _, name := range []string{"TS_AUTHKEY", "PODIUM_NODE_TS_AUTHKEY", "PODIUM_NODE_ENROLL_TOKEN"} {
		require.Contains(t, body, name+"=\n", "%s should be present and empty to be filled in", name)
	}
}

// TestInitConfiguresTheCLI guards the reason the quickstart can say "source this file and
// you are done": the CLI's two variables are in it, and PODIUM_TOKEN carries the dev token
// rather than a second secret. A divergence here is a shell that reads .env, looks
// configured, and is refused by every call.
func TestInitConfiguresTheCLI(t *testing.T) {
	env := parseEnv(t, renderEnv(initSecrets{
		Transport: server.TransportDev, PGPassword: "pg", DevToken: "dev",
		S3SecretKey: "s3", AgentToken: "agent",
	}))
	require.Equal(t, env["PODIUM_DEV_TOKEN"], env["PODIUM_TOKEN"],
		"the CLI presents PODIUM_TOKEN and the server checks it against PODIUM_DEV_TOKEN")
	require.NotEmpty(t, env["PODIUM_SERVER"])
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

	for _, name := range requiredByCompose[server.TransportDev] {
		require.NotEmpty(t, env[name], "%s was written with no value", name)
	}
	// Distinct values, not one secret reused: they guard different things.
	require.NotEqual(t, env["PODIUM_DEV_TOKEN"], env["PODIUM_AGENT_TOKEN"])
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
