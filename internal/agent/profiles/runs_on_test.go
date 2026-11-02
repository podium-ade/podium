package profiles

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A playbook is a container job: "host" would send it to a provider of that name.
func TestAPlaybookMayNotRunOnTheHost(t *testing.T) {
	for _, ok := range []string{"", RunsOnNodes, "modal", "boat"} {
		require.NoError(t, ValidateRunsOn(ok), ok)
	}
	require.Error(t, ValidateRunsOn(RunsOnHost))
}
