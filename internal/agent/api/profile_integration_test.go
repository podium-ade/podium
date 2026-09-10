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

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fileProfile is the half of the profile that lives on the conductor's host: one playbook,
// general, defined by a playbooks/general.yaml.
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
				Name: "general", Origin: profiles.OriginFile,
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

// newPlaybook is the request a browser sends to create a playbook.
func newPlaybook(name string) *agentv1.PlaybookDefinition {
	return &agentv1.PlaybookDefinition{
		Name:         name,
		Image:        "example.invalid/reporter:dev",
		SystemPrompt: "Write the weekly report.",
		AllowedTools: []string{"read", "bash"},
		MaxTurns:     20,
		Timeout:      "10m",
		Priority:     4,
		Env:          map[string]string{"PODIUM_AGENT_DRY_RUN": "1"},
		Secrets: []*agentv1.PlaybookSecretRef{
			{Name: "podium.agent.github_token", Target: "env", Key: "GITHUB_TOKEN"},
		},
	}
}

func (f profileFixture) create(t *testing.T, in *agentv1.PlaybookDefinition) *agentv1.PlaybookDefinition {
	t.Helper()
	res, err := f.svc.CreatePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: in}))
	require.NoError(t, err)
	return res.Msg.GetPlaybook()
}

// The whole point of the feature: a playbook made in a browser reaches the running conductor
// without anybody restarting anything.
func TestACreatedPlaybookReachesTheRunningProfileWithNoRestart(t *testing.T) {
	f := newProfileFixture(t)
	require.NotContains(t, f.live.Current().Playbooks, "reporter")

	got := f.create(t, newPlaybook("reporter"))
	assert.Equal(t, profiles.OriginStored, got.GetOrigin())
	assert.True(t, got.GetEditable())
	assert.Equal(t, "alice", got.GetUpdatedBy())

	// No reload, no restart: the same *Live the conductor reads is already carrying it.
	live := f.live.Current().Playbooks["reporter"]
	assert.Equal(t, "example.invalid/reporter:dev", live.Image)
	assert.Equal(t, []string{"read", "bash"}, live.AllowedTools)
	assert.Equal(t, 4, live.Priority, "the queue priority a browser set is the one turns run at")
	assert.Equal(t, "1", live.Env["PODIUM_AGENT_DRY_RUN"])
	require.Len(t, live.Secrets, 1)
	assert.Equal(t, "podium.agent.github_token", live.Secrets[0].Name)
}

func TestGetProfileReportsTheFileValuesBesideTheOverrides(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newPlaybook("reporter"))

	_, err := f.svc.UpdateProfile(loginCtx("alice"), connect.NewRequest(&agentv1.UpdateProfileRequest{
		DisplayName: "Reporter Bot", DefaultPlaybook: "reporter",
	}))
	require.NoError(t, err)

	res, err := f.svc.GetProfile(loginCtx("bob"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	p := res.Msg.GetProfile()
	assert.Equal(t, "podium", p.GetName())
	assert.Equal(t, "Reporter Bot", p.GetDisplayName())
	assert.Equal(t, "Podium", p.GetFileDisplayName(), "the file's value is reported beside the override")
	assert.Equal(t, "general", p.GetFileDefaultPlaybook())
	assert.Equal(t, []string{"display_name", "default_playbook"}, p.GetOverridden())
	assert.Equal(t, "alice", p.GetUpdatedBy())
	assert.Empty(t, res.Msg.GetStaleReason())

	byName := map[string]*agentv1.PlaybookDefinition{}
	for _, s := range res.Msg.GetPlaybooks() {
		byName[s.GetName()] = s
	}
	require.Len(t, byName, 2)
	assert.Equal(t, profiles.OriginFile, byName["general"].GetOrigin())
	assert.False(t, byName["general"].GetEditable(), "a file playbook is read-only through this API")
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

func TestAFilePlaybookIsReadOnly(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")

	_, err := f.svc.CreatePlaybook(ctx, connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: newPlaybook("general")}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "playbooks/general.yaml")

	_, err = f.svc.UpdatePlaybook(ctx, connect.NewRequest(&agentv1.UpdatePlaybookRequest{Playbook: newPlaybook("general")}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "read-only here")
	assert.Contains(t, err.Error(), "the files win")

	_, err = f.svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "general"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	assert.Equal(t, "podium-agent-runtime:dev", f.live.Current().Playbooks["general"].Image)
}

// A stored playbook a file playbook later shadows still exists in the database. It is reported so
// it can be deleted rather than quietly never running.
func TestAStoredPlaybookAFileLaterClaimsIsReportedAsShadowed(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newPlaybook("reporter"))

	// The operator adds playbooks/reporter.yaml and restarts: the same database, a profile
	// directory that now defines that name.
	files := fileProfile()
	files.Playbooks["reporter"] = profiles.Playbook{
		Name: "reporter", Origin: profiles.OriginFile, Image: "the-file-wins:dev",
		SystemPrompt: "The file's version.", AllowedTools: []string{"read"}, MaxTurns: 50,
	}
	restarted := profiles.NewLive(files)
	svc := NewAgentService(AgentServiceOptions{
		Store: f.svc.store, Secrets: newFakeSecrets(), Profiles: restarted,
	})
	require.NoError(t, svc.Reconcile(context.Background()))
	assert.Equal(t, "the-file-wins:dev", restarted.Current().Playbooks["reporter"].Image)

	res, err := svc.GetProfile(loginCtx("alice"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	var shadowed []*agentv1.PlaybookDefinition
	for _, s := range res.Msg.GetPlaybooks() {
		if s.GetShadowed() {
			shadowed = append(shadowed, s)
		}
	}
	require.Len(t, shadowed, 1)
	assert.Equal(t, "reporter", shadowed[0].GetName())
	assert.Equal(t, "example.invalid/reporter:dev", shadowed[0].GetImage())

	// Deleting the shadowed row is allowed: it is a stored playbook, whatever the files say.
	_, err = svc.DeletePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "reporter"}))
	require.NoError(t, err)
	assert.Equal(t, "the-file-wins:dev", restarted.Current().Playbooks["reporter"].Image)
}

