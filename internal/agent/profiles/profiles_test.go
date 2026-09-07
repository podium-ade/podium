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
default_playbook: general
`

const goodPlaybook = `image: podium-agent-runtime:dev
system_prompt: answer the question
allowed_tools: [read, grep]
`

func base() map[string]string {
	return map[string]string{
		"profile.yaml":           goodProfile,
		"prompts/profile.md":     "you are Podium",
		"playbooks/general.yaml": goodPlaybook,
	}
}

func TestLoadReadsTheProfileAndItsPlaybooks(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)

	assert.Equal(t, "podium", p.Name)
	assert.Equal(t, "Podium", p.DisplayName)
	assert.Equal(t, "you are Podium", p.SystemPrompt, "a file: prompt is resolved at load")
	assert.Equal(t, "claude-opus-5", p.Model)
	assert.Equal(t, []string{"general"}, p.PlaybookNames())

	general := p.Playbooks["general"]
	assert.Equal(t, "general", general.Name, "the playbook's name comes from the file name")
	assert.Equal(t, DefaultMaxTurns, general.MaxTurns)
	assert.Equal(t, 30*time.Minute, general.Timeout.Std())
	assert.Equal(t, "claude-opus-5", p.ModelFor(general), "a playbook with no model uses the profile's")
}

// A `file:` prompt is relative to the file that names it, which is not the same directory
// for profile.yaml and playbooks/general.yaml.
func TestAFilePromptIsRelativeToTheFileThatNamesIt(t *testing.T) {
	files := base()
	files["playbooks/general.yaml"] = `image: alpine:3
system_prompt: file:../prompts/general.md
allowed_tools: [read]
`
	files["prompts/general.md"] = "answer it"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "answer it", p.Playbooks["general"].SystemPrompt)
}

func TestEveryLoadFailureNamesTheFile(t *testing.T) {
	tests := []struct {
		name  string
		files func(map[string]string)
		want  []string
	}{
		{"an unknown key", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "systemprompt: oops\n"
		}, []string{"playbooks/general.yaml", "systemprompt"}},
		{"no image", func(f map[string]string) {
			f["playbooks/general.yaml"] = "system_prompt: x\nallowed_tools: [read]\n"
		}, []string{"playbooks/general.yaml", "image is required"}},
		{"empty allowed_tools", func(f map[string]string) {
			f["playbooks/general.yaml"] = "image: alpine:3\nsystem_prompt: x\nallowed_tools: []\n"
		}, []string{"playbooks/general.yaml", "allowed_tools"}},
		{"the reserved anthropic secret", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + `secrets:
  - {name: podium.agent.anthropic_api_key, target: env, key: ANTHROPIC_API_KEY}
`
		}, []string{"playbooks/general.yaml", "the conductor decides"}},
		{"the brief env var", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "env: {PODIUM_AGENT_TURN: x}\n"
		}, []string{"playbooks/general.yaml", "PODIUM_AGENT_TURN"}},
		{"the anthropic env var", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "env: {ANTHROPIC_API_KEY: x}\n"
		}, []string{"playbooks/general.yaml", "ANTHROPIC_API_KEY"}},
		{"a docker playbook pointing DOCKER_HOST somewhere else", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "docker: true\nenv: {DOCKER_HOST: tcp://elsewhere:2375}\n"
		}, []string{"playbooks/general.yaml", "DOCKER_HOST"}},
		{"a missing prompt file", func(f map[string]string) {
			delete(f, "prompts/profile.md")
		}, []string{"profile.yaml", "system_prompt"}},
		{"an empty prompt file", func(f map[string]string) {
			f["prompts/profile.md"] = "   \n"
		}, []string{"profile.yaml", "is empty"}},
		{"no system prompt at all", func(f map[string]string) {
			f["playbooks/general.yaml"] = "image: alpine:3\nallowed_tools: [read]\n"
		}, []string{"playbooks/general.yaml", "system_prompt is required"}},
		{"a default_playbook that does not exist", func(f map[string]string) {
			f["profile.yaml"] = goodProfile + "" // replaced below
			f["profile.yaml"] = `name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
default_playbook: coder
`
		}, []string{"profile.yaml", "default_playbook"}},
		{"a bad profile name", func(f map[string]string) {
			f["profile.yaml"] = `name: Podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
