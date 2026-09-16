//go:build integration

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

// fileProfile is the profile that lives on the conductor's host: one playbook, general,
// defined by a playbooks/general.yaml.
func fileProfile() *profiles.Profile {
	return &profiles.Profile{
		Name:            "podium",
		DisplayName:     "Podium",
		SystemPrompt:    "you are Podium",
		Model:           "claude-opus-5",
		DefaultPlaybook: "general",
		Dir:             "/etc/podium/agent",
		Playbooks: map[string]profiles.Playbook{
			"general": {
				Name: "general",
				Image: "podium-agent-runtime:dev", SystemPrompt: "Answer the question.",
				AllowedTools: []string{"read"}, MaxTurns: 50,
			},
		},
	}
}

type profileFixture struct {
	svc  *AgentService
	live *profiles.Live
}

func newProfileFixture(t *testing.T) profileFixture {
	t.Helper()
	live := profiles.NewLive(fileProfile())
	svc := NewAgentService(AgentServiceOptions{
		Store:    newStore(t),
		Secrets:  newFakeSecrets(),
		Model:    "claude-opus-5",
		Profiles: live,
	})
	return profileFixture{svc: svc, live: live}
}

func TestGetProfileReportsTheFileValuesBesideTheOverrides(t *testing.T) {
	f := newProfileFixture(t)

	_, err := f.svc.UpdateProfile(loginCtx("alice"), connect.NewRequest(&agentv1.UpdateProfileRequest{
		DisplayName: "Reporter Bot",
	}))
	require.NoError(t, err)

	res, err := f.svc.GetProfile(loginCtx("bob"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	p := res.Msg.GetProfile()
	assert.Equal(t, "podium", p.GetName())
	assert.Equal(t, "Reporter Bot", p.GetDisplayName())
	assert.Equal(t, "Podium", p.GetFileDisplayName(), "the file's value is reported beside the override")
	assert.Equal(t, "general", p.GetFileDefaultPlaybook())
	assert.Equal(t, []string{"display_name"}, p.GetOverridden())
	assert.Equal(t, "alice", p.GetUpdatedBy())
	assert.Empty(t, res.Msg.GetStaleReason())

	require.Len(t, res.Msg.GetPlaybooks(), 1)
	assert.Equal(t, "general", res.Msg.GetPlaybooks()[0].GetName())
}

// The model on the Settings card follows the profile, because that is the model a turn
// will actually run on.
func TestChangingTheModelIsWhatGetSettingsReports(t *testing.T) {
	f := newProfileFixture(t)
	before, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", before.Msg.GetProvider().GetModel())

	_, err = f.svc.UpdateProfile(loginCtx("alice"),
		connect.NewRequest(&agentv1.UpdateProfileRequest{Model: "claude-haiku-5"}))
	require.NoError(t, err)

	after, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-5", after.Msg.GetProvider().GetModel())
}

func TestAProfileOverrideNamingAMissingPlaybookIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	_, err := f.svc.UpdateProfile(loginCtx("alice"),
		connect.NewRequest(&agentv1.UpdateProfileRequest{DefaultPlaybook: "nope"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "general", f.live.Current().DefaultPlaybook)
}

// Clearing an override is sending an empty field: there is no second RPC for "use the
// file's value".
func TestAnEmptyFieldClearsTheOverride(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")
	_, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{DisplayName: "Bot"}))
	require.NoError(t, err)
	assert.Equal(t, "Bot", f.live.Current().DisplayName)

	res, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "Podium", res.Msg.GetProfile().GetDisplayName())
	assert.Empty(t, res.Msg.GetProfile().GetOverridden())
	assert.Equal(t, "Podium", f.live.Current().DisplayName)
}

