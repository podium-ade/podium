package skills

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zipOf builds an archive in memory. mode is applied verbatim so a test can ship the things
// a real archive can ship and this package has to refuse.
type entry struct {
	name string
	body string
	mode fs.FileMode
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func md(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nDo the thing.\n"
}

// A zip of the skill's own directory is the shape `zip -r pr-review.zip pr-review/`
// produces, and the wrapping directory comes off.
func TestAZipOfASkillDirectoryUploads(t *testing.T) {
	raw := zipOf(t,
		entry{name: "pr-review/"},
		entry{name: "pr-review/" + SkillFile, body: md("pr-review", "Use when reviewing a diff.")},
		entry{name: "pr-review/reference/checklist.md", body: "- read the diff\n"},
	)
	name, files, err := Parse(raw, "pr-review.zip")
	require.NoError(t, err)
	assert.Equal(t, "pr-review", name)
	assert.Len(t, files, 2)
	assert.Contains(t, files, SkillFile)
	assert.Contains(t, files, "reference/checklist.md")

	b, err := Build(name, files)
	require.NoError(t, err)
	assert.Equal(t, "pr-review", b.Name)
	assert.Equal(t, "Use when reviewing a diff.", b.Description)
	assert.Len(t, b.SHA256, 64)
	assert.Equal(t, 2, b.Files)
	assert.NotEmpty(t, b.Document)
}

// A zip made from inside the directory already has SKILL.md at its root, and nothing is
// stripped: doing so would eat `reference/` and leave the bundle short a directory.
func TestAZipMadeFromInsideTheDirectoryIsLeftAlone(t *testing.T) {
	raw := zipOf(t,
		entry{name: "./" + SkillFile, body: md("pr-review", "Use when reviewing.")},
		entry{name: "reference/checklist.md", body: "- read the diff\n"},
	)
	name, files, err := Parse(raw, "x.zip")
	require.NoError(t, err)
	assert.Equal(t, "pr-review", name)
	assert.Contains(t, files, SkillFile)
	assert.Contains(t, files, "reference/checklist.md")
}

// The name is the frontmatter's and not the archive's: an archive called anything at all
// installs the skill under the only name the harness would load it as.
func TestTheNameComesFromTheFrontmatterAndNotTheFile(t *testing.T) {
	raw := zipOf(t, entry{name: "whatever/" + SkillFile, body: md("pr-review", "Use when reviewing.")})
	name, _, err := Parse(raw, "some-download-4.zip")
	require.NoError(t, err)
	assert.Equal(t, "pr-review", name)
}

// A pasted SKILL.md is one file, and the file is the skill.
func TestAPastedSkillMarkdownUploads(t *testing.T) {
	name, files, err := Parse([]byte(md("release-notes", "Use when writing release notes.")), "")
	require.NoError(t, err)
	assert.Equal(t, "release-notes", name)
	assert.Equal(t, map[string]string{SkillFile: md("release-notes", "Use when writing release notes.")}, files)
}

func TestAnUploadWithNothingUsableInItIsRefused(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, _, err := Parse(nil, "x.zip")
		require.ErrorContains(t, err, "the upload is empty")
	})

	t.Run("over the upload cap", func(t *testing.T) {
		_, _, err := Parse(bytes.Repeat([]byte("x"), MaxUploadBytes+1), "big.zip")
		require.ErrorContains(t, err, "the limit for an upload is")
		require.ErrorContains(t, err, "big.zip")
	})

	t.Run("a zip header with no zip behind it", func(t *testing.T) {
		_, _, err := Parse([]byte("PK\x03\x04not really"), "lying.zip")
		require.ErrorContains(t, err, "not a readable zip")
	})

	t.Run("no SKILL.md", func(t *testing.T) {
		raw := zipOf(t, entry{name: "pr-review/notes.md", body: "hello"})
		_, _, err := Parse(raw, "x.zip")
		require.ErrorContains(t, err, "there is no "+SkillFile)
	})

	t.Run("frontmatter with no name", func(t *testing.T) {
		_, _, err := Parse([]byte("---\ndescription: Use always.\n---\n"), "SKILL.md")
		require.ErrorContains(t, err, "name is required in the frontmatter")
	})

	t.Run("not text", func(t *testing.T) {
		_, _, err := Parse([]byte{0xff, 0xfe, 0x00, 0x01}, "SKILL.md")
		require.ErrorContains(t, err, "not valid UTF-8")
	})
}

