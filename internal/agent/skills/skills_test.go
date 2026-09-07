package skills

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// write puts one file in a skill directory, creating parents.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// skillMD is the smallest frontmatter that validates.
func skillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the thing.\n"
}

// unpack reverses Load's encoding, which is what the runtime does.
func unpack(t *testing.T, encoded string) (raw []byte, files map[string]string) {
	t.Helper()
	gz, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	require.NoError(t, err)
	raw, err = io.ReadAll(zr)
	require.NoError(t, err)
	var doc struct {
		Files map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	return raw, doc.Files
}

func TestLoadPacksASkillDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pr-review")
	write(t, dir, SkillFile, skillMD("pr-review", "Use when reviewing a pull request."))
	write(t, dir, "reference/checklist.md", "- read the diff\n")
	write(t, dir, "scripts/run.sh", "#!/bin/sh\necho hi\n")

	b, err := Load(root, "pr-review")
	require.NoError(t, err)
	require.Equal(t, "pr-review", b.Name)
	require.Equal(t, "Use when reviewing a pull request.", b.Description)
	require.Equal(t, EnvPrefix+"PR_REVIEW", b.Env)
	require.Equal(t, 3, b.Files)

	raw, files := unpack(t, b.Encoded)
	sum := sha256.Sum256(raw)
	require.Equal(t, hex.EncodeToString(sum[:]), b.SHA256, "the digest is over the unpacked document")
	require.Len(t, files, 3)
	require.Contains(t, files[SkillFile], "name: pr-review")
	require.Equal(t, "- read the diff\n", files["reference/checklist.md"])
	require.Equal(t, "#!/bin/sh\necho hi\n", files["scripts/run.sh"])
}

func TestLoadIsDeterministic(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a-skill"), SkillFile, skillMD("a-skill", "Use always."))
	write(t, filepath.Join(root, "a-skill"), "b.md", "b\n")
	write(t, filepath.Join(root, "a-skill"), "a.md", "a\n")

	first, err := Load(root, "a-skill")
	require.NoError(t, err)
	second, err := Load(root, "a-skill")
	require.NoError(t, err)
	require.Equal(t, first.SHA256, second.SHA256)
	require.Equal(t, first.Encoded, second.Encoded)
}

// The names are built rather than written out: deploy/env_test.go scans this tree for
// quoted PODIUM_* literals, and a per-skill variable is not a knob an operator sets.
func TestEnvForMapsANameOntoAVariable(t *testing.T) {
	require.Equal(t, "PODIUM_AGENT_SKILL_", EnvPrefix)
	require.Equal(t, EnvPrefix+"PR_REVIEW", EnvFor("pr-review"))
	require.Equal(t, EnvPrefix+"X", EnvFor("x"))
	require.Equal(t, EnvPrefix+"A_B_C", EnvFor("a-b-c"))
}

func TestValidateNameFollowsTheHarnessRule(t *testing.T) {
	for _, ok := range []string{"a", "pr-review", "x9", "a-b-c", strings.Repeat("a", MaxNameLen)} {
		require.NoError(t, ValidateName(ok), ok)
	}
	for _, bad := range []string{
		"", "-lead", "trail-", "double--hyphen", "UPPER", "under_score", "dot.name", "sp ace",
		strings.Repeat("a", MaxNameLen+1),
	} {
		require.Error(t, ValidateName(bad), "%q should be refused", bad)
	}
}

