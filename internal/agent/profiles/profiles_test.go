package profiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write builds a profile directory from a map of relative path to content.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	return dir
}

const goodProfile = `name: podium
display_name: Podium
system_prompt: file:./prompts/profile.md
model: claude-opus-5
default_skill: general
`

const goodSkill = `image: podium-agent-runtime:dev
system_prompt: answer the question
allowed_tools: [Read, Grep]
`

func base() map[string]string {
	return map[string]string{
		"profile.yaml":        goodProfile,
		"prompts/profile.md":  "you are Podium",
		"skills/general.yaml": goodSkill,
	}
}

func TestLoadReadsTheProfileAndItsSkills(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)

	assert.Equal(t, "podium", p.Name)
	assert.Equal(t, "Podium", p.DisplayName)
	assert.Equal(t, "you are Podium", p.SystemPrompt, "a file: prompt is resolved at load")
	assert.Equal(t, "claude-opus-5", p.Model)
	assert.Equal(t, []string{"general"}, p.SkillNames())

	general := p.Skills["general"]
	assert.Equal(t, "general", general.Name, "the skill's name comes from the file name")
	assert.Equal(t, DefaultMaxTurns, general.MaxTurns)
	assert.Equal(t, 30*time.Minute, general.Timeout.Std())
	assert.Equal(t, "claude-opus-5", p.ModelFor(general), "a skill with no model uses the profile's")
}

// A `file:` prompt is relative to the file that names it, which is not the same directory
// for profile.yaml and skills/general.yaml.
func TestAFilePromptIsRelativeToTheFileThatNamesIt(t *testing.T) {
	files := base()
	files["skills/general.yaml"] = `image: alpine:3
system_prompt: file:../prompts/general.md
allowed_tools: [Read]
`
	files["prompts/general.md"] = "answer it"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "answer it", p.Skills["general"].SystemPrompt)
}

