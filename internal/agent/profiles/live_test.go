package profiles

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// stored is a minimally valid playbook of the kind the web UI creates.
func stored(name string) Playbook {
	return Playbook{
		Name:         name,
		Image:        "example.invalid/agent:dev",
		SystemPrompt: "answer the question",
		AllowedTools: []string{"read"},
	}
}

func TestAStoredPlaybookIsValidatedByTheRulesAFileIsHeldTo(t *testing.T) {
	ok, err := ValidateStoredPlaybook(stored("reporter"))
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxTurns, ok.MaxTurns, "the file loader's defaults apply here too")
	assert.Equal(t, 30*time.Minute, ok.Timeout.Std())
	assert.Equal(t, OriginStored, ok.Origin)

	for _, tc := range []struct {
		name     string
		playbook Playbook
		want     string
	}{
		{"no name", Playbook{Image: "i", SystemPrompt: "p", AllowedTools: []string{"read"}},
			`playbook name "" must match`},
		{"a name Slack cannot type", func() Playbook { s := stored("Reporter"); return s }(),
			`must match`},
		{"no prompt", func() Playbook { s := stored("x"); s.SystemPrompt = " "; return s }(),
			"system_prompt is required"},
		{"no tools", func() Playbook { s := stored("x"); s.AllowedTools = nil; return s }(),
			"allowed_tools is required"},
		// The reserved secrets are the one rule a playbook is refused for naming, and it is
		// not about privilege: the conductor attaches both itself, so a playbook listing one
		// is asking for something it is already getting.
		{"the reserved provider key", func() Playbook {
			s := stored("x")
			s.Secrets = []spec.SecretRef{{Name: AnthropicKeySecret, Key: "ANTHROPIC_API_KEY"}}
			return s
		}(), "secrets may not name " + AnthropicKeySecret},
		{"the reserved memory key", func() Playbook {
			s := stored("x")
			s.Secrets = []spec.SecretRef{{Name: MemoryKeySecret, Key: "PODIUM_MEMORY_API_KEY"}}
			return s
		}(), "secrets may not name " + MemoryKeySecret},
		{"the brief's env var", func() Playbook {
			s := stored("x")
			s.Env = map[string]string{BriefEnv: "anything"}
			return s
		}(), "env may not set " + BriefEnv},
		{"the provider key's env var", func() Playbook {
			s := stored("x")
			s.Env = map[string]string{AnthropicKeyEnv: "sk-not-a-key"}
			return s
		}(), "env may not set " + AnthropicKeyEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateStoredPlaybook(tc.playbook)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A stored playbook has no file beside it, so there is no directory to resolve a file: prompt
// against — and reading one off the conductor's disk on a browser's say-so is not something
// to do by accident.
func TestAStoredPlaybookCannotNameAPromptFile(t *testing.T) {
	s := stored("reporter")
	s.SystemPrompt = "file:../../etc/passwd"
	_, err := ValidateStoredPlaybook(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be the prompt itself")
}

// A playbook may name any registered secret, exactly as a task spec may. There is no
// allow-list, because there is none on CreateTask either: anybody who can submit a task
// already mounts any secret into an image of their choosing. See docs/security.md.
func TestAStoredPlaybookMayNameAnyOrdinarySecret(t *testing.T) {
	s := stored("reporter")
	s.Secrets = []spec.SecretRef{
		{Name: "podium.agent.github_token", Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN"},
		{Name: "some.other.credential", Target: spec.SecretTargetFile, Key: "/podium/secrets/x.json"},
	}
	_, err := ValidateStoredPlaybook(s)
	require.NoError(t, err)
}

func fileProfile(t *testing.T) *Profile {
	t.Helper()
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	return p
}

func TestMergeAddsStoredPlaybooksTheFilesDoNotDefine(t *testing.T) {
	files := fileProfile(t)
	got, shadowed, err := Merge(files, Overrides{}, []Playbook{stored("reporter")})
	require.NoError(t, err)
	assert.Empty(t, shadowed)
	assert.Equal(t, []string{"general", "reporter"}, got.PlaybookNames())
	assert.Equal(t, OriginFile, got.Playbooks["general"].Origin)
	assert.Equal(t, OriginStored, got.Playbooks["reporter"].Origin)
	assert.NotContains(t, files.Playbooks, "reporter", "merge does not write into the file profile")
}

// The precedence rule, and the reason for it: which of two definitions runs must not depend
// on which was written last.
func TestAFilePlaybookBeatsAStoredPlaybookOfTheSameName(t *testing.T) {
	files := fileProfile(t)
	rogue := stored("general")
	rogue.Image = "example.invalid/not-what-the-file-says:dev"

	got, shadowed, err := Merge(files, Overrides{}, []Playbook{rogue})
	require.NoError(t, err)
	assert.Equal(t, []string{"general"}, shadowed)
	assert.Equal(t, "podium-agent-runtime:dev", got.Playbooks["general"].Image)
	assert.Equal(t, OriginFile, got.Playbooks["general"].Origin)
}

func TestAnOverrideReplacesTheFileValueAndAnEmptyOneClearsIt(t *testing.T) {
	files := fileProfile(t)

	got, _, err := Merge(files, Overrides{DisplayName: "Bot", Model: "claude-haiku-5"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bot", got.DisplayName)
	assert.Equal(t, "claude-haiku-5", got.Model)
	assert.Equal(t, "general", got.DefaultPlaybook, "a field nobody overrode still comes from the file")

	back, _, err := Merge(files, Overrides{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Podium", back.DisplayName)
	assert.Equal(t, "claude-opus-5", back.Model)
}

func TestAnOverrideMayNameAStoredPlaybookAsTheDefault(t *testing.T) {
	files := fileProfile(t)
	got, _, err := Merge(files, Overrides{DefaultPlaybook: "reporter"}, []Playbook{stored("reporter")})
	require.NoError(t, err)
	assert.Equal(t, "reporter", got.DefaultPlaybook)
}

// The merged profile is validated by exactly the code that validates the directory, so a
// combination that could not have been written as files is refused here too.
func TestMergeRefusesWhatLoadWouldRefuse(t *testing.T) {
	files := fileProfile(t)

	_, _, err := Merge(files, Overrides{DefaultPlaybook: "nope"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `default_playbook "nope" names no playbook`)

	a, b := stored("one"), stored("two")
	a.Linear, b.Linear = true, true
	_, _, err = Merge(files, Overrides{}, []Playbook{a, b})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all set linear: true")

	c, d := stored("three"), stored("four")
	c.SlackChannels = []string{"C1"}
	d.SlackChannels = []string{"C1"}
	_, _, err = Merge(files, Overrides{}, []Playbook{c, d})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both claim slack channel C1")
}

func TestOverridesReportWhichFieldsTheySupply(t *testing.T) {
	assert.Empty(t, Overrides{}.Fields())
	assert.Equal(t,
		[]string{FieldDisplayName, FieldDefaultPlaybook},
		Overrides{DisplayName: "Bot", DefaultPlaybook: "general"}.Fields())
	assert.Empty(t, Overrides{DisplayName: "   "}.Trim().Fields(),
		"a field cleared to whitespace is a cleared override, not an invalid value")
}

// Live is what makes a change reach a running conductor: readers take Current() per use, so
// a swap is the whole of the reload.
func TestLiveSwapsTheProfileEveryLaterReaderSees(t *testing.T) {
	files := fileProfile(t)
	live := NewLive(files)
	assert.Same(t, files, live.Files())
	assert.Equal(t, []string{"general"}, live.Current().PlaybookNames())

	next, _, err := Merge(files, Overrides{}, []Playbook{stored("reporter")})
	require.NoError(t, err)
	live.Set(next)

	assert.Equal(t, []string{"general", "reporter"}, live.Current().PlaybookNames())
	assert.Same(t, files, live.Files(), "the file half never changes while the process runs")

	// A turn takes a Playbook by value, so a swap cannot change one that is already in flight.
	inFlight := live.Current().Playbooks["reporter"]
	changed := stored("reporter")
	changed.Image = "example.invalid/other:dev"
	after, _, err := Merge(files, Overrides{}, []Playbook{changed})
	require.NoError(t, err)
	live.Set(after)
	assert.Equal(t, "example.invalid/agent:dev", inFlight.Image)
}

func TestLiveIsSafeWhenThereIsNoProfileAtAll(t *testing.T) {
	var live *Live
	assert.Nil(t, live.Current())
	assert.Nil(t, live.Files())
	live.Set(&Profile{})
}

// A stored playbook with no image is accepted, like a file-loaded one, and comes out carrying
// the matched runtime.
func TestAStoredPlaybookWithNoImageGetsTheDefault(t *testing.T) {
	s := stored("x")
	s.Image = ""
	got, err := ValidateStoredPlaybook(s)
	require.NoError(t, err)
	require.Equal(t, DefaultRuntimeImage(), got.Image)
}
