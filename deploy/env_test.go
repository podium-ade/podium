// Package deploy holds Podium's deployment artefacts: the compose files, the node
// installer, the systemd unit and .env.example. It has no Go code — only the test below,
// which keeps .env.example honest.
package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	// Divides every scheduler timer by ten so the integration and e2e suites do not have
	// to sleep. The server logs a loud warning when it is on. Never set in production.
	"PODIUM_TEST_FAST_TIMERS": "test-only; the server warns loudly when it is set",
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
