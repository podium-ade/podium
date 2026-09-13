// Package deploy holds Podium's deployment artefacts: the compose files, the node
// installer, the systemd unit and .env.example. It has no Go code — only the test below,
// which keeps .env.example honest.
package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"

	"github.com/podium-ade/podium/internal/server"
	"github.com/podium-ade/podium/internal/transport/local"
)

// notConfiguration is every PODIUM_* name that appears in the source and is deliberately
// absent from .env.example. Each one needs a reason, and the reason has to be that an
// operator never sets it. Adding a name here to make the test pass is the failure mode this
// map is watching for, so the reasons are load-bearing.
var notConfiguration = map[string]string{
	// Injected into every task container by the node, and read there by podium-runner.
	// A task sees them; an operator never sets them.
	"PODIUM_TASK_ID":     "set by the node inside the task container",
	"PODIUM_LEASE_ID":    "set by the node inside the task container",
	"PODIUM_WORKDIR":     "set by the node inside the task container",
	"PODIUM_EVENTS_SOCK": "set by the node inside the task container",
	// The conductor sets it on a HOST turn's runtime, which has no node to bind-mount a
	// runner in and so has to be told where one is. An operator sets PODIUM_AGENT_RUNNER_BIN;
	// this is the name the runtime reads it under.
	"PODIUM_RUNNER_PATH": "set by the conductor on a host turn's runtime",
	// Minted per host turn by the conductor and read by the MCP server beside it, so that
	// turn can delegate a task in its own conversation. It lives as long as the turn and
	// an operator never sets one.
	"PODIUM_TURN_TOKEN": "minted by the conductor for one host turn",
	// Signed per TASK turn by the conductor and delivered as that turn's own secret: the
	// authority to mint a GitHub token for the repositories its playbook listed. It is read
	// by the git credential helper inside the container, it stops working when the turn
	// ends, and an operator never sets one. What an operator sets is
	// PODIUM_AGENT_GITHUB_APP_ID and its key.
	"PODIUM_GIT_CAPABILITY": "signed by the conductor for one turn, delivered as a secret",
	// A task-spec knob, not a deployment one: it goes in the spec's own env: block and is
	// documented in docs/task-spec.md.
	"PODIUM_KILL_AFTER": "per-task, set in the task spec's env: block",
	// Written by the conductor onto a turn's task spec and read by the agent runtime inside
	// the container. An operator never sets any of them: the brief is built per turn, and
	// the three dry-run knobs are the test seam step 16 defined. They are documented in
	// examples/agent/README.md and docs/agent.md.
	"PODIUM_AGENT_TURN":             "written by the conductor onto each turn's task spec",
	"PODIUM_AGENT_DRY_RUN":          "test-only; the dev source puts it on a task spec",
	"PODIUM_AGENT_DRY_RUN_SLEEP_MS": "test-only; the dev source puts it on a task spec",
	"PODIUM_AGENT_DRY_RUN_EXIT":     "test-only; the dev source puts it on a task spec",
	// The PREFIX of the variables one turn's Agent Skill bundles travel in. The conductor
	// writes one per skill it delivers, named after the skill, and the runtime reads it
	// there. An operator sets PODIUM_AGENT_SKILLS_DIR and never any of these — a playbook
	// that tries to set one is refused at load.
	"PODIUM_AGENT_SKILL_": "the prefix of the per-skill bundle variables the conductor writes",
	// The PREFIX of the variables one turn's MCP server tokens travel in. The conductor
	// writes one per server it delivers, named after the server, as the target of that
	// server's podium.agent.mcp.<name>_token secret. An operator registers a server and its
	// token in the web UI and never sets any of these — a playbook that tries to set one is
	// refused at load.
	"PODIUM_MCP_": "the prefix of the per-server MCP token variables the conductor writes",
	// The env var the shared memory's API key lands in INSIDE a task container. The
	// conductor names it on every turn's spec as the target of the
	// podium.agent.memory_api_key secret, and the runtime reads it there to authenticate
	// its MCP client. An operator sets PODIUM_AGENT_MEMORY_API_KEY, never this.
	"PODIUM_MEMORY_API_KEY": "the in-container target of the memory secret; set PODIUM_AGENT_MEMORY_API_KEY",
	// Divides every scheduler timer by ten so the integration and e2e suites do not have
	// to sleep. The server logs a loud warning when it is on. Never set in production.
	"PODIUM_TEST_FAST_TIMERS": "test-only; the server warns loudly when it is set",
	// Moves the docker integration suite's scratch dir off /tmp, which is only needed when
	// that suite itself runs inside a task container against a docker-in-docker sidecar:
	// the bind sources it makes have to be paths that nested daemon can resolve too.
	"PODIUM_TEST_TMPDIR": "test-only; relocates the docker suite's scratch dir when it runs nested",
	// Build-time, not run-time: the Makefile passes it to `pnpm build` so the UI header can
	// show the version of the binary serving it.
	"PODIUM_VERSION": "build-time, consumed by vite",
	// The Playwright harness's own addressing.
	"PODIUM_UI_URL": "web/e2e harness only",
	"PODIUM_CLI":    "web/e2e harness only",
}

