package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkewWarning(t *testing.T) {
	require.Empty(t, skewWarning("v0.3.1", "v0.3.1"))
	require.Empty(t, skewWarning("dev", "dev"))

	warning := skewWarning("v0.3.1", "v0.4.0")
	require.Contains(t, warning, "client v0.3.1")
	require.Contains(t, warning, "control plane v0.4.0")

	// A stamped client against an unstamped server is exactly the case an operator
	// needs told about: one of the two is not the release they think it is.
	require.NotEmpty(t, skewWarning("v0.3.1", "dev"))
	require.NotEmpty(t, skewWarning("dev", "v0.3.1"))
}
