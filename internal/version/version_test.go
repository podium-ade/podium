package version_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/version"
)

func TestStringDefaults(t *testing.T) {
	require.Equal(t, "dev (none)", version.String())
}
