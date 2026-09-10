package conductor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/internal/agent/store"
)

// skillsDir writes one usable skill per name and returns the directory holding them.
func skillsDir(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		body := "---\nname: " + name + "\ndescription: Use when testing.\n---\n\nDo the thing.\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, skills.SkillFile), []byte(body), 0o644))
	}
	return root
}

func skillsConductor(t *testing.T, dir string, playbook profiles.Playbook) *Conductor {
	t.Helper()
	playbook.Name = "coder"
	playbook.MaxTurns = 20
	return &Conductor{
		skillsDir: dir,
		profiles: profiles.NewLive(&profiles.Profile{
			Name:            "podium",
			DisplayName:     "Podium",
			Model:           "claude-opus-5",
			DefaultPlaybook: "coder",
			Playbooks:       map[string]profiles.Playbook{"coder": playbook},
		}),
	}
}

// The bundle is delivered beside the brief and never inside it: the brief carries the name,
// the digest and the variable, and the task spec carries the bytes.
func TestATurnCarriesItsSkillsBesideTheBrief(t *testing.T) {
	dir := skillsDir(t, "pr-review", "release-notes")
	playbook := profiles.Playbook{Skills: []string{"pr-review", "release-notes"}}
	c := skillsConductor(t, dir, playbook)
	playbook = c.profiles.Current().Playbooks["coder"]

	bundles, err := c.skillBundles(context.Background(), playbookJob(playbook))
	require.NoError(t, err)
	require.Len(t, bundles, 2)

	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, bundles, nil, choice)
	require.Len(t, b.Playbook.Skills, 2)
	assert.Equal(t, "pr-review", b.Playbook.Skills[0].Name)
	assert.Equal(t, skills.EnvPrefix+"PR_REVIEW", b.Playbook.Skills[0].BundleEnv)
	assert.Len(t, b.Playbook.Skills[0].SHA256, 64)

	spec := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, bundles, nil, choice)
	assert.Equal(t, bundles[0].Encoded, spec.Env[skills.EnvPrefix+"PR_REVIEW"])
	assert.Equal(t, bundles[1].Encoded, spec.Env[skills.EnvPrefix+"RELEASE_NOTES"])
	// The brief itself is untouched by any of this.
	assert.Equal(t, "encoded-brief", spec.Env[BriefEnv])
	// And the digest in the brief is the digest of what was delivered.
	assert.Equal(t, bundles[0].SHA256, b.Playbook.Skills[0].SHA256)
}

// A playbook that names no skills reads no directory and puts nothing on the spec. The
// permission map the runtime writes still denies everything — that is the runtime's job.
func TestAPlaybookWithNoSkillsDeliversNone(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{})
	playbook := c.profiles.Current().Playbooks["coder"]

	bundles, err := c.skillBundles(context.Background(), playbookJob(playbook))
	require.NoError(t, err)
	assert.Empty(t, bundles)

	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, bundles, nil, choice)
	assert.Nil(t, b.Playbook.Skills)

	spec := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, bundles, nil, choice)
	for k := range spec.Env {
		assert.NotContains(t, k, skills.EnvPrefix)
	}
}

// A skill the conductor cannot deliver fails the turn rather than running it with fewer
// skills than the playbook describes. The message names what is missing.
func TestASkillThatCannotBeDeliveredFailsTheTurn(t *testing.T) {
	t.Run("no skills directory on this host", func(t *testing.T) {
		c := skillsConductor(t, "", profiles.Playbook{Skills: []string{"pr-review"}})
		_, err := c.skillBundles(context.Background(), playbookJob(c.profiles.Current().Playbooks["coder"]))
		require.ErrorContains(t, err, skills.DirEnv+" is not set")
		require.ErrorContains(t, err, "coder")
	})

	t.Run("a name with nothing behind it", func(t *testing.T) {
		c := skillsConductor(t, skillsDir(t, "pr-review"), profiles.Playbook{Skills: []string{"missing"}})
		_, err := c.skillBundles(context.Background(), playbookJob(c.profiles.Current().Playbooks["coder"]))
		require.ErrorContains(t, err, `no skill named "missing"`)
		require.ErrorContains(t, err, "coder")
	})
}

// chatSource is the smallest Source taskSpec needs. taskSpec asks it for its kind and
// nothing else; the rest is here because the interface is.
type chatSource struct{}

func (chatSource) Kind() string                { return SourceChat }
func (chatSource) Events() <-chan InboundEvent { return nil }
func (chatSource) FetchTranscript(context.Context, string) ([]BriefEntry, error) {
	return nil, nil
}
func (chatSource) Post(context.Context, string, Outbound) (string, error) { return "", nil }
func (chatSource) Edit(context.Context, string, string, Outbound) error   { return nil }
func (chatSource) Attach(context.Context, string, Attachment) error       { return nil }
func (chatSource) React(context.Context, string, Reaction) error          { return nil }

// TestTheAssistantsSkillsComeFromTheProfile. The assistant is not a playbook, so a playbook
// naming skills does not give it any and profile.yaml's list does.
func TestTheAssistantsSkillsComeFromTheProfile(t *testing.T) {
	dir := skillsDir(t, "pr-review", "release-notes")
	c := skillsConductor(t, dir, profiles.Playbook{Skills: []string{"release-notes"}})
	profile := c.profiles.Current()
	profile.Skills = []string{"pr-review"}

	bundles, err := c.skillBundles(context.Background(), assistantJob(profile.Assistant()))
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	assert.Equal(t, "pr-review", bundles[0].Name)
}