func TestUpdateAndDeleteAStoredPlaybook(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")
	f.create(t, newPlaybook("reporter"))

	changed := newPlaybook("reporter")
	changed.Image = "example.invalid/reporter:v2"
	_, err := f.svc.UpdatePlaybook(ctx, connect.NewRequest(&agentv1.UpdatePlaybookRequest{Playbook: changed}))
	require.NoError(t, err)
	assert.Equal(t, "example.invalid/reporter:v2", f.live.Current().Playbooks["reporter"].Image)

	_, err = f.svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "reporter"}))
	require.NoError(t, err)
	assert.NotContains(t, f.live.Current().Playbooks, "reporter")

	_, err = f.svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "reporter"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = f.svc.UpdatePlaybook(ctx, connect.NewRequest(&agentv1.UpdatePlaybookRequest{Playbook: changed}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// Nothing invalid is ever stored: the write is refused before it reaches Postgres, and the
// running profile is untouched.
func TestAnInvalidPlaybookIsRefusedAndNothingIsStored(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")

	for _, tc := range []struct {
		name string
		mut  func(*agentv1.PlaybookDefinition)
		want string
	}{
		{"no tools", func(s *agentv1.PlaybookDefinition) { s.AllowedTools = nil }, "allowed_tools is required"},
		{"a bad name", func(s *agentv1.PlaybookDefinition) { s.Name = "Reporter!" }, "must match"},
		{"a priority nothing could distinguish",
			func(s *agentv1.PlaybookDefinition) { s.Priority = 5000 }, "priority must be between"},
		{"a timeout that is not one", func(s *agentv1.PlaybookDefinition) { s.Timeout = "soon" },
			"must be a duration"},
		{"the reserved provider key", func(s *agentv1.PlaybookDefinition) {
			s.Secrets = append(s.Secrets, &agentv1.PlaybookSecretRef{
				Name: profiles.AnthropicKeySecret, Target: "env", Key: "ANTHROPIC_API_KEY"})
		}, "secrets may not name " + profiles.AnthropicKeySecret},
		{"the brief's env var", func(s *agentv1.PlaybookDefinition) {
			s.Env = map[string]string{profiles.BriefEnv: "anything"}
		}, "env may not set " + profiles.BriefEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newPlaybook("reporter")
			tc.mut(in)
			_, err := f.svc.CreatePlaybook(ctx, connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: in}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, f.live.Current().Playbooks, "reporter")
		})
	}
}

// A playbook created with no image comes back carrying the runtime published alongside this
// build. Asserted through the API because "CreatePlaybook fills it in" is the half a caller
// depends on, and a unit test on applyDefaults cannot see it.
func TestAPlaybookWithNoImageGetsTheMatchedRuntime(t *testing.T) {
	f := newProfileFixture(t)
	in := newPlaybook("reporter")
	in.Image = ""
	got := f.create(t, in)
	assert.Equal(t, profiles.DefaultRuntimeImage(), got.GetImage())
}

// A playbook may name any registered secret, exactly as a task spec may. There is no
// allow-list here because there is none on CreateTask either — see docs/security.md.
func TestAPlaybookMayNameAnySecretATaskCould(t *testing.T) {
	f := newProfileFixture(t)
	in := newPlaybook("reporter")
	in.Secrets = []*agentv1.PlaybookSecretRef{
		{Name: "some.database.password", Target: "env", Key: "PGPASSWORD"},
		{Name: "some.service.account", Target: "file", Key: "/podium/secrets/sa.json"},
	}
	got := f.create(t, in)
	require.Len(t, got.GetSecrets(), 2)
	assert.Equal(t, "some.database.password", got.GetSecrets()[0].GetName())
}