func TestLoadRefusesWhatABundleMustNotCarry(t *testing.T) {
	tests := []struct {
		name  string
		setUp func(t *testing.T, root string)
		want  string
	}{
		{
			name: "no SKILL.md",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), "README.md", "hello\n")
			},
			want: "has no SKILL.md",
		},
		{
			name: "the frontmatter names another skill",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), SkillFile, skillMD("other", "Use always."))
			},
			want: `name is "other" but the directory is "s"`,
		},
		{
			name: "no frontmatter at all",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), SkillFile, "# Just a heading\n")
			},
			want: "must open with a --- frontmatter block",
		},
		{
			name: "the frontmatter is never closed",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), SkillFile, "---\nname: s\ndescription: x\n")
			},
			want: "never closed",
		},
		{
			name: "no description",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), SkillFile, "---\nname: s\n---\n\nbody\n")
			},
			want: "description is required",
		},
		{
			name: "the description is too long",
			setUp: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "s"), SkillFile, skillMD("s", strings.Repeat("x", MaxDescriptionLen+1)))
			},
			want: "the limit is 1024",
		},
		{
			name: "a symlink inside the skill",
			setUp: func(t *testing.T, root string) {
				dir := filepath.Join(root, "s")
				write(t, dir, SkillFile, skillMD("s", "Use always."))
				require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")))
			},
			want: "is a symlink; a skill may carry only files and directories",
		},
		{
			name: "the skill directory is itself a symlink",
			setUp: func(t *testing.T, root string) {
				target := filepath.Join(root, "real")
				write(t, target, SkillFile, skillMD("s", "Use always."))
				require.NoError(t, os.Symlink(target, filepath.Join(root, "s")))
			},
			want: "is a symlink; a skill must be a real directory",
		},
		{
			name: "a path component outside the allowed set",
			setUp: func(t *testing.T, root string) {
				dir := filepath.Join(root, "s")
				write(t, dir, SkillFile, skillMD("s", "Use always."))
				write(t, dir, ".hidden/x.md", "x\n")
			},
			want: "outside",
		},
		{
			name: "a file that is not text",
			setUp: func(t *testing.T, root string) {
				dir := filepath.Join(root, "s")
				write(t, dir, SkillFile, skillMD("s", "Use always."))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0xff, 0xfe, 0xfd}, 0o644))
			},
			want: "is not valid UTF-8",
		},
		{
			name: "more files than the cap",
			setUp: func(t *testing.T, root string) {
				dir := filepath.Join(root, "s")
				write(t, dir, SkillFile, skillMD("s", "Use always."))
				for i := 0; i <= MaxFiles; i++ {
					write(t, dir, fmt.Sprintf("many/f%d.md", i), "x\n")
				}
			},
			want: "that is the limit for one skill",
		},
		{
			name: "more bytes than the cap",
			setUp: func(t *testing.T, root string) {
				dir := filepath.Join(root, "s")
				write(t, dir, SkillFile, skillMD("s", "Use always."))
				write(t, dir, "big.md", strings.Repeat("x", MaxBytes+1))
			},
			want: "that is the limit for one skill",
		},
		{
			name: "not a directory",
			setUp: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "s"), []byte("x"), 0o644))
			},
			want: "is not a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setUp(t, root)
			_, err := Load(root, "s")
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadRefusesABundleTooLargeToDeliver(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	write(t, dir, SkillFile, skillMD("s", "Use always."))
	// Random-looking text does not compress, so it hits the encoded cap while staying
	// inside the unpacked one. The message has to name the encoded limit, because that is
	// the one an operator cannot see for themselves.
	write(t, dir, "noise.md", incompressible(MaxBytes-4096))

	_, err := Load(root, "s")
	require.Error(t, err)
	require.Contains(t, err.Error(), "once encoded for delivery")
	require.Contains(t, err.Error(), "one environment variable")
}

func TestLoadAllRefusesMoreThanTheCapAndAnyDuplicate(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		write(t, filepath.Join(root, n), SkillFile, skillMD(n, "Use always."))
	}

	got, err := LoadAll(root, []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "a", got[0].Name)

	_, err = LoadAll(root, []string{"a", "a"})
	require.ErrorContains(t, err, `skill "a" is declared twice`)

	many := make([]string, MaxSkills+1)
	for i := range many {
		many[i] = "a"
	}
	_, err = LoadAll(root, many)
	require.ErrorContains(t, err, "the limit is 8 per playbook")

	_, err = LoadAll("", []string{"a"})
	require.ErrorContains(t, err, "PODIUM_AGENT_SKILLS_DIR is not set")

	got, err = LoadAll(root, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestLoadRefusesANameThatIsNotOne(t *testing.T) {
	_, err := Load(t.TempDir(), "../etc")
	require.ErrorContains(t, err, "must match")
}

// incompressible is text gzip cannot shrink much: base64 of a deterministic byte sequence
// with no repetition short enough for the window to find.
func incompressible(n int) string {
	var b strings.Builder
	x := uint32(2463534242)
	for b.Len() < n {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		var buf [4]byte
		buf[0] = byte(x)
		buf[1] = byte(x >> 8)
		buf[2] = byte(x >> 16)
		buf[3] = byte(x >> 24)
		b.WriteString(base64.StdEncoding.EncodeToString(buf[:]))
	}
	return b.String()[:n]
}
