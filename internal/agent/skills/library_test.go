package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is the conductor's database, as much of it as a Library asks for.
type fakeStore struct {
	rows map[string]Stored
	err  error
}

func (f *fakeStore) StoredSkill(_ context.Context, name string) (Stored, error) {
	if f.err != nil {
		return Stored{}, f.err
	}
	row, ok := f.rows[name]
	if !ok {
		return Stored{}, fmt.Errorf("%w: %s", ErrNotStored, name)
	}
	return row, nil
}

func (f *fakeStore) ListStoredSkills(context.Context) ([]Stored, error) {
	out := make([]Stored, 0, len(f.rows))
	for _, row := range f.rows {
		row.Document = nil
		out = append(out, row)
	}
	return out, f.err
}

// stored builds the row an upload would have written: the document Build produced and the
// digest it computed, which is what the library re-derives and compares.
func stored(t *testing.T, name, desc string, enabled bool) Stored {
	t.Helper()
	b, err := Build(name, map[string]string{SkillFile: md(name, desc)})
	require.NoError(t, err)
	return Stored{
		Name: name, Description: b.Description, SHA256: b.SHA256,
		SizeBytes: int64(len(b.Document)), FileCount: b.Files,
		Enabled: enabled, Document: b.Document,
	}
}

func writeDirSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SkillFile), []byte(md(name, desc)), 0o644))
}

// A stored skill is delivered exactly as a directory one is: same document, same digest,
// same environment variable.
func TestAStoredSkillIsDelivered(t *testing.T) {
	lib := Library{Store: &fakeStore{rows: map[string]Stored{
		"pr-review": stored(t, "pr-review", "Use when reviewing a diff.", true),
	}}}
	got, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "pr-review", got[0].Name)
	assert.Equal(t, "Use when reviewing a diff.", got[0].Description)
	assert.Equal(t, EnvPrefix+"PR_REVIEW", got[0].Env)
	assert.NotEmpty(t, got[0].Encoded)
}

// The directory wins. It is a file on the conductor's host, put there by whoever runs the
// process, and a browser cannot override one.
func TestTheDirectoryWinsOverAStoredSkillOfTheSameName(t *testing.T) {
	root := t.TempDir()
	writeDirSkill(t, root, "pr-review", "From the host.")
	lib := Library{Dir: root, Store: &fakeStore{rows: map[string]Stored{
		"pr-review": stored(t, "pr-review", "From the database.", true),
	}}}
	got, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "From the host.", got[0].Description)
}

// A directory that is there and will not load is an error, not a reason to serve the
// database's answer under the same name.
func TestABrokenDirectorySkillDoesNotFallThroughToTheDatabase(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-review"), 0o755))
	lib := Library{Dir: root, Store: &fakeStore{rows: map[string]Stored{
		"pr-review": stored(t, "pr-review", "From the database.", true),
	}}}
	_, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.ErrorContains(t, err, "has no "+SkillFile)
}

// A disabled skill fails the turn rather than being skipped. "Disabled" is a statement
// about the skill; the playbook still says it needs it.
func TestADisabledSkillFailsTheTurn(t *testing.T) {
	lib := Library{Store: &fakeStore{rows: map[string]Stored{
		"pr-review": stored(t, "pr-review", "Use always.", false),
	}}}
	_, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.ErrorContains(t, err, "uploaded but disabled")
}

// A row whose digest no longer matches its bytes is refused: the two disagree and neither
// of them is trustworthy.
func TestARowThatDisagreesWithItsDigestIsRefused(t *testing.T) {
	row := stored(t, "pr-review", "Use always.", true)
	row.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	lib := Library{Store: &fakeStore{rows: map[string]Stored{"pr-review": row}}}
	_, err := lib.Bundles(context.Background(), []string{"pr-review"})
	require.ErrorContains(t, err, "the row says")
}

func TestANameInNeitherPlaceIsRefused(t *testing.T) {
	root := t.TempDir()
	lib := Library{Dir: root, Store: &fakeStore{rows: map[string]Stored{}}}
	_, err := lib.Bundles(context.Background(), []string{"missing"})
	require.ErrorContains(t, err, `no skill named "missing"`)
	require.ErrorContains(t, err, "neither uploaded nor a directory")
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
	assert.Equal(t, 1, got[2].FileCount)
	assert.Positive(t, got[2].SizeBytes)
}

func TestListDirOnNothingAtAll(t *testing.T) {
	got, err := ListDir("")
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = ListDir(filepath.Join(t.TempDir(), "never-created"))
	require.NoError(t, err)
	assert.Empty(t, got)
}
