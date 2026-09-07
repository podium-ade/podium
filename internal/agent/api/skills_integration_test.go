//go:build integration

package api

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/skills"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

type skillFixture struct {
	svc  *AgentService
	live *profiles.Live
	dir  string
}

// newSkillFixture is a conductor with both halves of the library: a real database and a
// skills directory on its host. The `general` playbook names one skill, so the "which
// playbooks would this break" reporting has something to report.
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

func skillZip(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(root + "/" + name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func (f skillFixture) upload(t *testing.T, login string, req *agentv1.UploadSkillRequest) *agentv1.UploadSkillResponse {
	t.Helper()
	res, err := f.svc.UploadSkill(loginCtx(login), connect.NewRequest(req))
	require.NoError(t, err)
	return res.Msg
}

func (f skillFixture) list(t *testing.T) *agentv1.ListSkillsResponse {
	t.Helper()
	res, err := f.svc.ListSkills(loginCtx("alice"), connect.NewRequest(&agentv1.ListSkillsRequest{}))
	require.NoError(t, err)
	return res.Msg
}

// The whole point of the phase: a skill loaded from a browser, with nobody touching the
// conductor's filesystem, and a turn of the playbook that names it can be handed the bytes.
func TestASkillUploadedThroughTheAPIIsDeliverable(t *testing.T) {
	f := newSkillFixture(t, "")
	raw := skillZip(t, "pr-review", map[string]string{
		skills.SkillFile:         skillMarkdown("pr-review", "Use when reviewing a diff."),
		"reference/checklist.md": "- read the diff\n",
	})

	got := f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: raw, Filename: "pr-review.zip"})
	assert.False(t, got.GetReplaced())
	skill := got.GetSkill()
	assert.Equal(t, "pr-review", skill.GetName())
	assert.Equal(t, "Use when reviewing a diff.", skill.GetDescription())
	assert.Len(t, skill.GetSha256(), 64)
	assert.Equal(t, int32(2), skill.GetFileCount())
	assert.True(t, skill.GetEnabled())
	assert.Equal(t, skills.OriginStored, skill.GetOrigin())
	assert.True(t, skill.GetEditable())
	assert.Equal(t, "alice", skill.GetUploadedBy())
	assert.Equal(t, []string{"general"}, skill.GetPlaybooks())

	// And the library the conductor reads per turn now resolves it, digest and all.
	lib := skills.Library{Store: f.svc.store}
	bundles, err := lib.Bundles(loginCtx("alice"), []string{"pr-review"})
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	assert.Equal(t, skill.GetSha256(), bundles[0].SHA256)
	assert.Equal(t, skills.EnvPrefix+"PR_REVIEW", bundles[0].Env)
	assert.NotEmpty(t, bundles[0].Encoded)
}

// A pasted SKILL.md is the other upload shape and produces the same row.
func TestAPastedSkillMarkdownIsStored(t *testing.T) {
	f := newSkillFixture(t, "")
	got := f.upload(t, "bob", &agentv1.UploadSkillRequest{
		Content: []byte(skillMarkdown("release-notes", "Use when writing release notes.")),
	})
	assert.Equal(t, "release-notes", got.GetSkill().GetName())
	assert.Equal(t, int32(1), got.GetSkill().GetFileCount())
	assert.Empty(t, got.GetSkill().GetPlaybooks())
}

// Replacing a skill every playbook that names it runs is not something a file picker should
// be able to do by accident.
func TestReplacingAStoredSkillTakesAnExplicitReplace(t *testing.T) {
	f := newSkillFixture(t, "")
	body := []byte(skillMarkdown("pr-review", "First."))
	f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: body})

	_, err := f.svc.UploadSkill(loginCtx("bob"), connect.NewRequest(&agentv1.UploadSkillRequest{
		Content: []byte(skillMarkdown("pr-review", "Second.")),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "already stored")
	assert.Contains(t, err.Error(), "alice")

	got := f.upload(t, "bob", &agentv1.UploadSkillRequest{
		Content: []byte(skillMarkdown("pr-review", "Second.")),
		Replace: true,
	})
	assert.True(t, got.GetReplaced())
	assert.Equal(t, "Second.", got.GetSkill().GetDescription())
	assert.Equal(t, "bob", got.GetSkill().GetUploadedBy())
}

// A disabled skill stays disabled when its bundle is replaced: turning one off is a
// decision, and re-uploading the bytes is not a decision to undo it.
func TestReplacingADisabledSkillLeavesItDisabled(t *testing.T) {
	f := newSkillFixture(t, "")
	f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: []byte(skillMarkdown("pr-review", "First."))})

	_, err := f.svc.SetSkillEnabled(loginCtx("alice"), connect.NewRequest(
		&agentv1.SetSkillEnabledRequest{Name: "pr-review", Enabled: false}))
	require.NoError(t, err)

	got := f.upload(t, "alice", &agentv1.UploadSkillRequest{
		Content: []byte(skillMarkdown("pr-review", "Second.")), Replace: true,
	})
	assert.False(t, got.GetSkill().GetEnabled())
}

