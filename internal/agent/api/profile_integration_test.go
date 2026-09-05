//go:build integration

package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// fileProfile is the half of the profile that lives on the conductor's host: one skill,
// general, defined by a skills/general.yaml.
func fileProfile() *profiles.Profile {
	return &profiles.Profile{
		Name:         "podium",
		DisplayName:  "Podium",
		SystemPrompt: "you are Podium",
		Model:        "claude-opus-5",
		DefaultSkill: "general",
		Dir:          "/etc/podium/agent",
		Skills: map[string]profiles.Skill{
			"general": {
				Name: "general", Origin: profiles.OriginFile,
				Image: "podium-agent-runtime:dev", SystemPrompt: "Answer the question.",
				AllowedTools: []string{"Read"}, MaxTurns: 50,
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

// newSkill is the request a browser sends to create a skill.
func newSkill(name string) *agentv1.SkillDefinition {
	return &agentv1.SkillDefinition{
		Name:         name,
		Image:        "example.invalid/reporter:dev",
		SystemPrompt: "Write the weekly report.",
		AllowedTools: []string{"Read", "Bash"},
		MaxTurns:     20,
		Timeout:      "10m",
		Env:          map[string]string{"PODIUM_AGENT_DRY_RUN": "1"},
		Secrets: []*agentv1.SkillSecretRef{
			{Name: "podium.agent.github_token", Target: "env", Key: "GITHUB_TOKEN"},
		},
	}
}

func (f profileFixture) create(t *testing.T, in *agentv1.SkillDefinition) *agentv1.SkillDefinition {
	t.Helper()
	res, err := f.svc.CreateSkill(loginCtx("alice"), connect.NewRequest(&agentv1.CreateSkillRequest{Skill: in}))
	require.NoError(t, err)
	return res.Msg.GetSkill()
}

// The whole point of the feature: a skill made in a browser reaches the running conductor
// without anybody restarting anything.
func TestACreatedSkillReachesTheRunningProfileWithNoRestart(t *testing.T) {
	f := newProfileFixture(t)
	require.NotContains(t, f.live.Current().Skills, "reporter")

	got := f.create(t, newSkill("reporter"))
	assert.Equal(t, profiles.OriginStored, got.GetOrigin())
	assert.True(t, got.GetEditable())
	assert.Equal(t, "alice", got.GetUpdatedBy())

	// No reload, no restart: the same *Live the conductor reads is already carrying it.
	live := f.live.Current().Skills["reporter"]
	assert.Equal(t, "example.invalid/reporter:dev", live.Image)
	assert.Equal(t, []string{"Read", "Bash"}, live.AllowedTools)
	assert.Equal(t, "1", live.Env["PODIUM_AGENT_DRY_RUN"])
	require.Len(t, live.Secrets, 1)
	assert.Equal(t, "podium.agent.github_token", live.Secrets[0].Name)
}

func TestGetProfileReportsTheFileValuesBesideTheOverrides(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newSkill("reporter"))

	_, err := f.svc.UpdateProfile(loginCtx("alice"), connect.NewRequest(&agentv1.UpdateProfileRequest{
		DisplayName: "Reporter Bot", DefaultSkill: "reporter",
	}))
	require.NoError(t, err)

	res, err := f.svc.GetProfile(loginCtx("bob"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	p := res.Msg.GetProfile()
	assert.Equal(t, "podium", p.GetName())
	assert.Equal(t, "Reporter Bot", p.GetDisplayName())
	assert.Equal(t, "Podium", p.GetFileDisplayName(), "the file's value is reported beside the override")
	assert.Equal(t, "general", p.GetFileDefaultSkill())
	assert.Equal(t, []string{"display_name", "default_skill"}, p.GetOverridden())
	assert.Equal(t, "alice", p.GetUpdatedBy())
	assert.Empty(t, res.Msg.GetStaleReason())

	byName := map[string]*agentv1.SkillDefinition{}
	for _, s := range res.Msg.GetSkills() {
		byName[s.GetName()] = s
	}
	require.Len(t, byName, 2)
	assert.Equal(t, profiles.OriginFile, byName["general"].GetOrigin())
	assert.True(t, byName["general"].GetEditable(), "both halves are editable; a file skill writes its file")
	assert.Equal(t, profiles.OriginStored, byName["reporter"].GetOrigin())
	assert.True(t, byName["reporter"].GetEditable())
	assert.Equal(t, "10m0s", byName["reporter"].GetTimeout())
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

// A file skill is edited in its file. This is the whole of "there is no difference between
// file-defined and not": the same RPC, the same editor, and the change lands where the skill
// actually lives instead of in a shadow copy that would never run.
func TestAFileSkillIsEditedInItsFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(
		"name: podium\ndisplay_name: Podium\nsystem_prompt: be direct\n"+
			"model: claude-opus-5\ndefault_skill: general\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "general.yaml"), []byte(
		"image: podium-agent-runtime:dev\nsystem_prompt: Answer the question.\n"+
			"allowed_tools: [Read]\nmax_turns: 50\n"), 0o600))

	files, err := profiles.Load(dir)
	require.NoError(t, err)
	live := profiles.NewLive(files)
	svc := NewAgentService(AgentServiceOptions{
		Store: newStore(t), Secrets: newFakeSecrets(), Profiles: live,
	})
	ctx := loginCtx("alice")
	require.NoError(t, svc.ReloadProfile(ctx))

	// Move it to Grok, which is the case the whole picker exists for.
	edited := newSkill("general")
	edited.Agent = profiles.AgentGrok
	edited.Model = "grok-4.6"
	edited.Effort = profiles.EffortXHigh
	res, err := svc.UpdateSkill(ctx, connect.NewRequest(&agentv1.UpdateSkillRequest{Skill: edited}))
	require.NoError(t, err)
	assert.Equal(t, profiles.OriginFile, res.Msg.GetSkill().GetOrigin(),
		"it is still a file skill; it was not quietly moved into the database")

	// The file on disk is what changed.
	raw, err := os.ReadFile(profiles.SkillPath(dir, "general"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent: grok")
	assert.Contains(t, string(raw), "model: grok-4.6")

	// And the running profile followed, with no restart.
	running := live.Current().Skills["general"]
	assert.Equal(t, profiles.AgentGrok, running.Agent)
	assert.Equal(t, "grok-4.6", running.Model)
	assert.Equal(t, profiles.EffortXHigh, running.Effort)

	// Nothing was written to the database half: one skill, one place.
	stored, err := svc.store.ListStoredSkills(ctx)
	require.NoError(t, err)
	assert.Empty(t, stored)
}

// Deleting a file skill deletes its file. It is refused when the profile could not load
// without it — the same rule a directory is held to at start-up — and the file survives.
func TestDeletingAFileSkillRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(
		"name: podium\ndisplay_name: Podium\nsystem_prompt: be direct\n"+
			"model: claude-opus-5\ndefault_skill: general\n"), 0o600))
	for _, n := range []string{"general", "reporter"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", n+".yaml"), []byte(
			"image: podium-agent-runtime:dev\nsystem_prompt: x\n"+
				"allowed_tools: [Read]\nmax_turns: 50\n"), 0o600))
	}
	files, err := profiles.Load(dir)
	require.NoError(t, err)
	live := profiles.NewLive(files)
	svc := NewAgentService(AgentServiceOptions{
		Store: newStore(t), Secrets: newFakeSecrets(), Profiles: live,
	})
	ctx := loginCtx("alice")
	require.NoError(t, svc.ReloadProfile(ctx))

	_, err = svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "reporter"}))
	require.NoError(t, err)
	assert.NoFileExists(t, profiles.SkillPath(dir, "reporter"))
	assert.NotContains(t, live.Current().Skills, "reporter")

	// general is default_skill, so a directory without it would not load at all.
	_, err = svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "general"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.FileExists(t, profiles.SkillPath(dir, "general"), "refused before anything was removed")
}