// envExample is the file under test, relative to this package.
const envExample = ".env.example"

// quotedEnvName finds an environment variable name written as a Go string literal, which is
// the only way any Podium binary can read one. Matching literals rather than prose is what
// keeps a sentence in a comment from being mistaken for a variable.
var quotedEnvName = regexp.MustCompile(`"(PODIUM_[A-Z0-9_]+|TS_AUTHKEY)"`)

// composeEnvName finds ${VAR} interpolation in a compose file. These are variables no
// binary reads but an operator still has to set.
var composeEnvName = regexp.MustCompile(`\$\{(PODIUM_[A-Z0-9_]+|TS_AUTHKEY)[:?}-]`)

// shellEnvName finds a variable a script in this directory reads. run-host.sh and
// install-node.sh are as much a part of the deployment surface as the compose files —
// run-host.sh is what `make stack-up` runs — and the variables they take were invisible to
// this test until they were scanned, which is how four of them ended up set in a real .env
// and documented in no file at all.
var shellEnvName = regexp.MustCompile(`\$\{?(PODIUM_[A-Z0-9_]+|TS_AUTHKEY)`)

// declaredName finds a variable declared in .env.example, commented out or not.
var declaredName = regexp.MustCompile(`(?m)^#?\s*(PODIUM_[A-Z0-9_]+|TS_AUTHKEY)=`)