func TestEveryLoadFailureNamesTheFile(t *testing.T) {
	tests := []struct {
		name  string
		files func(map[string]string)
		want  []string
	}{
		{"an unknown key", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + "systemprompt: oops\n"
		}, []string{"skills/general.yaml", "systemprompt"}},
		{"no image", func(f map[string]string) {
			f["skills/general.yaml"] = "system_prompt: x\nallowed_tools: [Read]\n"
		}, []string{"skills/general.yaml", "image is required"}},
		{"empty allowed_tools", func(f map[string]string) {
			f["skills/general.yaml"] = "image: alpine:3\nsystem_prompt: x\nallowed_tools: []\n"
		}, []string{"skills/general.yaml", "allowed_tools"}},
		{"the reserved anthropic secret", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + `secrets:
  - {name: podium.agent.anthropic_api_key, target: env, key: ANTHROPIC_API_KEY}
`
		}, []string{"skills/general.yaml", "the conductor decides"}},
		{"the brief env var", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + "env: {PODIUM_AGENT_TURN: x}\n"
		}, []string{"skills/general.yaml", "PODIUM_AGENT_TURN"}},
		{"the anthropic env var", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + "env: {ANTHROPIC_API_KEY: x}\n"
		}, []string{"skills/general.yaml", "ANTHROPIC_API_KEY"}},
		{"a docker skill pointing DOCKER_HOST somewhere else", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + "docker: true\nenv: {DOCKER_HOST: tcp://elsewhere:2375}\n"
		}, []string{"skills/general.yaml", "DOCKER_HOST"}},
		{"a missing prompt file", func(f map[string]string) {
			delete(f, "prompts/profile.md")
		}, []string{"profile.yaml", "system_prompt"}},
		{"an empty prompt file", func(f map[string]string) {
			f["prompts/profile.md"] = "   \n"
		}, []string{"profile.yaml", "is empty"}},
		{"no system prompt at all", func(f map[string]string) {
			f["skills/general.yaml"] = "image: alpine:3\nallowed_tools: [Read]\n"
		}, []string{"skills/general.yaml", "system_prompt is required"}},
		{"a default_skill that does not exist", func(f map[string]string) {
			f["profile.yaml"] = goodProfile + "" // replaced below
			f["profile.yaml"] = `name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
default_skill: coder
`
		}, []string{"profile.yaml", "default_skill"}},
		{"a bad profile name", func(f map[string]string) {
			f["profile.yaml"] = `name: Podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
default_skill: general
`
		}, []string{"profile.yaml", "must match"}},
		{"two skills claiming one channel", func(f map[string]string) {
			f["skills/general.yaml"] = goodSkill + "slack_channels: [C1]\n"
			f["skills/coder.yaml"] = goodSkill + "slack_channels: [C1]\n"
		}, []string{"profile.yaml", "both claim slack channel C1"}},
		{"a skill file name that is not a skill name", func(f map[string]string) {
			f["skills/Coder.yaml"] = goodSkill
		}, []string{"Coder.yaml", "must match"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := base()
			tc.files(files)
			_, err := Load(write(t, files))
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// Without the flag there is no attached daemon to collide with, so a skill may point its
// turn at whatever engine it likes.
func TestASkillWithoutDockerMaySetDockerHost(t *testing.T) {
	files := base()
	files["skills/general.yaml"] = goodSkill + "env: {DOCKER_HOST: tcp://elsewhere:2375}\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "tcp://elsewhere:2375", p.Skills["general"].Env["DOCKER_HOST"])
	assert.False(t, p.Skills["general"].Docker)
}

func TestAProfileWithNoSkillsIsRefused(t *testing.T) {
	_, err := Load(write(t, map[string]string{
		"profile.yaml":       goodProfile,
		"prompts/profile.md": "hi",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds no skills")
}

// The routing rules, in order, plus the one that must NOT be a rule.
func TestSelect(t *testing.T) {
	files := base()
	files["skills/coder.yaml"] = goodSkill + "slack_channels: [C-CODE]\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	t.Run("a /skill prefix picks the skill and is stripped", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/coder fix it"})
		assert.Equal(t, "coder", sel.Skill.Name)
		assert.Equal(t, "fix it", sel.Instruction)
		assert.True(t, sel.Explicit)
	})

	t.Run("a bare /skill with no words still picks it", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/coder"})
		assert.Equal(t, "coder", sel.Skill.Name)
		assert.Empty(t, sel.Instruction)
	})

	// Somebody typing /shrug must not break the bot, and must not have their text eaten.
	t.Run("an unknown /name falls through with the text intact", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/shrug hi"})
		assert.Equal(t, "general", sel.Skill.Name)
		assert.Equal(t, "/shrug hi", sel.Instruction)
		assert.False(t, sel.Explicit)
	})

	t.Run("a path is not a skill selector", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/etc/hosts is wrong"})
		assert.Equal(t, "general", sel.Skill.Name)
		assert.Equal(t, "/etc/hosts is wrong", sel.Instruction)
	})

	t.Run("a claimed channel beats the default", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-CODE", Text: "please look"})
		assert.Equal(t, "coder", sel.Skill.Name)
		assert.False(t, sel.Explicit, "a channel claim is routing, not somebody naming a skill")
	})

	t.Run("a /skill prefix beats a claimed channel", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-CODE", Text: "/general what is this"})
		assert.Equal(t, "general", sel.Skill.Name)
		assert.Equal(t, "what is this", sel.Instruction)
	})

	t.Run("a skill the source knows beats everything", func(t *testing.T) {
		sel := p.Select(Routing{Skill: "coder", Channel: "C1", Text: "/general hi"})
		assert.Equal(t, "coder", sel.Skill.Name)
		assert.True(t, sel.Explicit)
		assert.Equal(t, "/general hi", sel.Instruction, "the source's choice leaves the text alone")
	})

	t.Run("nothing matches, so the default runs", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-OTHER", Text: "  hello  "})
		assert.Equal(t, "general", sel.Skill.Name)
		assert.Equal(t, "hello", sel.Instruction)
	})
}