// Two playbooks claiming one Slack channel is ambiguous routing, and the merge refuses it
// wherever the second one came from.
func TestAPlaybookThatWouldBreakRoutingIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")

	first := newPlaybook("reporter")
	first.SlackChannels = []string{"C1"}
	f.create(t, first)

	second := newPlaybook("auditor")
	second.SlackChannels = []string{"C1"}
	_, err := f.svc.CreatePlaybook(ctx, connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: second}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "both claim slack channel C1")
	assert.NotContains(t, f.live.Current().Playbooks, "auditor")
}

func TestDeletingTheDefaultPlaybookIsRefused(t *testing.T) {
	f := newProfileFixture(t)
	ctx := loginCtx("alice")
	f.create(t, newPlaybook("reporter"))
	_, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{DefaultPlaybook: "reporter"}))
	require.NoError(t, err)

	_, err = f.svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "reporter"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), `default_playbook "reporter" names no playbook`)
	assert.Contains(t, f.live.Current().Playbooks, "reporter")
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

// The reconcile path: a second conductor on the same database, which never saw the write.
func TestReloadPicksUpAChangeThisProcessDidNotMake(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newPlaybook("reporter"))

	other := profiles.NewLive(fileProfile())
	require.NotContains(t, other.Current().Playbooks, "reporter")

	_, err := ReloadProfile(context.Background(), f.svc.store, other)
	require.NoError(t, err)
	assert.Contains(t, other.Current().Playbooks, "reporter")
}

// The chat's chip surface still works off the same live profile, so a new playbook is
// selectable the moment it is created.
func TestListPlaybooksSeesAStoredPlaybookImmediately(t *testing.T) {
	f := newProfileFixture(t)
	f.create(t, newPlaybook("reporter"))

	res, err := f.svc.ListPlaybooks(loginCtx("alice"), connect.NewRequest(&agentv1.ListPlaybooksRequest{}))
	require.NoError(t, err)
	var names []string
	for _, s := range res.Msg.GetPlaybooks() {
		names = append(names, s.GetName())
	}
	assert.Equal(t, []string{"general", "reporter"}, names)
}

func TestTheProfileRpcsWithNoProfileSaySo(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{Store: newStore(t)})
	ctx := loginCtx("alice")

	_, err := svc.GetProfile(ctx, connect.NewRequest(&agentv1.GetProfileRequest{}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.CreatePlaybook(ctx, connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: newPlaybook("x")}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "x"}))
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

// The other half of the feature: a playbook FILE an operator has just edited reaches the
// running conductor too, which used to take a restart of the process.
func TestEditingAPlaybookFileReachesTheRunningProfileWithNoRestart(t *testing.T) {
	f, dir := onDiskProfile(t)
	require.NotContains(t, f.live.Current().Playbooks, "reporter")

	writePlaybookFile(t, dir, "reporter", "Write the weekly report.")
	require.NoError(t, f.reload(t))

	assert.Contains(t, f.live.Current().Playbooks, "reporter")
	assert.Equal(t, profiles.OriginFile, f.live.Current().Playbooks["reporter"].Origin)
	// Files() is the half an override is explained against, so it has to move too.
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

// The two halves are merged on a reload exactly as they are at start-up: re-reading the
// directory must not throw away a playbook or an override somebody made in the browser.
func TestReloadingTheDirectoryKeepsWhatTheDatabaseHolds(t *testing.T) {
	f, dir := onDiskProfile(t)
	ctx := loginCtx("alice")
	f.create(t, newPlaybook("reporter"))
	_, err := f.svc.UpdateProfile(ctx, connect.NewRequest(&agentv1.UpdateProfileRequest{DisplayName: "Bot"}))
	require.NoError(t, err)

	writePlaybookFile(t, dir, "triage", "Triage the ticket.")
	require.NoError(t, f.reload(t))

	cur := f.live.Current()
	assert.Equal(t, []string{"general", "reporter", "triage"}, cur.PlaybookNames())
	assert.Equal(t, "Bot", cur.DisplayName)
	// The file half is what an override is shown against, and it holds no override.
	assert.Equal(t, "Podium", f.live.Files().DisplayName)
}

// A file added for a name the database already holds shadows it, on a reload as at start-up.
func TestAReloadedFileShadowsAStoredPlaybookOfTheSameName(t *testing.T) {
	f, dir := onDiskProfile(t)
	f.create(t, newPlaybook("reporter"))
	require.Equal(t, profiles.OriginStored, f.live.Current().Playbooks["reporter"].Origin)

	writePlaybookFile(t, dir, "reporter", "The file's report.")
	require.NoError(t, f.reload(t))

	assert.Equal(t, profiles.OriginFile, f.live.Current().Playbooks["reporter"].Origin)
	res, err := f.svc.GetProfile(loginCtx("alice"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	var shadowed []string
	for _, s := range res.Msg.GetPlaybooks() {
		if s.GetShadowed() {
			shadowed = append(shadowed, s.GetName())
		}
	}
	assert.Equal(t, []string{"reporter"}, shadowed)
}

// A conductor started with no directory has nothing to go back to, and says so rather than
// offering a button that cannot work.
func TestReloadingWithNoProfileDirectorySaysSo(t *testing.T) {
	f := newProfileFixture(t)
	err := f.reload(t)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