// A stored skill a file skill later shadows still exists in the database. It is reported so
// it can be deleted rather than quietly never running.
func TestAStoredSkillAFileLaterClaimsIsReportedAsShadowed(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newSkill("reporter"))

	// The operator adds skills/reporter.yaml and restarts: the same database, a profile
	// directory that now defines that name.
	files := fileProfile()
	files.Skills["reporter"] = profiles.Skill{
		Name: "reporter", Origin: profiles.OriginFile, Image: "the-file-wins:dev",
		SystemPrompt: "The file's version.", AllowedTools: []string{"Read"}, MaxTurns: 50,
	}
	restarted := profiles.NewLive(files)
	svc := NewAgentService(AgentServiceOptions{
		Store: f.svc.store, Secrets: newFakeSecrets(), Profiles: restarted,
	})
	require.NoError(t, svc.ReloadProfile(context.Background()))
	assert.Equal(t, "the-file-wins:dev", restarted.Current().Skills["reporter"].Image)

	res, err := svc.GetProfile(loginCtx("alice"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	var shadowed []*agentv1.SkillDefinition
	for _, s := range res.Msg.GetSkills() {
		if s.GetShadowed() {
			shadowed = append(shadowed, s)
		}
	}
	require.Len(t, shadowed, 1)
	assert.Equal(t, "reporter", shadowed[0].GetName())
	assert.Equal(t, "example.invalid/reporter:dev", shadowed[0].GetImage())

	// Deleting the shadowed row is allowed: it is a stored skill, whatever the files say.
	_, err = svc.DeleteSkill(loginCtx("alice"), connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "reporter"}))
	require.NoError(t, err)
	assert.Equal(t, "the-file-wins:dev", restarted.Current().Skills["reporter"].Image)
}