// TestEveryEnvVarIsDocumented is the regression guard step 14 asks for: a variable that the
// code reads and .env.example does not mention is a variable nobody deploying Podium can
// discover. It runs on `make test`, needs nothing but the source tree, and fails with the
// exact names to add.
func TestEveryEnvVarIsDocumented(t *testing.T) {
	documented := documentedVars(t)

	var missing []string
	for name, files := range sourceVars(t) {
		if _, ok := notConfiguration[name]; ok {
			continue
		}
		if !documented[name] {
			missing = append(missing, name+" (read in "+strings.Join(files, ", ")+")")
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"these variables are read by the source but are not in deploy/%s.\n"+
			"Add each one with a comment saying what it does, or — only if an operator "+
			"genuinely never sets it — add it to notConfiguration with a reason.", envExample)
}

// TestEnvExampleDocumentsNothingImaginary is the other direction, and it is the one that
// catches a variable that was renamed or deleted: a template offering a knob that does
// nothing is worse than one that is merely incomplete.
func TestEnvExampleDocumentsNothingImaginary(t *testing.T) {
	known := sourceVars(t)

	var phantom []string
	for name := range documentedVars(t) {
		if _, ok := known[name]; !ok {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(phantom)
	require.Empty(t, phantom,
		"deploy/%s documents variables that nothing reads any more. "+
			"Delete them, or fix the spelling.", envExample)
}

// documentedVars is every variable named in .env.example.
func documentedVars(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(envExample)
	require.NoError(t, err)

	out := map[string]bool{}
	for _, m := range declaredName.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	require.NotEmpty(t, out, "%s declares no variables at all — is the format still NAME=?", envExample)
	return out
}

// sourceVars is every variable the tree reads, mapped to the files that read it. Go source
// is scanned for quoted names; the compose files are scanned for ${...} interpolation.
func sourceVars(t *testing.T) map[string][]string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	out := map[string][]string{}
	add := func(name, file string) {
		if !contains(out[name], file) {
			out[name] = append(out[name], file)
		}
	}

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "bin": true, "dist": true,
		"plans": true, "runnerbin": true, "test-results": true,
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		var re *regexp.Regexp
		switch {
		case strings.HasSuffix(path, ".go"):
			re = quotedEnvName
		case strings.HasSuffix(path, ".yml"), strings.HasSuffix(path, ".yaml"):
			re = composeEnvName
		case strings.HasSuffix(path, ".sh"):
			re = shellEnvName
		default:
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // walking this repository
		if readErr != nil {
			return readErr
		}
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			add(m[1], rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, out, "found no environment variables in the tree — has the scan broken?")
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// composeEnvRef matches one ${VAR}, ${VAR:-default}, ${VAR-default} or ${VAR:?message}
// reference, which is the whole of compose's interpolation syntax that these files use.
var composeEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?[-?]([^}]*))?\}`)

// composeServiceEnv reads one service's environment block out of a compose file and
// interpolates it the way `docker compose` would, with supplied standing in for the
// variables an operator provides. It is deliberately not a general compose parser: it
// understands exactly what these files contain.
func composeServiceEnv(t *testing.T, file, service string, supplied map[string]string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &parsed))
	svc, ok := parsed.Services[service]
	require.True(t, ok, "%s has no %s service", file, service)
	require.NotEmpty(t, svc.Environment, "%s's %s service sets no environment", file, service)

	out := make(map[string]string, len(svc.Environment))
	for name, value := range svc.Environment {
		out[name] = composeEnvRef.ReplaceAllStringFunc(fmt.Sprint(value), func(ref string) string {
			m := composeEnvRef.FindStringSubmatch(ref)
			if v, ok := supplied[m[1]]; ok {
				return v
			}
			// `:?` has no default — it is compose refusing to start without a value — so a
			// variable the caller did not supply and that has no default becomes empty,
			// which is what a real deployment would then be told off for.
			if strings.Contains(ref, "-") {
				return m[2]
			}
			return ""
		})
	}
	return out
}

// TestTheComposeServerConfigurationStarts is the guard the rest of this file was missing.
// Every name in .env.example was documented and every value in docker-compose.yml was
// unchecked, so the shipped deployment sat there for a release with PODIUM_LOCAL_LISTEN set to
// an address the local transport refuses — `podium-server: local transport: refusing to listen on
// "0.0.0.0:8080"`, exit 1, on the very first `docker compose up`.
func TestTheComposeServerConfigurationStarts(t *testing.T) {
	for _, tc := range []struct {
		file      string
		transport string
	}{
		{file: "docker-compose.yml", transport: server.TransportLocal},
		{file: "docker-compose.tailnet.yml", transport: server.TransportTailnet},
		{file: "docker-compose.host.yml", transport: server.TransportLocal},
	} {
		t.Run(tc.file, func(t *testing.T) {
			env := composeServiceEnv(t, tc.file, "server", map[string]string{
				"PODIUM_PG_PASSWORD":   "pgpassword",
				"PODIUM_LOCAL_TOKEN":   "devtoken",
				"PODIUM_S3_SECRET_KEY": "s3secretkey",
				"PODIUM_AGENT_TOKEN":   "agenttoken",
				"TS_AUTHKEY":           "tskey-auth-notreal",
				"PODIUM_TAILNET":       "tail0a1b2c",
			})
			// The server reads its whole configuration from the environment, so the compose
			// block IS the configuration: set it and ask the binary's own loader.
			clearPodiumEnv(t)
			for name, value := range env {
				t.Setenv(name, value)
			}

			cfg := server.ConfigFromEnv()
			require.Equal(t, tc.transport, cfg.Transport)
			require.NoError(t, cfg.Validate(),
				"%s cannot start: the shipped deployment must be a valid configuration", tc.file)
		})
	}
}

// TestTheComposeServerBindsEveryInterfaceOnPurpose: the fix for that boot failure is a
// waiver, not a weaker check, and the two halves have to travel together. A compose file
// that binds 0.0.0.0 without saying why would be refused all over again.
func TestTheComposeServerBindsEveryInterfaceOnPurpose(t *testing.T) {
	env := composeServiceEnv(t, "docker-compose.yml", "server", map[string]string{
		"PODIUM_PG_PASSWORD":   "pgpassword",
		"PODIUM_LOCAL_TOKEN":   "devtoken",
		"PODIUM_S3_SECRET_KEY": "s3secretkey",
		"PODIUM_AGENT_TOKEN":   "agenttoken",
	})
	require.Equal(t, "0.0.0.0:8080", env["PODIUM_LOCAL_LISTEN"],
		"inside a container loopback is the container's own and nothing could reach it")
	require.Equal(t, "true", env[local.UnsafeListenVar],
		"binding every interface needs the waiver, and the waiver is what says the operator meant it")

	require.Error(t, local.CheckListen(env["PODIUM_LOCAL_LISTEN"], false),
		"the loopback rule still stands for everyone who has not asked for the waiver")
	require.NoError(t, local.CheckListen(env["PODIUM_LOCAL_LISTEN"], true))
}

// TestTheHostComposeServerBindsTheHost is the same waiver, for a different reason:
// docker-compose.host.yml puts the server in the host's network namespace so a worker
// on the other side of a WireGuard (or VPN, or LAN) has a real address to dial.
func TestTheHostComposeServerBindsTheHost(t *testing.T) {
	env := composeServiceEnv(t, "docker-compose.host.yml", "server", map[string]string{
		"PODIUM_PG_PASSWORD":   "pgpassword",
		"PODIUM_LOCAL_TOKEN":   "devtoken",
		"PODIUM_S3_SECRET_KEY": "s3secretkey",
		"PODIUM_AGENT_TOKEN":   "agenttoken",
	})
	require.Equal(t, server.TransportLocal, env["PODIUM_TRANSPORT"],
		"host-network auth is still the local token; PODIUM_TRANSPORT=host talks to tailscaled")
	require.Equal(t, "0.0.0.0:8080", env["PODIUM_LOCAL_LISTEN"])
	require.Equal(t, "true", env[local.UnsafeListenVar])
	require.NoError(t, local.CheckListen(env["PODIUM_LOCAL_LISTEN"], true))
}

// clearPodiumEnv unsets every PODIUM_* and TS_AUTHKEY variable in the ambient environment, so
// a test measures the compose file and not the machine it runs on.
func clearPodiumEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PODIUM_") || name == "TS_AUTHKEY" {
			t.Setenv(name, "")
		}
	}
}
