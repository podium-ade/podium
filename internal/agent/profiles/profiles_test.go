package profiles

import (
	"fmt"
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
	assert.Equal(t, 0, general.Priority, "a playbook that names no priority queues with everything else")
	assert.Equal(t, DefaultMaxTurns, general.MaxTurns)
	assert.Equal(t, 30*time.Minute, general.Timeout.Std())
	assert.Equal(t, "claude-opus-5", p.ModelFor(general), "a playbook with no model uses the profile's")
	assert.False(t, general.Interactive, "interactive is off unless the playbook asks for it")
}

func TestAPlaybookMayOptIntoInteractiveTurns(t *testing.T) {
	files := base()
	files["playbooks/general.yaml"] = `image: alpine:3
system_prompt: answer
allowed_tools: [read]
interactive: true
`
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.True(t, p.Playbooks["general"].Interactive)
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

// A playbook's priority is read off its file, and negative is a legitimate thing to want: a
// two-hour dogfood run should wait behind whatever somebody is watching.
func TestAPlaybookCarriesItsQueuePriority(t *testing.T) {
	for _, priority := range []int{7, -5, MinPriority, MaxPriority} {
		files := base()
		files["playbooks/general.yaml"] = goodPlaybook + fmt.Sprintf("priority: %d\n", priority)
		p, err := Load(write(t, files))
		require.NoError(t, err)
		assert.Equal(t, priority, p.Playbooks["general"].Priority)
	}
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
		{"a priority nothing could distinguish", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "priority: 5000\n"
		}, []string{"playbooks/general.yaml", "priority must be between -1000 and 1000"}},
		{"a missing prompt file", func(f map[string]string) {
			delete(f, "prompts/profile.md")
		}, []string{"profile.yaml", "system_prompt"}},
		{"an empty prompt file", func(f map[string]string) {
			f["prompts/profile.md"] = "   \n"
		}, []string{"profile.yaml", "is empty"}},
		{"no system prompt at all", func(f map[string]string) {
			f["playbooks/general.yaml"] = "image: alpine:3\nallowed_tools: [read]\n"
		}, []string{"playbooks/general.yaml", "system_prompt is required"}},
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
		{"a skill name the harness would refuse", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "skills: [Pr_Review]\n"
		}, []string{"playbooks/general.yaml", "skill name", "must match"}},
		{"the same skill twice", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "skills: [pr-review, pr-review]\n"
		}, []string{"playbooks/general.yaml", `skills names "pr-review" twice`}},
		{"more skills than the cap", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook +
				"skills: [a, b, c, d, e, f, g, h, i]\n"
		}, []string{"playbooks/general.yaml", "the limit is 8"}},
		{"a skill bundle env var of its own", func(f map[string]string) {
			f["playbooks/general.yaml"] = goodPlaybook + "env: {PODIUM_AGENT_SKILL_PR_REVIEW: x}\n"
		}, []string{"playbooks/general.yaml", "AGENT_SKILL_PR_REVIEW"}},
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

// A playbook's Agent Skills are an allow-list of names and nothing more: whether a name has
// a directory behind it is the conductor's business, not the profile loader's, so a profile
// loads on a machine with no skills directory at all.
func TestAPlaybookDeclaresSkillsByNameAndDefaultsToNone(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	assert.Empty(t, p.Playbooks["general"].Skills, "nothing is implicit")

	files := base()
	files["playbooks/general.yaml"] = goodPlaybook + "skills: [pr-review, release-notes]\n"
	p, err = Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, []string{"pr-review", "release-notes"}, p.Playbooks["general"].Skills)
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

func TestAProfileWithNoPlaybooksLoads(t *testing.T) {
	p, err := Load(write(t, map[string]string{
		"profile.yaml":       goodProfile,
		"prompts/profile.md": "hi",
	}))
	require.NoError(t, err)
	assert.Empty(t, p.PlaybookNames())
	assert.Equal(t, "general", p.DefaultPlaybook, "the file's default is kept; Select treats a missing name as no playbook")

	sel := p.Select(Routing{Channel: "C1", Text: "hello"})
	assert.Empty(t, sel.Playbook.Name)
}

