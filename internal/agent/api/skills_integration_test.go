//go:build integration

package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

type skillFixture struct {
	svc  *AgentService
	live *profiles.Live
	dir  string
}

func newSkillFixture(t *testing.T, skillsDir string) skillFixture {
	t.Helper()
	profile := fileProfile()
	playbook := profile.Playbooks["general"]
	playbook.Skills = []string{"pr-review"}
	profile.Playbooks["general"] = playbook

	live := profiles.NewLive(profile)
	svc := NewAgentService(AgentServiceOptions{
		Store:     newStore(t),
		Secrets:   newFakeSecrets(),
		Model:     "claude-opus-5",
		SkillsDir: skillsDir,
		Profiles:  live,
	})
	return skillFixture{svc: svc, live: live, dir: skillsDir}
}

func skillMarkdown(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nDo the thing.\n"
}

func writeSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	root := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, skills.SkillFile), []byte(skillMarkdown(name, desc)), 0o644))
}

func (f skillFixture) list(t *testing.T) *agentv1.ListSkillsResponse {
	t.Helper()
	res, err := f.svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	return res.Msg
}

func TestListSkillsReadsTheHostDirectory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "pr-review", "Use when reviewing a diff.")
	got := newSkillFixture(t, dir).list(t)
	require.Len(t, got.GetSkills(), 1)
	skill := got.GetSkills()[0]
	assert.Equal(t, "pr-review", skill.GetName())
	assert.Equal(t, "Use when reviewing a diff.", skill.GetDescription())
	assert.Equal(t, []string{"general"}, skill.GetPlaybooks())
	assert.NotEmpty(t, skill.GetSha256())
	assert.Equal(t, dir, got.GetSkillsDir())
}

func TestListSkillsReportsABrokenDirectoryRatherThanHidingIt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pr-review"), 0o755))
	got := newSkillFixture(t, dir).list(t)
	require.Len(t, got.GetSkills(), 1)
	assert.Equal(t, "pr-review", got.GetSkills()[0].GetName())
	assert.Contains(t, got.GetSkills()[0].GetProblem(), skills.SkillFile)
}

func TestListSkillsWithNoDirectoryIsEmpty(t *testing.T) {
	got := newSkillFixture(t, "").list(t)
	assert.Empty(t, got.GetSkills())
	assert.Empty(t, got.GetSkillsDir())
}

func TestTheListReportsTheCapsABundleIsHeldTo(t *testing.T) {
	got := newSkillFixture(t, "").list(t)
	assert.Equal(t, int64(skills.MaxBytes), got.GetMaxBytes())
	assert.Equal(t, int32(skills.MaxFiles), got.GetMaxFiles())
	assert.Equal(t, int32(skills.MaxSkills), got.GetMaxPerPlaybook())
}