func TestTheProfileRpcsWithNoProfileSaySo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Store: newStore(t)})
	ctx := loginCtx("alice")

	_, err := svc.GetProfile(ctx, connect.NewRequest(&agentv1.GetProfileRequest{}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// onDiskProfile writes a real profile directory and returns a fixture reading it, which is
// what the reload RPC needs and the in-memory fileProfile above cannot give it.
func onDiskProfile(t *testing.T) (profileFixture, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(
		"name: podium\ndisplay_name: Podium\nsystem_prompt: file:./prompts/profile.md\n"+
			"model: claude-opus-5\ndefault_playbook: general\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prompts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompts", "profile.md"),
		[]byte("you are Podium"), 0o600))
	writePlaybookFile(t, dir, "general", "Answer the question.")

	files, err := profiles.Load(dir)
	require.NoError(t, err)
	live := profiles.NewLive(files)
	svc := NewAgentService(AgentServiceOptions{
		Store:      newStore(t),
		Secrets:    newFakeSecrets(),
		Model:      "claude-opus-5",
		Profiles:   live,
		ProfileDir: dir,
	})
	return profileFixture{svc: svc, live: live}, dir
}

func writePlaybookFile(t *testing.T, dir, name, prompt string) {
	t.Helper()
	body := "image: podium-agent-runtime:dev\nsystem_prompt: " + prompt +
		"\nallowed_tools: [read]\nmax_turns: 50\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "playbooks", name+".yaml"), []byte(body), 0o600))
}

func (f profileFixture) reload(t *testing.T) error {
	t.Helper()
	_, err := f.svc.ReloadProfileDir(loginCtx("alice"),
		connect.NewRequest(&agentv1.ReloadProfileDirRequest{}))
	return err
}

// A playbook FILE an operator has just edited reaches the running conductor too, which used
// to take a restart of the process.
func TestEditingAPlaybookFileReachesTheRunningProfileWithNoRestart(t *testing.T) {
	f, dir := onDiskProfile(t)
	require.NotContains(t, f.live.Current().Playbooks, "reporter")

	writePlaybookFile(t, dir, "reporter", "Write the weekly report.")
	require.NoError(t, f.reload(t))

	assert.Contains(t, f.live.Current().Playbooks, "reporter")
	assert.Contains(t, f.live.Files().Playbooks, "reporter")
}

// A prompt a `file:` points at is resolved at load, so editing one is the same reload.
func TestReloadingTheDirectoryPicksUpAnEditedPromptFile(t *testing.T) {
	f, dir := onDiskProfile(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompts", "profile.md"),
		[]byte("you are Podium, and you are terse"), 0o600))

	require.NoError(t, f.reload(t))
	assert.Equal(t, "you are Podium, and you are terse", f.live.Current().SystemPrompt)
}

// A directory that does not load is the case the button exists to survive: an operator with
// half a file saved must not be able to break a bot that is answering.
func TestAProfileDirectoryThatDoesNotLoadIsRefusedAndChangesNothing(t *testing.T) {
	f, dir := onDiskProfile(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "playbooks", "broken.yaml"),
		[]byte("image: [unterminated\n"), 0o600))

	err := f.reload(t)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.NotContains(t, f.live.Current().Playbooks, "broken")
	assert.Equal(t, []string{"general"}, f.live.Current().PlaybookNames())
}

// Re-reading the directory must not throw away an override somebody made on the Assistant
// screen.
func TestReloadingTheDirectoryKeepsTheOverrides(t *testing.T) {
	f, dir := onDiskProfile(t)
	ctx := loginCtx("alice")
	_, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{DisplayName: "Bot"}))
	require.NoError(t, err)

	writePlaybookFile(t, dir, "triage", "Triage the ticket.")
	require.NoError(t, f.reload(t))

	cur := f.live.Current()
	assert.Equal(t, []string{"general", "triage"}, cur.PlaybookNames())
	assert.Equal(t, "Bot", cur.DisplayName)
	assert.Equal(t, "Podium", f.live.Files().DisplayName)
}

// A conductor started with no directory has nothing to go back to, and says so rather than
// offering a button that cannot work.
func TestReloadingWithNoProfileDirectorySaysSo(t *testing.T) {
	f := newProfileFixture(t)
	err := f.reload(t)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
