package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// `podium node label worker-3` with no --add and no --remove is a typo, not a request to
// leave the node alone: it is refused before anything reaches the server.
func TestNodeLabelNeedsSomethingToChange(t *testing.T) {
	e := &env{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cmd := newNodeLabelCommand(e)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"worker-3"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "at least one --add or --remove")
	require.Equal(t, ExitUsage, err.(*ExitError).Code)
}