// Disabling does not quietly narrow a playbook. The turn fails saying which skill and why.
func TestADisabledSkillFailsTheTurnRatherThanBeingSkipped(t *testing.T) {
	f := newSkillFixture(t, "")
	f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: []byte(skillMarkdown("pr-review", "Use always."))})

	res, err := f.svc.SetSkillEnabled(loginCtx("alice"), connect.NewRequest(
		&agentv1.SetSkillEnabledRequest{Name: "pr-review", Enabled: false}))
	require.NoError(t, err)
	assert.False(t, res.Msg.GetSkill().GetEnabled())
	assert.Equal(t, []string{"general"}, res.Msg.GetSkill().GetPlaybooks())

	lib := skills.Library{Store: f.svc.store}
	_, err = lib.Bundles(loginCtx("alice"), []string{"pr-review"})
	require.ErrorContains(t, err, "uploaded but disabled")

	// And back on again.
	res, err = f.svc.SetSkillEnabled(loginCtx("alice"), connect.NewRequest(
		&agentv1.SetSkillEnabledRequest{Name: "pr-review", Enabled: true}))
	require.NoError(t, err)
	assert.True(t, res.Msg.GetSkill().GetEnabled())
}

// The directory is read-only through this API, exactly as a playbooks/<name>.yaml is, and a
// stored skill of the same name is reported as shadowed so it can be deleted.
func TestTheHostDirectoryIsReadOnlyAndShadowsAStoredSkill(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pr-review"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pr-review", skills.SkillFile),
		[]byte(skillMarkdown("pr-review", "From the host.")), 0o644))

	// A conductor with no directory stores it first; the directory appears later, which is
	// the only way the shadowed state can be reached.
	f := newSkillFixture(t, "")
	f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: []byte(skillMarkdown("pr-review", "From the database."))})
	f.svc.skillsDir = dir

	for _, tc := range []struct {
		what string
		call func() error
	}{
		{"upload", func() error {
			_, err := f.svc.UploadSkill(loginCtx("alice"), connect.NewRequest(&agentv1.UploadSkillRequest{
				Content: []byte(skillMarkdown("pr-review", "Third.")), Replace: true,
			}))
			return err
		}},
		{"disable", func() error {
			_, err := f.svc.SetSkillEnabled(loginCtx("alice"), connect.NewRequest(
				&agentv1.SetSkillEnabledRequest{Name: "pr-review"}))
			return err
		}},
		{"delete", func() error {
			_, err := f.svc.DeleteSkill(loginCtx("alice"), connect.NewRequest(
				&agentv1.DeleteSkillRequest{Name: "pr-review"}))
			return err
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "the host wins")
		})
	}

	got := f.list(t)
	assert.Equal(t, dir, got.GetSkillsDir())
	require.Len(t, got.GetSkills(), 2)
	assert.Equal(t, skills.OriginDir, got.GetSkills()[0].GetOrigin())
	assert.Equal(t, "From the host.", got.GetSkills()[0].GetDescription())
	assert.False(t, got.GetSkills()[0].GetEditable())
	assert.False(t, got.GetSkills()[0].GetShadowed())
	assert.Equal(t, skills.OriginStored, got.GetSkills()[1].GetOrigin())
	assert.True(t, got.GetSkills()[1].GetShadowed())

	// The directory wins for the turn as well as for the listing.
	lib := skills.Library{Dir: dir, Store: f.svc.store}
	bundles, err := lib.Bundles(loginCtx("alice"), []string{"pr-review"})
	require.NoError(t, err)
	assert.Equal(t, "From the host.", bundles[0].Description)
}

