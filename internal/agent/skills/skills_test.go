package skills

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
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
	files := b.Files
	require.Equal(t, Digest(files), b.SHA256, "the digest is over the files")
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
	require.Equal(t, first.Files, second.Files)
}

// The runtime computes the same digest over the directory it is given. This fixture is
// pinned on both sides (agent/runtime/src/skills.test.ts), so the two cannot drift apart.
func TestDigestIsTheOneTheRuntimeComputes(t *testing.T) {
	got := Digest(map[string]string{
		"SKILL.md":               "---\nname: pr-review\n---\n",
		"reference/checklist.md": "- read the diff ✓\n",
	})
	require.Equal(t, digestFixture, got)

	// Moving bytes between a path and its content changes it: the length is in the hash.
	require.NotEqual(t, Digest(map[string]string{"ab": "c"}), Digest(map[string]string{"a": "bc"}))
}

func TestABundleMountsAtItsOwnDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pr-review"), SkillFile, skillMD("pr-review", "Use always."))
	write(t, filepath.Join(root, "pr-review"), "reference/a.md", "a\n")
	b, err := Load(root, "pr-review")
	require.NoError(t, err)

	m := b.Mount()
	require.Equal(t, "/podium/files/skills/pr-review", m.Target)
	require.Equal(t, b.Files, m.Files)
	require.NoError(t, (&spec.TaskSpec{Image: "x", Timeout: 1, MaxAttempts: 1, Mounts: []spec.FileMount{m}}).Validate())

	out := t.TempDir()
	require.NoError(t, b.WriteUnder(out))
	raw, err := os.ReadFile(filepath.Join(out, "pr-review", "reference", "a.md"))
	require.NoError(t, err)
	require.Equal(t, "a\n", string(raw))
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

func TestLoadTakesASkillFarPastAnEnvironmentVariable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	write(t, dir, SkillFile, skillMD("s", "Use always."))
	// Twice what Linux lets one environment string hold, which is what used to carry it.
	write(t, dir, "noise.md", incompressible(256<<10))

	b, err := Load(root, "s")
	require.NoError(t, err)
	require.Greater(t, b.Size(), 256<<10)

	write(t, dir, "more.md", incompressible(MaxBytes))
	_, err = Load(root, "s")
	require.ErrorContains(t, err, "that is the limit")
}

func TestLoadAllRefusesMoreThanTheCapAndAnyDuplicate(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		write(t, filepath.Join(root, n), SkillFile, skillMD(n, "Use always."))
	}
	ctx := context.Background()
	lib := Library{Dir: root}

	got, err := lib.Bundles(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "a", got[0].Name)

	_, err = lib.Bundles(ctx, []string{"a", "a"})
	require.ErrorContains(t, err, `skill "a" is declared twice`)

	many := make([]string, MaxSkills+1)
	for i := range many {
		many[i] = "a"
	}
	_, err = lib.Bundles(ctx, many)
	require.ErrorContains(t, err, "the limit is 8 per playbook")

	_, err = Library{}.Bundles(ctx, []string{"a"})
	require.ErrorContains(t, err, "PODIUM_AGENT_SKILLS_DIR is not set")

	got, err = lib.Bundles(ctx, nil)
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

// digestFixture is Digest of the files in TestDigestIsTheOneTheRuntimeComputes.
const digestFixture = "8818b95a9167cc448360be8bc1c4bbfd600fdf99bbfb4c589c159c1885060317"