default_playbook: general
`
		}, []string{"profile.yaml", "must match"}},
		{"two playbooks claiming one channel", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "slack_channels: [C1]\n"
			f["playbooks/coder.yaml"] = goodPlaybook + "slack_channels: [C1]\n"
		}, []string{"profile.yaml", "both claim slack channel C1"}},
		{"a playbook file name that is not a playbook name", func(f map[string]string) {
			f["playbooks/Coder.yaml"] = goodPlaybook
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

// Without the flag there is no attached daemon to collide with, so a playbook may point its
// turn at whatever engine it likes.
func TestAPlaybookWithoutDockerMaySetDockerHost(t *testing.T) {
	files := base()
	files["playbooks/general.yaml"] = goodPlaybook + "env: {DOCKER_HOST: tcp://elsewhere:2375}\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "tcp://elsewhere:2375", p.Playbooks["general"].Env["DOCKER_HOST"])
	assert.False(t, p.Playbooks["general"].Docker)
}

func TestAProfileWithNoPlaybooksIsRefused(t *testing.T) {
	_, err := Load(write(t, map[string]string{
		"profile.yaml":       goodProfile,
		"prompts/profile.md": "hi",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds no playbooks")
}

// A profile directory written before the rename holds skills/ and no playbooks/. Loading it
// as "no playbooks here" would start a conductor that can do nothing at all, so the error
// names the rename and what to do about it.
func TestAProfileDirectoryStillHoldingSkillsIsRefusedByName(t *testing.T) {
	_, err := Load(write(t, map[string]string{
		"profile.yaml":        goodProfile,
		"prompts/profile.md":  "hi",
		"skills/general.yaml": goodPlaybook,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds skills/ and no playbooks/")
	assert.Contains(t, err.Error(), "now called a playbook")
	assert.Contains(t, err.Error(), "default_playbook")
}

// Both directories at once is a rename half done: playbooks/ is authoritative and the
// leftover skills/ is not read.
func TestAProfileDirectoryHoldingBothReadsPlaybooks(t *testing.T) {
	files := base()
	files["skills/leftover.yaml"] = goodPlaybook
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, []string{"general"}, p.PlaybookNames())
}

// The routing rules, in order, plus the one that must NOT be a rule.
func TestSelect(t *testing.T) {
	files := base()
	files["playbooks/coder.yaml"] = goodPlaybook + "slack_channels: [C-CODE]\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	t.Run("a /playbook prefix picks the playbook and is stripped", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/coder fix it"})
		assert.Equal(t, "coder", sel.Playbook.Name)
		assert.Equal(t, "fix it", sel.Instruction)
		assert.True(t, sel.Explicit)
	})

	t.Run("a bare /playbook with no words still picks it", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/coder"})
		assert.Equal(t, "coder", sel.Playbook.Name)
		assert.Empty(t, sel.Instruction)
	})

	// Somebody typing /shrug must not break the bot, and must not have their text eaten.
	t.Run("an unknown /name falls through with the text intact", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/shrug hi"})
		assert.Equal(t, "general", sel.Playbook.Name)
		assert.Equal(t, "/shrug hi", sel.Instruction)
		assert.False(t, sel.Explicit)
	})

	t.Run("a path is not a playbook selector", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C1", Text: "/etc/hosts is wrong"})
		assert.Equal(t, "general", sel.Playbook.Name)
		assert.Equal(t, "/etc/hosts is wrong", sel.Instruction)
	})

	t.Run("a claimed channel beats the default", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-CODE", Text: "please look"})
		assert.Equal(t, "coder", sel.Playbook.Name)
		assert.False(t, sel.Explicit, "a channel claim is routing, not somebody naming a playbook")
	})

	t.Run("a /playbook prefix beats a claimed channel", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-CODE", Text: "/general what is this"})
		assert.Equal(t, "general", sel.Playbook.Name)
		assert.Equal(t, "what is this", sel.Instruction)
	})

	t.Run("a playbook the source knows beats everything", func(t *testing.T) {
		sel := p.Select(Routing{Playbook: "coder", Channel: "C1", Text: "/general hi"})
		assert.Equal(t, "coder", sel.Playbook.Name)
		assert.True(t, sel.Explicit)
		assert.Equal(t, "/general hi", sel.Instruction, "the source's choice leaves the text alone")
	})

	t.Run("nothing matches, so the default runs", func(t *testing.T) {
		sel := p.Select(Routing{Channel: "C-OTHER", Text: "  hello  "})
		assert.Equal(t, "general", sel.Playbook.Name)
		assert.Equal(t, "hello", sel.Instruction)
	})
}

// TestASourcesDefaultLosesToATypedPlaybookAndBeatsTheProfiles is the web chat's precedence:
// the chip is knowledge and wins outright, a typed /playbook is the most specific thing a
// human can say next, and chat_default_playbook is only where a message with neither lands.
func TestASourcesDefaultLosesToATypedPlaybookAndBeatsTheProfiles(t *testing.T) {
	files := base()
	files["playbooks/coder.yaml"] = goodPlaybook
	files["playbooks/analyst.yaml"] = goodPlaybook
	p, err := Load(write(t, files))
	require.NoError(t, err)

	t.Run("the source's default beats profile.default_playbook", func(t *testing.T) {
		sel := p.Select(Routing{DefaultPlaybook: "analyst", Text: "how many accounts"})
		assert.Equal(t, "analyst", sel.Playbook.Name)
		assert.False(t, sel.Explicit, "a default is not somebody naming a playbook")
	})

	t.Run("a typed /playbook beats the source's default", func(t *testing.T) {
		sel := p.Select(Routing{DefaultPlaybook: "analyst", Text: "/general reply with pong"})
		assert.Equal(t, "general", sel.Playbook.Name)
		assert.Equal(t, "reply with pong", sel.Instruction, "a matched prefix is stripped")
		assert.True(t, sel.Explicit)
	})

	t.Run("the chip beats a typed /playbook", func(t *testing.T) {
		sel := p.Select(Routing{Playbook: "coder", DefaultPlaybook: "analyst", Text: "/general hi"})
		assert.Equal(t, "coder", sel.Playbook.Name)
		assert.Equal(t, "/general hi", sel.Instruction)
		assert.True(t, sel.Explicit)
	})

	t.Run("an unknown /name still falls through to the source's default", func(t *testing.T) {
		sel := p.Select(Routing{DefaultPlaybook: "analyst", Text: "/shrug hi"})
		assert.Equal(t, "analyst", sel.Playbook.Name)
		assert.Equal(t, "/shrug hi", sel.Instruction)
		assert.False(t, sel.Explicit)
	})
}

func TestPlaybookPrefixRE(t *testing.T) {
	for _, text := range []string{"/coder fix", "/coder\nfix", "/coder"} {
		assert.NotNil(t, PlaybookPrefixRE.FindStringSubmatch(text), "%q must match", text)
	}
	for _, text := range []string{"x /coder", "/Coder fix", "/coder/fix", "//coder", "/etc/hosts"} {
		assert.Nil(t, PlaybookPrefixRE.FindStringSubmatch(text), "%q must not match", text)
	}
}

// TestExactlyOnePlaybookMayTakeLinearTickets. Two is refused at load: a ticket has no channel
// and no /playbook prefix, so there is nothing to disambiguate two claims with, and routing
// that resolved by map iteration order would be worse than a refusal.
func TestExactlyOnePlaybookMayTakeLinearTickets(t *testing.T) {
	files := base()
	files["playbooks/coder.yaml"] = goodPlaybook + "linear: true\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, "coder", p.LinearPlaybook())
	assert.True(t, p.Playbooks["coder"].Linear)
	assert.False(t, p.Playbooks["general"].Linear)

	files["playbooks/fixer.yaml"] = goodPlaybook + "linear: true\n"
	_, err = Load(write(t, files))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder")
	assert.Contains(t, err.Error(), "fixer")
	assert.Contains(t, err.Error(), "linear: true")
}

// TestNoPlaybookTakingLinearTicketsIsAllowed. A bot with no Linear at all is a supported
// deployment — most of them — so zero is not a load error. The Linear source is what
// refuses to start when a key is set and no playbook claims tickets, because that is where the
// key is known.
func TestNoPlaybookTakingLinearTicketsIsAllowed(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	assert.Empty(t, p.LinearPlaybook())
}

// TestASourceChosenPlaybookWinsOverEveryRoutingRule. It is how a Linear ticket reaches the
// coder playbook: the source names it and Select honours a non-empty playbook first, so neither a
// /prefix in the ticket's text nor a channel claim can redirect it.
func TestASourceChosenPlaybookWinsOverEveryRoutingRule(t *testing.T) {
	files := base()
	files["playbooks/coder.yaml"] = goodPlaybook + "linear: true\nslack_channels: [C999]\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	sel := p.Select(Routing{Playbook: "coder", Channel: "C999", Text: "/general please just answer"})
	assert.Equal(t, "coder", sel.Playbook.Name)
	assert.True(t, sel.Explicit)
	assert.Equal(t, "/general please just answer", sel.Instruction,
		"a ticket's text is not a command line: no prefix is stripped")
}
