package api

import (
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func TestCreatePlaybookWritesTheYamlFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(`
name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
`), 0o644))

	p, err := profiles.Load(dir)
	require.NoError(t, err)
	svc := NewAgentService(AgentServiceOptions{
		Profiles:   profiles.NewLive(p),
		ProfileDir: dir,
	})

	res, err := svc.CreatePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.CreatePlaybookRequest{
		Playbook: &agentv1.PlaybookDefinition{
			Name:         "reporter",
			Image:        "alpine:3",
			SystemPrompt: "Write the weekly report.",
			AllowedTools: []string{"read", "bash"},
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, "reporter", res.Msg.Playbook.Name)
	assert.True(t, res.Msg.Playbook.Editable)
	raw, err := os.ReadFile(filepath.Join(dir, "playbooks", "reporter.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "system_prompt: Write the weekly report.")
	assert.Contains(t, string(raw), "allowed_tools:")
}

func TestCreatePlaybookRefusesANameThatExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(`
name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "playbooks", "reporter.yaml"), []byte(`
image: alpine:3
system_prompt: already here
allowed_tools: [read]
`), 0o644))

	p, err := profiles.Load(dir)
	require.NoError(t, err)
	svc := NewAgentService(AgentServiceOptions{
		Profiles:   profiles.NewLive(p),
		ProfileDir: dir,
	})

	_, err = svc.CreatePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.CreatePlaybookRequest{
		Playbook: &agentv1.PlaybookDefinition{
			Name:         "reporter",
			Image:        "alpine:3",
			SystemPrompt: "nope",
			AllowedTools: []string{"read"},
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestDeletePlaybookRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(`
name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "playbooks", "reporter.yaml"), []byte(`
image: alpine:3
system_prompt: already here
allowed_tools: [read]
`), 0o644))

	p, err := profiles.Load(dir)
	require.NoError(t, err)
	svc := NewAgentService(AgentServiceOptions{
		Profiles:   profiles.NewLive(p),
		ProfileDir: dir,
	})

	_, err = svc.DeletePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "reporter"}))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "playbooks", "reporter.yaml"))
	assert.True(t, os.IsNotExist(err))
}

func TestUploadSkillWritesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	svc := NewAgentService(AgentServiceOptions{SkillsDir: dir})

	md := []byte("---\nname: pr-review\ndescription: Use when reviewing a diff.\n---\n\nReview the PR.\n")
	res, err := svc.UploadSkill(loginCtx("alice"), connect.NewRequest(&agentv1.UploadSkillRequest{
		Content:  md,
		Filename: "SKILL.md",
	}))
	require.NoError(t, err)
	assert.Equal(t, "pr-review", res.Msg.Skill.Name)
	assert.False(t, res.Msg.Replaced)
	listed, err := svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Skills, 1)
	assert.Contains(t, listed.Msg.Skills[0].Markdown, "Review the PR.")
	_, err = os.Stat(filepath.Join(dir, "pr-review", "SKILL.md"))
	require.NoError(t, err)

	_, err = svc.DeleteSkill(loginCtx("alice"), connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "pr-review"}))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "pr-review"))
	assert.True(t, os.IsNotExist(err))
}
