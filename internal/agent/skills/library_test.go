package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeDirSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SkillFile), []byte(skillMD(name, desc)), 0o644))
}

func TestADirectorySkillIsDelivered(t *testing.T) {
	root := t.TempDir()
	writeDirSkill(t, root, "pr-review", "Use when reviewing a diff.")
	lib := Library{Dir: root}
	got, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "pr-review", got[0].Name)
	assert.Equal(t, "Use when reviewing a diff.", got[0].Description)
	assert.Equal(t, EnvPrefix+"PR_REVIEW", got[0].Env)
	assert.NotEmpty(t, got[0].Encoded)
}

func TestABrokenDirectorySkillFailsTheTurn(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-review"), 0o755))
	lib := Library{Dir: root}
	_, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.ErrorContains(t, err, "has no "+SkillFile)
}

func TestANameThatIsNotADirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	lib := Library{Dir: root}
	_, err := lib.Bundles(context.Background(), []string{"missing"})
	require.ErrorContains(t, err, `no skill named "missing"`)
	require.ErrorContains(t, err, DirEnv)
}

func TestNoSkillsDirectoryFailsANamedSkill(t *testing.T) {
	_, err := Library{}.Bundles(context.Background(), []string{"pr-review"})
	require.ErrorContains(t, err, DirEnv+" is not set")
}

// ListDir reports a broken directory rather than hiding it: a skill an operator can see in
// a shell and cannot see in the UI is the worst version of this.
func TestListDirReportsWhatIsThereAndWhatIsWrongWithIt(t *testing.T) {
	root := t.TempDir()
	writeDirSkill(t, root, "pr-review", "Use when reviewing a diff.")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "no-skill-md"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Not_A_Name"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("notes"), 0o644))

	got, err := ListDir(root)
	require.NoError(t, err)
	require.Len(t, got, 3, "a loose file beside the directories is not a broken skill")

	assert.Equal(t, "Not_A_Name", got[0].Name)
	assert.Contains(t, got[0].Problem, "the harness would never load it")

	assert.Equal(t, "no-skill-md", got[1].Name)
	assert.Contains(t, got[1].Problem, SkillFile)

	assert.Equal(t, "pr-review", got[2].Name)
	assert.Empty(t, got[2].Problem)
	assert.Equal(t, "Use when reviewing a diff.", got[2].Description)
	assert.NotEmpty(t, got[2].SHA256)
}
