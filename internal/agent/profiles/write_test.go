package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// profileDir is a minimal writable profile: a profile.yaml, one skill, and a prompts/ file
// the skill points at.
func profileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prompts"), 0o755))
	write := func(rel, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600))
	}
	write("profile.yaml", "name: podium\ndisplay_name: Podium\nsystem_prompt: be direct\n"+
		"model: claude-opus-5\ndefault_skill: general\n")
	write("prompts/general.md", "Answer the question.\n")
	write("skills/general.yaml", `# a comment a human wrote
image: podium-agent-runtime:dev
system_prompt: file:../prompts/general.md
allowed_tools: [Read, Bash]
max_turns: 50
timeout: 15m
`)
	return dir
}

func loadSkill(t *testing.T, dir, name string) Skill {
	t.Helper()
	p, err := Load(dir)
	require.NoError(t, err)
	s, ok := p.Skills[name]
	require.True(t, ok, "skill %s is not in the reloaded directory", name)
	return s
}

// The whole point of writing files: a change made through the API is in the file the next
// load reads, not in a shadow copy of it.
func TestWriteSkillFileRoundTrips(t *testing.T) {
	dir := profileDir(t)
	s := loadSkill(t, dir, "general")

	s.Agent = AgentGrok
	s.Model = "grok-4.6"
	s.Effort = EffortXHigh
	s.AllowedTools = []string{"Read", "Grep"}
	s.Resources = spec.Resources{CPU: 2, MemoryMB: 4096}
	require.NoError(t, WriteSkillFile(dir, s))

	got := loadSkill(t, dir, "general")
	assert.Equal(t, AgentGrok, got.Agent)
	assert.Equal(t, "grok-4.6", got.Model)
	assert.Equal(t, EffortXHigh, got.Effort)
	assert.Equal(t, []string{"Read", "Grep"}, got.AllowedTools)
	assert.Equal(t, spec.Resources{CPU: 2, MemoryMB: 4096}, got.Resources)
	assert.Equal(t, spec.Duration(15*60*1e9), got.Timeout, "an untouched field survives")
}

// A `file:` prompt keeps being a `file:` prompt. Inlining it would flatten a profile's
// prompt layout into its YAML and leave the .md orphaned — from one Save in a browser.
func TestWriteSkillFileKeepsAFilePromptAsAReference(t *testing.T) {
	dir := profileDir(t)
	s := loadSkill(t, dir, "general")
	require.Equal(t, "Answer the question.\n", s.SystemPrompt, "Load resolves the reference")

	s.SystemPrompt = "Answer the question, briefly.\n"
	require.NoError(t, WriteSkillFile(dir, s))

	raw, err := os.ReadFile(filepath.Join(dir, "skills", "general.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "file:../prompts/general.md",
		"the YAML still points at the prompt file")
	assert.NotContains(t, string(raw), "Answer the question, briefly",
		"and does not carry the prompt body itself")

	body, err := os.ReadFile(filepath.Join(dir, "prompts", "general.md"))
	require.NoError(t, err)
	assert.Equal(t, "Answer the question, briefly.\n", string(body),
		"the new prompt went to the file the reference names")

	assert.Equal(t, "Answer the question, briefly.\n", loadSkill(t, dir, "general").SystemPrompt)
}

// A skill whose prompt is inline stays inline: nothing invents a prompts/ file.
func TestWriteSkillFileKeepsAnInlinePromptInline(t *testing.T) {
	dir := profileDir(t)
	s := loadSkill(t, dir, "general")
	s.Name = "reporter"
	s.SystemPrompt = "Write the weekly report.\nTwo paragraphs.\n"
	require.NoError(t, WriteSkillFile(dir, s))

	raw, err := os.ReadFile(filepath.Join(dir, "skills", "reporter.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Write the weekly report.")
	// A multi-line prompt is a block scalar, not a quoted one-liner with \n in it.
	assert.Contains(t, string(raw), "system_prompt: |")
	assert.Equal(t, s.SystemPrompt, loadSkill(t, dir, "reporter").SystemPrompt)
}

// The cost, asserted so nobody discovers it by losing a file's prose.
func TestWriteSkillFileDoesNotKeepComments(t *testing.T) {
	dir := profileDir(t)
	before, err := os.ReadFile(filepath.Join(dir, "skills", "general.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(before), "# a comment a human wrote")

	require.NoError(t, WriteSkillFile(dir, loadSkill(t, dir, "general")))

	after, err := os.ReadFile(filepath.Join(dir, "skills", "general.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(after), "# a comment a human wrote",
		"a struct round-trip cannot keep them; the UI says so before you press Save")
	assert.Contains(t, string(after), "# Written by the Podium web UI",
		"and the file says what happened to it")
}

func TestDeleteSkillFileIsIdempotent(t *testing.T) {
	dir := profileDir(t)
	require.NoError(t, DeleteSkillFile(dir, "general"))
	assert.NoFileExists(t, filepath.Join(dir, "skills", "general.yaml"))
	assert.NoError(t, DeleteSkillFile(dir, "general"), "the goal state is 'no such file'")

	// The prompt it referenced is left alone: prompts are shared, and an orphan costs
	// nothing next to deleting one another skill still points at.
	assert.FileExists(t, filepath.Join(dir, "prompts", "general.md"))
}

// A read-only profile directory is the shipped compose's default, so its error has to name
// the fix rather than surfacing as a bare permission denied.
func TestWriteSkillFileNamesTheReadOnlyMount(t *testing.T) {
	dir := profileDir(t)
	skill := loadSkill(t, dir, "general")
	require.NoError(t, os.Chmod(filepath.Join(dir, "skills"), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "skills"), 0o755) })

	err := WriteSkillFile(dir, skill)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrProfileDirReadOnly)
	assert.Contains(t, err.Error(), ":rw", "the message says what to change")
}

// A failed write leaves the previous definition intact rather than a truncated file.
func TestWriteSkillFileLeavesNothingHalfWritten(t *testing.T) {
	dir := profileDir(t)
	before, err := os.ReadFile(filepath.Join(dir, "skills", "general.yaml"))
	require.NoError(t, err)

	bad := loadSkill(t, dir, "general")
	bad.Name = "Not A Valid Name"
	require.Error(t, WriteSkillFile(dir, bad))

	after, err := os.ReadFile(filepath.Join(dir, "skills", "general.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}
