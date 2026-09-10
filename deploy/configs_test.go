package deploy

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"
)

// docker-compose.yml carries the Postgres bootstrap script inline, as a compose `config`,
// so that a deployment is one file with nothing to fetch beside it. docker-compose.dev.yml
// and docker-compose.tailnet.yml mount the same script from postgres/init.sql instead,
// because neither is distributed as a single file.
//
// Two copies of anything drift. This is the test that says so: the inline content and the
// file must be byte-identical, and the fix when this fails is to copy the file into the
// compose file rather than to edit either by hand.
func TestInlinePostgresInitMatchesTheFile(t *testing.T) {
	onDisk, err := os.ReadFile("postgres/init.sql")
	require.NoError(t, err)

	raw, err := os.ReadFile("docker-compose.yml")
	require.NoError(t, err)

	var file struct {
		Configs map[string]struct {
			Content string `yaml:"content"`
		} `yaml:"configs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))

	inline, ok := file.Configs["postgres-init"]
	require.True(t, ok, "docker-compose.yml has no `postgres-init` config; the whole point of "+
		"it is that the deployment needs no second file")
	require.Equal(t, string(onDisk), inline.Content,
		"the inline postgres-init config and postgres/init.sql have drifted. Regenerate the "+
			"inline block from the file — do not hand-edit one to match the other.")
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
