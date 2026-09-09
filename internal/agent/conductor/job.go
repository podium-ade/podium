package conductor

import (
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
)

// job is what one turn does, as everything below accept needs it: a prompt, a tool list, a
// step cap and a set of Agent Skills.
//
// There are exactly two kinds of job and they are not variations of each other:
//
//   - A TASK's job comes from a playbook. It runs in a container on a node, with whatever
//     that playbook declares — an image, a workspace, repositories, a Docker daemon, a
//     browser — and whichever tools it allows.
//   - A CONVERSATION's job is the ASSISTANT. It runs in the conductor's own process, with
//     the fixed short tool list in host.go, no container and no filesystem it should touch.
//     The playbooks are what it delegates to, not something it is configured from.
//
// One type for both is what keeps runTurn, the brief and the accounting single-path. Keeping
// the container half in one field is what stops it being read where none of it exists: a
// conversation's `playbook` is the zero value, so its repos, its image and its sidecars are
// empty because there are none, and not because something took them away afterwards.
type job struct {
	// name is what this turn is doing, for the brief, the metrics and the log. It is the
	// playbook's name for a task and AssistantName for the assistant.
	//
	// It is NOT what the session and turn rows store — those store playbook.Name, which is
	// empty for the assistant, because a conversation genuinely runs no playbook and a row
	// claiming otherwise would be a row that lies.
	name         string
	systemPrompt string
	allowedTools []string
	// maxTurns caps the turn's steps, and zero means no cap. A playbook always has one; the
	// assistant has one only if profile.yaml set it.
	maxTurns int
	// timeout is the wall clock. It is the ASSISTANT's bound and is zero for a playbook,
	// whose turn is bounded by its task's own timeout on the node instead.
	timeout time.Duration
	skills  []string
	// mcpServers is the MCP servers this turn may reach, by name, out of the conductor's
	// registry. A playbook's own list; empty for the assistant, which reaches other systems
	// by delegating to a playbook that has them rather than by holding their credentials
	// itself.
	mcpServers []string
	// playbook is the container half, and the zero Playbook for the assistant. Only the
	// task path reads it.
	playbook profiles.Playbook
	// onHost is true for the assistant: this turn is a child of the conductor.
	onHost bool
}

// AssistantName is what a turn the conductor answers a conversation with is called. It is a
// label — on the brief, on a metric, in a log line — and never a playbook name: nothing
// resolves it, and a profile that happens to have a playbook of this name is not affected.
const AssistantName = "assistant"

// playbookJob is one playbook's turn, run as a task in a container.
func playbookJob(p profiles.Playbook) job {
	return job{
		name:         p.Name,
		systemPrompt: p.SystemPrompt,
		allowedTools: p.AllowedTools,
		maxTurns:     p.MaxTurns,
		skills:       p.Skills,
		mcpServers:   p.MCPServers,
		playbook:     p,
	}
}

// assistantJob is the turn that answers a conversation, run here.
//
// Its step cap is profile.yaml's verbatim, zero included: unset means no cap, because the
// assistant is a relay and the thing worth bounding is the container it starts.
//
// It carries NO system prompt of its own, and that absence is the point: the assistant is
// the profile, so its prompt is already the brief's profile.system_prompt. A second copy in
// the playbook slot would be the same words twice, and a different second prompt would be a
// persona nobody wrote.
func assistantJob(a profiles.Assistant) job {
	return job{
		name:         AssistantName,
		allowedTools: hostTools,
		maxTurns:     a.MaxTurns,
		timeout:      a.Timeout,
		skills:       a.Skills,
		onHost:       true,
	}
}

// choose is what this turn runs on. The two kinds inherit from different places — a task
// from its playbook, then the profile; the assistant from the profile alone — and the
// override on top of either is the composer's model picker.
func (j job) choose(p *profiles.Profile, o profiles.Override) profiles.Choice {
	if j.onHost {
		return p.ResolveAssistant(o)
	}
	return p.Resolve(j.playbook, o)
}

// provenance is what a retained memory says produced it. The assistant is named as itself
// rather than as a playbook, because there is no playbook of that name to go and read.
func (j job) provenance() string {
	if j.onHost {
		return "podium agent, the assistant"
	}
	return "podium agent, playbook " + j.name
}

// memoryTags is how a memory is found again. A task's carries the playbook that produced it;
// the assistant's carries nothing extra, because `playbook:assistant` would be a tag naming
// a playbook that does not exist and would then be searched for.
func (j job) memoryTags() []string {
	if j.onHost {
		return nil
	}
	return []string{"playbook:" + j.name}
}

// assistantTimeout is the wall clock this turn gets. A playbook's job carries none — a task
// is bounded by its own timeout on the node — so this is only ever asked of the assistant,
// and profiles.Assistant has already defaulted it.
func (j job) assistantTimeout() time.Duration {
	if j.timeout > 0 {
		return j.timeout
	}
	return profiles.DefaultAssistantTimeout.Std()
}
