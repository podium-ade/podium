package skills

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// zipOf builds a zip of the given files, in order.
func zipOf(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		require.NoError(t, err)
		_, err = w.Write([]byte(f[1]))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestParseUploadTakesAZippedSkill(t *testing.T) {
	name, files, err := ParseUpload(zipOf(t, [][2]string{
		{"pr-review/SKILL.md", skillMD("pr-review", "Use always.")},
		{"pr-review/reference/a.md", "a\n"},
	}), "pr-review.zip")
	require.NoError(t, err)
	require.Equal(t, "pr-review", name)
	require.Equal(t, "a\n", files["reference/a.md"])
}

// A zip is small on the wire and any size unpacked, so the caps hold while it is read.
func TestParseUploadStopsReadingAtTheCaps(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		entries := [][2]string{{"SKILL.md", skillMD("s", "Use always.")}}
		for i := range MaxFiles {
			entries = append(entries, [2]string{fmt.Sprintf("f%d.md", i), "x\n"})
		}
		_, _, err := ParseUpload(zipOf(t, entries), "s.zip")
		require.ErrorContains(t, err, fmt.Sprintf("more than %d files", MaxFiles))
	})

	t.Run("bytes", func(t *testing.T) {
		// Highly compressible: the zip is tiny and what it unpacks to is not.
		half := strings.Repeat("x", MaxBytes/2+1)
		_, _, err := ParseUpload(zipOf(t, [][2]string{
			{"SKILL.md", skillMD("s", "Use always.")},
			{"a.md", half},
			{"b.md", half},
		}), "s.zip")
		require.ErrorContains(t, err, fmt.Sprintf("more than %d bytes", MaxBytes))
	})
}
