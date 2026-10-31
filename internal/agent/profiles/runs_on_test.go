package profiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

func TestTheAssistantMayRunOnTheHostAndAPlaybookMayNot(t *testing.T) {
	for _, ok := range []string{"", RunsOnHost, RunsOnNodes, "modal", "boat"} {
		require.NoError(t, ValidateAssistantRunsOn(ok), ok)
	}
	require.Error(t, ValidateAssistantRunsOn("modal, boat"))
	require.Error(t, ValidateAssistantRunsOn(" modal"))

	// A playbook is a container job: "host" would send it to a provider of that name.
	require.Error(t, ValidateRunsOn(RunsOnHost))
}

func TestSandboxBlockIsAPlacementWithNoOverrides(t *testing.T) {
	assert.Nil(t, SandboxBlock(""))
	assert.Nil(t, SandboxBlock(RunsOnNodes))
	assert.Equal(t, &spec.Sandbox{Provider: "modal"}, SandboxBlock("modal"))
}
