package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOrganization(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateOrganization(Organization{}))
	require.NoError(t, ValidateOrganization(Organization{OrganizationName: "Acme Corp", DeploymentName: "Acme AI"}))
	// The caps are characters, not bytes: a name in another script is not cut short.
	require.NoError(t, ValidateOrganization(Organization{DeploymentName: strings.Repeat("é", MaxDeploymentNameRunes)}))

	for _, bad := range []Organization{
		{DeploymentName: strings.Repeat("a", MaxDeploymentNameRunes+1)},
		{OrganizationName: strings.Repeat("a", MaxOrganizationNameRunes+1)},
		{DeploymentName: "Acme\nAI"},
		{OrganizationName: "<script>"},
		{DeploymentName: "\xff"},
	} {
		err := ValidateOrganization(bad)
		require.ErrorIs(t, err, ErrInvalidArgument, "%+v", bad)
	}
}
