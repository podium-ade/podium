package docker

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

func TestStageMountsWritesReadOnlyBindMounts(t *testing.T) {
	// A umask that would leave the task unable to read what was staged, had the modes been
	// left to open(2).
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	e := &Executor{dataDir: t.TempDir(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	got, err := e.stageMounts("task_1", []spec.FileMount{
		{Target: "/podium/files/skills/a", Files: map[string]string{"SKILL.md": "a\n", "ref/deep/x.md": "x\n"}},
		{Target: "/podium/files/skills/b", Files: map[string]string{"SKILL.md": "b\n"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)

	src := filepath.Join(e.taskDir("task_1"), mountsSubdir, "0")
	require.Equal(t, mount.Mount{Type: mount.TypeBind, Source: src, Target: "/podium/files/skills/a", ReadOnly: true}, got[0])
	require.Equal(t, "/podium/files/skills/b", got[1].Target)

	raw, err := os.ReadFile(filepath.Join(src, "ref", "deep", "x.md"))
	require.NoError(t, err)
	require.Equal(t, "x\n", string(raw))
	for path, want := range map[string]os.FileMode{
		src:                                 mountDirMode,
		filepath.Join(src, "ref"):           mountDirMode,
		filepath.Join(src, "ref", "deep"):   mountDirMode,
		filepath.Join(src, "SKILL.md"):      mountFileMode,
		filepath.Join(src, "ref/deep/x.md"): mountFileMode,
	} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, want, info.Mode().Perm(), path)
	}
}

func TestStageMountsRefusesAPathThatEscapes(t *testing.T) {
	e := &Executor{dataDir: t.TempDir(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := e.stageMounts("task_1", []spec.FileMount{
		{Target: "/podium/files/a", Files: map[string]string{"../../escape": "x"}},
	})
	require.ErrorIs(t, err, errSpec)
	_, statErr := os.Stat(filepath.Join(e.taskDir("task_1"), "escape"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
