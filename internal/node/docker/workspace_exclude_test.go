package docker

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

func tarOf(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range names {
		if n[len(n)-1] == '/' {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Typeflag: tar.TypeDir}))
			continue
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(n))}))
		_, err := tw.Write([]byte(n))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func namesIn(t *testing.T, raw []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		if hdr.Typeflag != tar.TypeDir {
			body, _ := io.ReadAll(tr)
			assert.Equal(t, hdr.Name, string(body), "contents travel with their entry")
		}
		out = append(out, hdr.Name)
	}
}

// TestASnapshotLeavesOutWhatTheTaskRebuilds: an excluded name is left out wherever it is in
// the tree, with everything under it; a name that merely starts the same way is kept.
func TestASnapshotLeavesOutWhatTheTaskRebuilds(t *testing.T) {
	src := tarOf(t, "workspace/", "workspace/app/main.go", "workspace/app/node_modules/x/y.js",
		"workspace/.cache/go-build/a", "workspace/app/target-notes.txt", "workspace/app/target/out.bin")
	var out bytes.Buffer
	require.NoError(t, filterTar(&out, bytes.NewReader(src),
		excludedNames(map[string]string{spec.WorkspaceExcludeEnv: "node_modules, .cache,target,../x,a/b"})))
	assert.Equal(t, []string{"workspace/", "workspace/app/main.go", "workspace/app/target-notes.txt"}, namesIn(t, out.Bytes()))

	out.Reset()
	require.NoError(t, filterTar(&out, bytes.NewReader(src), excludedNames(nil)))
	assert.Equal(t, src, out.Bytes(), "with nothing excluded the snapshot is the tar as it was")
}
