//go:build integration

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrganizationNamesDefaultToUnsetAndReplaceWhole(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	got, err := s.GetOrganization(ctx)
	require.NoError(t, err)
	require.Equal(t, Organization{}, got)

	set, err := s.SetOrganization(ctx, Organization{OrganizationName: "  Acme Corp ", DeploymentName: "Acme AI", UpdatedBy: "ada"})
	require.NoError(t, err)
	require.Equal(t, "Acme Corp", set.OrganizationName)
	require.Equal(t, "Acme AI", set.DeploymentName)
	require.Equal(t, "ada", set.UpdatedBy)
	require.False(t, set.UpdatedAt.IsZero())

	// A save replaces both: clearing the deployment name goes back to Podium.
	_, err = s.SetOrganization(ctx, Organization{OrganizationName: "Acme Corp", UpdatedBy: "bob"})
	require.NoError(t, err)
	got, err = s.GetOrganization(ctx)
	require.NoError(t, err)
	require.Equal(t, "Acme Corp", got.OrganizationName)
	require.Empty(t, got.DeploymentName)
	require.Equal(t, "bob", got.UpdatedBy)

	_, err = s.SetOrganization(ctx, Organization{DeploymentName: strings.Repeat("x", MaxDeploymentNameRunes+1)})
	require.ErrorIs(t, err, ErrInvalidArgument)
}
