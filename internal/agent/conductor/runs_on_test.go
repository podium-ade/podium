package conductor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/pkg/spec"
)

// A turn that runs on a provider asks for a sandbox there, with the playbook's overrides
// for that provider and no other; one that runs on the fleet's own nodes asks for none.
func TestATurnRunsWhereItsPlaybookSays(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{
		Image: "podium-agent-runtime:dev",
		Sandbox: &profiles.PlaybookSandbox{
			Boat:  &spec.BoatSandbox{Type: "large", Org: "acme"},
			Modal: &spec.ModalSandbox{Runtime: "vm"},
		},
	})
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	playbook.RunsOn = "boat"
	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
	require.NoError(t, sp.Validate())
	sp.ApplyDefaults()
	assert.Equal(t, &spec.Sandbox{Provider: "boat", Boat: &spec.BoatSandbox{Type: "large", Org: "acme"}}, sp.Sandbox)
	assert.Subset(t, sp.Labels, []string{spec.LabelSandbox, spec.LabelSandboxProvider + "boat"})
	sp.Sandbox.Boat.Type = "small"
	assert.Equal(t, "large", c.profiles.Current().Playbooks["coder"].Sandbox.Boat.Type,
		"a turn's spec must not share its sandbox with the loaded playbook")

	for _, runsOn := range []string{"", profiles.RunsOnNodes} {
		playbook.RunsOn = runsOn
		sp = c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
		assert.Nil(t, sp.Sandbox, "runs_on %q", runsOn)
		assert.Empty(t, sp.Command, "a container takes its command from the image")
	}

	// A Modal image is one Modal holds, so the turn goes to Modal whatever runs_on says.
	playbook.Image = spec.ModalImagePrefix + "podium-agent-runtime-dev:0.7.3"
	sp = c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
	require.NoError(t, sp.Validate())
	require.NotNil(t, sp.Sandbox)
	assert.Equal(t, "modal", sp.Sandbox.Provider)
	assert.Equal(t, runtimeCommand, sp.Command)

	c.store = nil
	playbook.RunsOn = "modal"
	assert.Equal(t, "modal", c.placed(context.Background(), playbook).RunsOn, "a playbook's own runs_on wins")
}

func TestPlaybookRunsOnIsValidated(t *testing.T) {
	for _, bad := range []string{"boat,modal", " boat", "sandbox.provider=boat"} {
		assert.Error(t, profiles.ValidateRunsOn(bad), bad)
	}
	for _, ok := range []string{"", "nodes", "boat", "modal"} {
		assert.NoError(t, profiles.ValidateRunsOn(ok), ok)
	}
}

// A playbook whose image Modal holds runs in a Modal sandbox when it does not say where, so the
// brief's surface URL is the sandbox's and the playbook's Modal settings reach the spec — not
// the fleet default's, which may be the nodes or another provider.
func TestAModalImageIsPlacedOnModal(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{
		Image:   "modal:podium-agent-runtime-dev:0.7.3",
		Sandbox: &profiles.PlaybookSandbox{Modal: &spec.ModalSandbox{Runtime: "vm"}},
	})
	c.gitTaskURL = "http://host.docker.internal:8090"
	c.sandboxTaskURL = "https://podium.example.com/turn-surface"
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	placed := c.placed(context.Background(), playbook)
	assert.Equal(t, "modal", placed.RunsOn)
	assert.Equal(t, "https://podium.example.com/turn-surface", c.turnSurfaceURL(placed.RunsOn))

	sp := c.taskSpec(chatSource{}, placed, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
	require.NoError(t, sp.Validate())
	sp.ApplyDefaults()
	assert.Equal(t, &spec.Sandbox{Provider: "modal", Modal: &spec.ModalSandbox{Runtime: "vm"}}, sp.Sandbox)

	playbook.RunsOn = profiles.RunsOnNodes
	assert.Equal(t, profiles.RunsOnNodes, c.placed(context.Background(), playbook).RunsOn,
		"a playbook that names where it runs is taken at its word")
}

// A turn on a provider reaches memory at the sandbox address when one is set; every other
// turn, and every turn when none is, keeps the address the fleet's nodes use.
func TestASandboxTurnIsBriefedWithMemoryItCanReach(t *testing.T) {
	nodes := &BriefMemory{MCPURL: "http://host.docker.internal:8888/mcp/podium", APIKeyEnv: "K"}
	c := &Conductor{memory: nodes}
	assert.Same(t, nodes, c.memoryFor("modal"), "no sandbox address: unchanged")

	c.memorySandboxMCPURL = "https://memory.example.com/mcp/podium"
	got := c.memoryFor("modal")
	assert.Equal(t, &BriefMemory{MCPURL: "https://memory.example.com/mcp/podium", APIKeyEnv: "K"}, got)
	assert.Equal(t, "http://host.docker.internal:8888/mcp/podium", nodes.MCPURL, "the shared block is not changed")
	for _, runsOn := range []string{"", profiles.RunsOnNodes, profiles.RunsOnHost} {
		assert.Same(t, nodes, c.memoryFor(runsOn), runsOn)
	}
	assert.Nil(t, (&Conductor{memorySandboxMCPURL: "https://x"}).memoryFor("modal"), "no memory stays no memory")
}

// A docker and browser playbook on Modal asks for what a node would give it, beside a turn
// that runs in its Modal image: a privileged daemon sharing the workspace, a browser, and the
// vm runtime that can run them.
func TestADockerAndBrowserPlaybookOnModalKeepsItsSidecars(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{
		Image:     "modal:agent-docker:1",
		Docker:    true,
		Browser:   true,
		Resources: spec.Resources{CPU: 8, MemoryMB: 16384},
		Sandbox:   &profiles.PlaybookSandbox{Modal: &spec.ModalSandbox{Runtime: "vm"}},
	})
	playbook := c.placed(context.Background(), c.profiles.Current().Playbooks["coder"])
	require.Equal(t, "modal", playbook.RunsOn, "a modal: image places itself")
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
	require.NoError(t, sp.Validate())

	assert.Equal(t, &spec.Sandbox{Provider: "modal", Modal: &spec.ModalSandbox{Runtime: "vm"}}, sp.Sandbox)
	assert.Equal(t, runtimeCommand, sp.Command)
	assert.Equal(t, dockerSidecarHost, sp.Env[profiles.DockerHostEnv])
	dind := sp.Sidecars[dockerSidecarName]
	assert.True(t, dind.Privileged && dind.ShareWorkspace, "the daemon is privileged and shares /workspace")
	assert.Contains(t, sp.Sidecars, browserSidecarName)
	assert.Subset(t, sp.Labels, []string{spec.LabelSandbox, spec.LabelSandboxProvider + "modal"})
}