// A skill a playbook names is deletable, and the response to the list says which playbooks
// would break — because profiles.Load deliberately does not check that a named skill exists,
// and refusing here would be the only place the two disagreed.
func TestDeletingASkillAPlaybookNamesIsAllowedAndVisible(t *testing.T) {
	f := newSkillFixture(t, "")
	f.upload(t, "alice", &agentv1.UploadSkillRequest{Content: []byte(skillMarkdown("pr-review", "Use always."))})
	assert.Equal(t, []string{"general"}, f.list(t).GetSkills()[0].GetPlaybooks())

	_, err := f.svc.DeleteSkill(loginCtx("alice"), connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "pr-review"}))
	require.NoError(t, err)
	assert.Empty(t, f.list(t).GetSkills())

	_, err = f.svc.DeleteSkill(loginCtx("alice"), connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "pr-review"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// Nothing invalid is ever stored, and the refusal names the rule rather than saying no.
func TestAnInvalidUploadIsRefusedAndStoresNothing(t *testing.T) {
	f := newSkillFixture(t, "")
	cases := []struct {
		what, want string
		content    []byte
	}{
		{"no frontmatter", "frontmatter", []byte("just some prose\n")},
		{"no description", "description is required", []byte("---\nname: pr-review\n---\n")},
		{
			"a name the harness would refuse", "must match",
			[]byte(skillMarkdown("Not A Name", "Use always.")),
		},
		{"empty", "the upload is empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			_, err := f.svc.UploadSkill(loginCtx("alice"),
				connect.NewRequest(&agentv1.UploadSkillRequest{Content: tc.content}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.Empty(t, f.list(t).GetSkills())
}

// A traversal never reaches the database, let alone a container.
func TestAnUploadThatTriesToEscapeIsRefused(t *testing.T) {
	f := newSkillFixture(t, "")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		skills.SkillFile:             skillMarkdown("pr-review", "Use always."),
		"../../.ssh/authorized_keys": "ssh-rsa pwned",
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	_, err := f.svc.UploadSkill(loginCtx("alice"), connect.NewRequest(&agentv1.UploadSkillRequest{
		Content: buf.Bytes(), Filename: "evil.zip",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "..")
	assert.Empty(t, f.list(t).GetSkills())
}

// The caps a refusal names are the caps the UI is told about, so the two cannot drift.
func TestTheListReportsTheCapsAnUploadIsHeldTo(t *testing.T) {
	got := newSkillFixture(t, "").list(t)
	assert.Equal(t, int64(skills.MaxBytes), got.GetMaxBytes())
	assert.Equal(t, int32(skills.MaxFiles), got.GetMaxFiles())
	assert.Equal(t, int32(skills.MaxSkills), got.GetMaxPerPlaybook())
}

// A playbook made in a browser can name skills, which is the gap Phase 2 left open.
func TestABrowserCreatedPlaybookCanNameSkills(t *testing.T) {
	f := newSkillFixture(t, "")
	in := newPlaybook("reporter")
	in.Skills = []string{"pr-review", "release-notes"}

	res, err := f.svc.CreatePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: in}))
	require.NoError(t, err)
	assert.Equal(t, []string{"pr-review", "release-notes"}, res.Msg.GetPlaybook().GetSkills())

	// And it is on the profile the conductor reads, so the next turn gets those two.
	assert.Equal(t, []string{"pr-review", "release-notes"}, f.live.Current().Playbooks["reporter"].Skills)

	// Round-tripped through GetProfile as well, which is what the editor reads back.
	profile, err := f.svc.GetProfile(loginCtx("alice"), connect.NewRequest(&agentv1.GetProfileRequest{}))
	require.NoError(t, err)
	for _, p := range profile.Msg.GetPlaybooks() {
		if p.GetName() == "reporter" {
			assert.Equal(t, []string{"pr-review", "release-notes"}, p.GetSkills())
		}
	}
}

// A playbook naming a skill the harness would never accept is refused where every other
// playbook rule is refused, by the same validator profile.yaml goes through.
func TestAPlaybookNamingAnImpossibleSkillIsRefused(t *testing.T) {
	f := newSkillFixture(t, "")
	in := newPlaybook("reporter")
	in.Skills = []string{"Not A Name"}
	_, err := f.svc.CreatePlaybook(loginCtx("alice"), connect.NewRequest(&agentv1.CreatePlaybookRequest{Playbook: in}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "must match")
}
