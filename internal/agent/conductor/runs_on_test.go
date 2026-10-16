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
	}

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
