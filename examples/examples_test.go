// Package examples holds the task specs the documentation points at. It has no Go code —
// only the test below, which parses every example the way `podium run --spec` does.
package examples

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

// TestEveryExampleParses runs each examples/*.yaml through the same decoder the CLI uses.
// ParseTaskSpec decodes with KnownFields(true), so a field that has been renamed or a key
// that never existed fails here rather than in front of somebody following the docs.
func TestEveryExampleParses(t *testing.T) {
	paths, err := filepath.Glob("*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no examples found — has the directory moved?")

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			f, err := os.Open(path)
			require.NoError(t, err)
			defer func() { _ = f.Close() }()

			s, err := spec.ParseTaskSpec(f)
			require.NoError(t, err, "%s does not parse; docs/task-spec.md and this file disagree", path)
			require.NotEmpty(t, s.Image)

			// Every image an example uses must be one a reader can actually pull, and one
			// this repository is allowed to use.
			require.Contains(t, []string{"alpine:3", "pgvector/pgvector:pg16", "redis:7-alpine"}, s.Image,
				"examples stick to images the test environment already has")
			for name, sc := range s.Sidecars {
				require.Contains(t, []string{"alpine:3", "pgvector/pgvector:pg16", "redis:7-alpine"}, sc.Image,
					"sidecar %q uses an image outside the allowed set", name)
			}
		})
	}
}