func TestUpdateAndDeleteAStoredSkill(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")
	f.create(t, newSkill("reporter"))

	changed := newSkill("reporter")
	changed.Image = "example.invalid/reporter:v2"
	_, err := f.svc.UpdateSkill(ctx, connect.NewRequest(&agentv1.UpdateSkillRequest{Skill: changed}))
	require.NoError(t, err)
	assert.Equal(t, "example.invalid/reporter:v2", f.live.Current().Skills["reporter"].Image)

	_, err = f.svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "reporter"}))
	require.NoError(t, err)
	assert.NotContains(t, f.live.Current().Skills, "reporter")

	_, err = f.svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "reporter"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = f.svc.UpdateSkill(ctx, connect.NewRequest(&agentv1.UpdateSkillRequest{Skill: changed}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// Nothing invalid is ever stored: the write is refused before it reaches Postgres, and the
// running profile is untouched.
func TestAnInvalidSkillIsRefusedAndNothingIsStored(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")

	for _, tc := range []struct {
		name string
		mut  func(*agentv1.SkillDefinition)
		want string
	}{
		{"no image", func(s *agentv1.SkillDefinition) { s.Image = "" }, "image is required"},
		{"no tools", func(s *agentv1.SkillDefinition) { s.AllowedTools = nil }, "allowed_tools is required"},
		{"a bad name", func(s *agentv1.SkillDefinition) { s.Name = "Reporter!" }, "must match"},
		{"a timeout that is not one", func(s *agentv1.SkillDefinition) { s.Timeout = "soon" },
			"must be a duration"},
		{"the reserved provider key", func(s *agentv1.SkillDefinition) {
			s.Secrets = append(s.Secrets, &agentv1.SkillSecretRef{
				Name: profiles.AnthropicKeySecret, Target: "env", Key: "ANTHROPIC_API_KEY"})
		}, "secrets may not name " + profiles.AnthropicKeySecret},
		{"the brief's env var", func(s *agentv1.SkillDefinition) {
			s.Env = map[string]string{profiles.BriefEnv: "anything"}
		}, "env may not set " + profiles.BriefEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newSkill("reporter")
			tc.mut(in)
			_, err := f.svc.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Skill: in}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, f.live.Current().Skills, "reporter")
		})
	}
}

// A skill may name any registered secret, exactly as a task spec may. There is no
// allow-list here because there is none on CreateTask either — see docs/security.md.
func TestASkillMayNameAnySecretATaskCould(t *testing.T) {
	f := newProfileFixture(t)
	in := newSkill("reporter")
	in.Secrets = []*agentv1.SkillSecretRef{
		{Name: "some.database.password", Target: "env", Key: "PGPASSWORD"},
		{Name: "some.service.account", Target: "file", Key: "/podium/secrets/sa.json"},
	}
	got := f.create(t, in)
	require.Len(t, got.GetSecrets(), 2)
	assert.Equal(t, "some.database.password", got.GetSecrets()[0].GetName())
}

// Two skills claiming one Slack channel is ambiguous routing, and the merge refuses it
// wherever the second one came from.
func TestASkillThatWouldBreakRoutingIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")

	first := newSkill("reporter")
	first.SlackChannels = []string{"C1"}
	f.create(t, first)

	second := newSkill("auditor")
	second.SlackChannels = []string{"C1"}
	_, err := f.svc.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Skill: second}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "both claim slack channel C1")
	assert.NotContains(t, f.live.Current().Skills, "auditor")
}

func TestDeletingTheDefaultSkillIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")
	f.create(t, newSkill("reporter"))
	_, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{DefaultSkill: "reporter"}))
	require.NoError(t, err)

	_, err = f.svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "reporter"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), `default_skill "reporter" names no skill`)
	assert.Contains(t, f.live.Current().Skills, "reporter")
}

func TestAProfileOverrideNamingAMissingSkillIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	_, err := f.svc.UpdateProfile(loginCtx("alice"),
		connect.NewRequest(&agentv1.UpdateProfileRequest{DefaultSkill: "nope"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "general", f.live.Current().DefaultSkill)
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

// The reconcile path: a second conductor on the same database, which never saw the write.
func TestReloadPicksUpAChangeThisProcessDidNotMake(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newSkill("reporter"))

	other := profiles.NewLive(fileProfile())
	require.NotContains(t, other.Current().Skills, "reporter")

	_, err := ReloadProfile(context.Background(), f.svc.store, other)
	require.NoError(t, err)
	assert.Contains(t, other.Current().Skills, "reporter")
}

// The chat's chip surface still works off the same live profile, so a new skill is
// selectable the moment it is created.
func TestListSkillsSeesAStoredSkillImmediately(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newSkill("reporter"))

	res, err := f.svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	var names []string
	for _, s := range res.Msg.GetSkills() {
		names = append(names, s.GetName())
	}
	assert.Equal(t, []string{"general", "reporter"}, names)
}

func TestTheProfileRpcsWithNoProfileSaySo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Store: newStore(t)})
	ctx := loginCtx("alice")

	_, err := svc.GetProfile(ctx, connect.NewRequest(&agentv1.GetProfileRequest{}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Skill: newSkill("x")}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "x"}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
