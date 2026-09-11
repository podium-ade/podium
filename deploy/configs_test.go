package deploy

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"
)

// docker-compose.yml, docker-compose.tailnet.yml and docker-compose.host.yml carry the
// Postgres bootstrap script inline, as a compose `config`, so that a deployment is one
// file with nothing to fetch beside it. docker-compose.dev.yml mounts the same script
// from postgres/init.sql instead, because it is only ever used inside a clone.
//
// Two copies of anything drift. This is the test that says so: the inline content and the
// file must be byte-identical, and the fix when this fails is to copy the file into the
// compose file rather than to edit either by hand.
func TestInlinePostgresInitMatchesTheFile(t *testing.T) {
	onDisk, err := os.ReadFile("postgres/init.sql")
	require.NoError(t, err)

	// The distributed files carry it inline, because they are meant to be saved on their
	// own. docker-compose.dev.yml mounts the file instead: it is only ever used in a clone.
	for _, name := range []string{"docker-compose.yml", "docker-compose.tailnet.yml", "docker-compose.host.yml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(name)
			require.NoError(t, err)

			var file struct {
				Configs map[string]struct {
					Content string `yaml:"content"`
				} `yaml:"configs"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &file))

			inline, ok := file.Configs["postgres-init"]
			require.True(t, ok, "%s has no `postgres-init` config; the whole point of it is "+
				"that the deployment needs no second file", name)
			require.Equal(t, string(onDisk), inline.Content,
				"the inline postgres-init config in %s and postgres/init.sql have drifted. "+
					"Regenerate the inline block from the file — do not hand-edit one to "+
					"match the other.", name)
		})
	}
}

// Every base image is pinned by digest, in all three files. A tag is a moving target and a
// base image is the part of a supply chain nobody looks at — and this is the check that
// actually catches it: docker-compose.dev.yml had quietly drifted off two of them.
//
// Podium's own four images are the exception: they are selected by PODIUM_IMAGE_REPO and
// PODIUM_IMAGE_TAG, so an operator pins the version and a digest here would defeat that.
func TestBaseImagesArePinnedByDigest(t *testing.T) {
	image := regexp.MustCompile(`(?m)^\s+image:\s*(\S+)`)
	for _, name := range []string{"docker-compose.yml", "docker-compose.dev.yml", "docker-compose.tailnet.yml", "docker-compose.host.yml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(name)
			require.NoError(t, err)
			for _, m := range image.FindAllStringSubmatch(string(raw), -1) {
				ref := m[1]
				if strings.Contains(ref, "${PODIUM_IMAGE_REPO") {
					continue // ours, pinned by the operator with PODIUM_IMAGE_TAG
				}
				require.Contains(t, ref, "@sha256:",
					"%s: %s is not pinned by digest", name, ref)
			}
		})
	}
}

// The compose file has to start with no .env and no files beside it, which means every
// variable it interpolates carries a default. A `:?` here is the failure this test exists to
// catch: it turns `docker compose up` into an error message, which is the one thing this
// deployment promises not to do.
//
// The tailnet file is deliberately NOT covered. It cannot start without TS_AUTHKEY and
// PODIUM_TAILNET, which nobody can default, and failing closed there is correct.
func TestTheSingleFileNeedsNoConfiguration(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	require.NoError(t, err)
	// Only real interpolation counts. The file talks ABOUT `:?` in a comment explaining why
	// it does not use one, and a substring search would trip over that.
	required := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):\?`).FindAllStringSubmatch(string(raw), -1)
	var names []string
	for _, m := range required {
		names = append(names, m[1])
	}
	require.Empty(t, names,
		"docker-compose.yml interpolates %v with `:?`, so `docker compose up` fails until an "+
			"operator sets them. Give each a default instead.", names)
}

// TestHostComposeSharesTheHostNetwork is the reason docker-compose.host.yml exists:
// the server, conductor and a co-located node inherit this machine's routing table
// (WireGuard, a corporate VPN, the LAN) instead of Docker's bridge NAT.
func TestHostComposeSharesTheHostNetwork(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.host.yml")
	require.NoError(t, err)

	var file struct {
		Services map[string]struct {
			NetworkMode string `yaml:"network_mode"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))
	for _, name := range []string{"server", "agent", "node", "cli"} {
		require.Equal(t, "host", file.Services[name].NetworkMode,
			"%s must use the host network so it sees this machine's routes", name)
	}
	for _, name := range []string{"postgres", "objectstore", "hindsight", "init"} {
		require.Empty(t, file.Services[name].NetworkMode,
			"%s stays on the compose network; the host-networked server reaches it on loopback", name)
	}
}

// README.md carries the compose file verbatim, so that the front page shows the whole
// deployment rather than describing it. Two copies of anything drift, so this is the test
// that says so: the fenced block between the BEGIN/END markers must be the file itself.
func TestReadmeEmbedsTheComposeFileVerbatim(t *testing.T) {
	want, err := os.ReadFile("docker-compose.yml")
	require.NoError(t, err)

	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)

	const begin = "<!-- BEGIN deploy/docker-compose.yml -->\n```yaml\n"
	const end = "```\n<!-- END deploy/docker-compose.yml -->"
	i := strings.Index(string(readme), begin)
	require.GreaterOrEqual(t, i, 0, "README.md has no BEGIN marker for the compose file")
	rest := string(readme)[i+len(begin):]
	j := strings.Index(rest, end)
	require.GreaterOrEqual(t, j, 0, "README.md has no END marker for the compose file")

	require.Equal(t, string(want), rest[:j],
		"README.md's embedded compose file and deploy/docker-compose.yml have drifted. Copy "+
			"the file into the fenced block between the markers; do not edit the README copy.")
}