func TestADanglingDefaultPlaybookStillLoads(t *testing.T) {
	files := base()
	files["profile.yaml"] = `name: podium
display_name: Podium
system_prompt: hi
model: claude-opus-5
default_playbook: coder
`
	p, err := Load(write(t, files))
	require.NoError(t, err)
	assert.Equal(t, []string{"general"}, p.PlaybookNames())
	assert.Equal(t, "coder", p.DefaultPlaybook)
	assert.Empty(t, p.Select(Routing{Text: "hello"}).Playbook.Name)
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

// TestTheAssistantComesFromTheProfileAndNotFromAPlaybook. A conversation is answered on the
// conductor's own host, so there is no image, no workspace and no playbook in that path: the
// prompt, the model and the skills are the profile's own.
func TestTheAssistantComesFromTheProfileAndNotFromAPlaybook(t *testing.T) {
	files := base()
	files["profile.yaml"] = goodProfile + "skills: [validate-pr]\nmax_turns: 12\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	a := p.Assistant()
	assert.Equal(t, []string{"validate-pr"}, a.Skills)
	assert.Equal(t, 12, a.MaxTurns)
}

// TestTheAssistantHasNoTurnCapUnlessOneIsSet. The opposite of a playbook, on purpose: the
// assistant answers a conversation and delegates, so what is worth bounding is the container
// it starts. A cap firing mid-answer says "I ran out of turns" about a turn that had not
// failed, which is how a working delegation came to look like a failure.
func TestTheAssistantHasNoTurnCapUnlessOneIsSet(t *testing.T) {
	p, err := Load(write(t, base()))
	require.NoError(t, err)
	assert.Zero(t, p.Assistant().MaxTurns, "unset means no cap, not the playbook default")
	assert.Empty(t, p.Assistant().Skills, "a profile that names no skills gets none")

	// A playbook still gets one, because nobody is watching a task.
	assert.Equal(t, DefaultMaxTurns, p.Playbooks["general"].MaxTurns)
}

func TestTheAssistantsFieldsAreValidated(t *testing.T) {
	t.Run("a skill name the harness would refuse", func(t *testing.T) {
		files := base()
		files["profile.yaml"] = goodProfile + "skills: [\"Not A Name\"]\n"
		_, err := Load(write(t, files))
		require.ErrorContains(t, err, "skills")
	})

	t.Run("the same skill twice", func(t *testing.T) {
		files := base()
		files["profile.yaml"] = goodProfile + "skills: [validate-pr, validate-pr]\n"
		_, err := Load(write(t, files))
		require.ErrorContains(t, err, `names "validate-pr" twice`)
	})

	t.Run("a negative turn cap", func(t *testing.T) {
		files := base()
		files["profile.yaml"] = goodProfile + "max_turns: -1\n"
		_, err := Load(write(t, files))
		require.ErrorContains(t, err, "max_turns must be at least 1")
	})
}

// TestTheAssistantsModelIsTheProfilesAndTheOverrideStillMoves. There is no playbook to
// inherit from, so the profile's own triple is the starting point — and the composer's
// picker still moves the whole triple, exactly as it does for a task.
func TestTheAssistantsModelIsTheProfilesAndTheOverrideStillMoves(t *testing.T) {
	files := base()
	files["profile.yaml"] = goodProfile + "agent: claude\neffort: high\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	plain := p.ResolveAssistant(Override{})
	assert.Equal(t, "claude", plain.Agent)
	assert.Equal(t, "claude-opus-5", plain.Model)
	assert.Equal(t, "high", plain.Effort)

	// Picking a model picks its backend, which is the one rule shared with a task's.
	moved := p.ResolveAssistant(Override{Model: "grok-4.6"})
	assert.Equal(t, "grok", moved.Agent)
	assert.Equal(t, "grok-4.6", moved.Model)
}

// TestAPlaybooksModelNeverReachesTheAssistant. The two are separate on purpose: the podium
// playbook runs Grok at high effort, and that must not change who answers the chat.
func TestAPlaybooksModelNeverReachesTheAssistant(t *testing.T) {
	files := base()
	files["playbooks/general.yaml"] = goodPlaybook + "agent: grok\nmodel: grok-4.6\neffort: high\n"
	p, err := Load(write(t, files))
	require.NoError(t, err)

	assert.Equal(t, "grok-4.6", p.Resolve(p.Playbooks["general"], Override{}).Model)
	assert.Equal(t, "claude-opus-5", p.ResolveAssistant(Override{}).Model)
}
