package profiles

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/version"
)

// A conductor and the runtime its turns run in ship as a matched pair, so the default image
// is derived from this build's own version rather than written into a playbook. The cases
// that matter are the ones that are NOT releases: the Makefile stamps `git describe`, so an
// ordinary local build carries a short commit and one made after a tag carries a -N-g suffix.
// Sending a node to look for a published image under either of those is the bug this guards.
func TestDefaultRuntimeImageIsVersionMatched(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })

	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{"a release", "0.1.0", RuntimeImageRepo + ":0.1.0"},
		{"a prerelease", "0.1.0-rc.1", RuntimeImageRepo + ":0.1.0-rc.1"},
		{"a later release", "10.20.30", RuntimeImageRepo + ":10.20.30"},
		{"an unstamped build", "dev", LocalRuntimeImage},
		{"git describe on an untagged tree", "04d190f", LocalRuntimeImage},
		{"git describe after a tag", "v0.1.0-5-gabc123", LocalRuntimeImage},
		{"git describe, dirty", "v0.1.0-5-gabc123-dirty", LocalRuntimeImage},
		{"empty", "", LocalRuntimeImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version.Version = tc.version
			require.Equal(t, tc.want, DefaultRuntimeImage())
		})
	}
}

// A playbook that names no image is valid, and comes out carrying the matched runtime. This
// is what lets the image ship a profile directory that no release has to edit.
func TestAPlaybookWithNoImageGetsTheDefault(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "0.1.0"

	s := Playbook{AllowedTools: []string{"read"}}
	s.applyDefaults()
	require.Equal(t, RuntimeImageRepo+":0.1.0", s.Image)
	require.NoError(t, s.validate("test"))
}

// An image that IS named still wins: an operator pinning a digest or their own build must not
// have it replaced.
func TestAnExplicitImageIsKept(t *testing.T) {
	s := Playbook{Image: "registry.example.com/mine@sha256:abc", AllowedTools: []string{"read"}}
	s.applyDefaults()
	require.Equal(t, "registry.example.com/mine@sha256:abc", s.Image)
}