// TestASourcesDefaultLosesToATypedSkillAndBeatsTheProfiles is the web chat's precedence:
// the chip is knowledge and wins outright, a typed /skill is the most specific thing a
// human can say next, and chat_default_skill is only where a message with neither lands.
func TestASourcesDefaultLosesToATypedSkillAndBeatsTheProfiles(t *testing.T) {
	files := base()
	files["skills/coder.yaml"] = goodSkill
	files["skills/analyst.yaml"] = goodSkill
	p, err := Load(write(t, files))
	require.NoError(t, err)

	t.Run("the source's default beats profile.default_skill", func(t *testing.T) {
		sel := p.Select(Routing{DefaultSkill: "analyst", Text: "how many accounts"})
		assert.Equal(t, "analyst", sel.Skill.Name)
		assert.False(t, sel.Explicit, "a default is not somebody naming a skill")
	})

	t.Run("a typed /skill beats the source's default", func(t *testing.T) {
		sel := p.Select(Routing{DefaultSkill: "analyst", Text: "/general reply with pong"})
		assert.Equal(t, "general", sel.Skill.Name)
		assert.Equal(t, "reply with pong", sel.Instruction, "a matched prefix is stripped")
		assert.True(t, sel.Explicit)
	})

	t.Run("the chip beats a typed /skill", func(t *testing.T) {
		sel := p.Select(Routing{Skill: "coder", DefaultSkill: "analyst", Text: "/general hi"})
		assert.Equal(t, "coder", sel.Skill.Name)
		assert.Equal(t, "/general hi", sel.Instruction)
		assert.True(t, sel.Explicit)
	})

	t.Run("an unknown /name still falls through to the source's default", func(t *testing.T) {
		sel := p.Select(Routing{DefaultSkill: "analyst", Text: "/shrug hi"})
		assert.Equal(t, "analyst", sel.Skill.Name)
		assert.Equal(t, "/shrug hi", sel.Instruction)
		assert.False(t, sel.Explicit)
	})
}

func TestSkillPrefixRE(t *testing.T) {
	for _, text := range []string{"/coder fix", "/coder\nfix", "/coder"} {
		assert.NotNil(t, SkillPrefixRE.FindStringSubmatch(text), "%q must match", text)
	}
	for _, text := range []string{"x /coder", "/Coder fix", "/coder/fix", "//coder", "/etc/hosts"} {
		assert.Nil(t, SkillPrefixRE.FindStringSubmatch(text), "%q must not match", text)
	}
}

// TestExactlyOneSkillMayTakeLinearTickets. Two is refused at load: a ticket has no channel
// and no /skill prefix, so there is nothing to disambiguate two claims with, and routing
// that resolved by map iteration order would be worse than a refusal.
func TestExactlyOneSkillMayTakeLinearTickets(t *testing.T) {
	files := base()
	files["skills/coder.yaml"] = goodSkill + "linear: true\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "coder", p.LinearSkill())
	assert.True(t, p.Skills["coder"].Linear)
	assert.False(t, p.Skills["general"].Linear)

	files["skills/fixer.yaml"] = goodSkill + "linear: true\n"
	_, err = Load(write(t, files))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder")
	assert.Contains(t, err.Error(), "fixer")
	assert.Contains(t, err.Error(), "linear: true")
}

// TestNoSkillTakingLinearTicketsIsAllowed. A bot with no Linear at all is a supported
// deployment — most of them — so zero is not a load error. The Linear source is what
// refuses to start when a key is set and no skill claims tickets, because that is where the
// key is known.
func TestNoSkillTakingLinearTicketsIsAllowed(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	assert.Empty(t, p.LinearSkill())
}

// TestASourceChosenSkillWinsOverEveryRoutingRule. It is how a Linear ticket reaches the
// coder skill: the source names it and Select honours a non-empty skill first, so neither a
// /prefix in the ticket's text nor a channel claim can redirect it.
func TestASourceChosenSkillWinsOverEveryRoutingRule(t *testing.T) {
	files := base()
	files["skills/coder.yaml"] = goodSkill + "linear: true\nslack_channels: [C999]\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	sel := p.Select(Routing{Skill: "coder", Channel: "C999", Text: "/general please just answer"})
	assert.Equal(t, "coder", sel.Skill.Name)
	assert.True(t, sel.Explicit)
	assert.Equal(t, "/general please just answer", sel.Instruction,
		"a ticket's text is not a command line: no prefix is stripped")
}