// The traversal guards, on the side that reads somebody else's archive. Every one of these
// is a refusal rather than a repair: a cleaned ../../.ssh is still a write outside the
// skill's directory, so the answer is no and not path.Clean.
func TestAZipCannotEscapeTheSkillsDirectory(t *testing.T) {
	cases := []struct {
		what, path, want string
	}{
		{"a parent traversal", "../../.ssh/authorized_keys", `must not contain ".."`},
		{"an absolute path", "/etc/cron.d/x", "must be relative to the skill's own directory"},
		{"a backslash", `..\..\windows`, "no backslash"},
		// This one is not a traversal; it is a name a bundle path may not have, and Build is
		// where it is caught rather than the archive reader.
		{"a dotfile", ".bashrc", "outside"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			raw := zipOf(t,
				entry{name: SkillFile, body: md("pr-review", "Use always.")},
				entry{name: tc.path, body: "pwned"},
			)
			name, files, err := Parse(raw, "evil.zip")
			if err == nil {
				_, err = Build(name, files)
			}
			require.Error(t, err, "path %q was accepted", tc.path)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A zip entry can be a symlink; a bundle cannot. It is refused by name rather than skipped,
// because a zip that ships one is a zip whose author expected it to arrive.
func TestAZipThatShipsASymlinkIsRefused(t *testing.T) {
	raw := zipOf(t,
		entry{name: SkillFile, body: md("pr-review", "Use always.")},
		entry{name: "secrets", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
	)
	_, _, err := Parse(raw, "evil.zip")
	require.ErrorContains(t, err, "is a symlink")
	require.ErrorContains(t, err, "only files and directories")
}

// Two entries for one path is how an archive smuggles a second version of a file past
// whoever read the first.
func TestAZipWithTheSamePathTwiceIsRefused(t *testing.T) {
	raw := zipOf(t,
		entry{name: SkillFile, body: md("pr-review", "Use always.")},
		entry{name: "notes.md", body: "harmless"},
		entry{name: "notes.md", body: "not harmless"},
	)
	_, _, err := Parse(raw, "twice.zip")
	require.ErrorContains(t, err, "twice")
}

// A file whose header understates its size is still read under a hard limit, so a bomb
// fails on the cap rather than on memory.
func TestAZipIsCappedWhateverItsHeaderSays(t *testing.T) {
	raw := zipOf(t,
		entry{name: SkillFile, body: md("pr-review", "Use always.")},
		entry{name: "big.md", body: strings.Repeat("a", MaxBytes+1)},
	)
	_, _, err := Parse(raw, "bomb.zip")
	require.Error(t, err)
	require.ErrorContains(t, err, "unpacked")
}

func TestAZipWithTooManyFilesIsRefused(t *testing.T) {
	entries := []entry{{name: SkillFile, body: md("pr-review", "Use always.")}}
	for i := 0; i <= MaxFiles; i++ {
		entries = append(entries, entry{name: "f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".md", body: "x"})
	}
	name, files, err := Parse(zipOf(t, entries...), "many.zip")
	require.NoError(t, err)
	_, err = Build(name, files)
	require.ErrorContains(t, err, "the limit is 64")
}

// A skill too big to be delivered is refused when it is uploaded, naming the cap. Before
// this the same skill was accepted and then failed the first turn that asked for it.
func TestASkillTooBigToDeliverIsRefusedAtUpload(t *testing.T) {
	body := md("pr-review", "Use always.") + incompressible(MaxBytes*3/4)
	name, files, err := Parse([]byte(body), "SKILL.md")
	require.NoError(t, err)
	_, err = Build(name, files)
	require.ErrorContains(t, err, "once encoded for delivery")
	require.ErrorContains(t, err, "one environment variable")
}

// What a desktop archiver adds, nobody meant to ship. These two are skipped; everything
// else — including any other dotfile — is still refused.
func TestArchiverNoiseIsSkipped(t *testing.T) {
	raw := zipOf(t,
		entry{name: "pr-review/" + SkillFile, body: md("pr-review", "Use always.")},
		entry{name: "pr-review/.DS_Store", body: "junk"},
		entry{name: "__MACOSX/pr-review/._SKILL.md", body: "junk"},
	)
	name, files, err := Parse(raw, "made-on-a-mac.zip")
	require.NoError(t, err)
	assert.Equal(t, "pr-review", name)
	assert.Equal(t, []string{SkillFile}, keysOf(files))
}

// A round trip through the database changes nothing the digest is over.
func TestABundleSurvivesAStoredDocument(t *testing.T) {
	name, files, err := Parse([]byte(md("pr-review", "Use always.")), "SKILL.md")
	require.NoError(t, err)
	first, err := Build(name, files)
	require.NoError(t, err)

	again, err := FromDocument(name, first.Document)
	require.NoError(t, err)
	assert.Equal(t, first.SHA256, again.SHA256)
	assert.Equal(t, first.Encoded, again.Encoded)
	assert.Equal(t, first.Description, again.Description)
}

// A document that has been changed under the row fails rather than reaching a container.
func TestACorruptedStoredDocumentIsRefused(t *testing.T) {
	_, err := FromDocument("pr-review", []byte("{"))
	require.ErrorContains(t, err, "not a bundle document")

	_, err = FromDocument("pr-review", []byte(`{"files":{}}`))
	require.ErrorContains(t, err, "no files")

	_, err = FromDocument("pr-review", []byte(`{"files":{"`+SkillFile+`":"no frontmatter"}}`))
	require.ErrorContains(t, err, "frontmatter")
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
