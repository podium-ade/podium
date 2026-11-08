package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// A system routine reads only the repositories an admin listed on it.
func TestASystemRoutineReadsOnlyItsRepositories(t *testing.T) {
	r := store.Routine{Scope: store.RoutineScopeSystem, Repos: []string{"acme/api", "Acme/Web"}}

	owner, name, err := routineRepo(r, "acme/api")
	require.NoError(t, err)
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "api", name)

	owner, name, err = routineRepo(r, "acme/web")
	require.NoError(t, err, "GitHub names are not case-sensitive")
	assert.Equal(t, "Acme", owner)
	assert.Equal(t, "Web", name)

	_, _, err = routineRepo(r, "acme/billing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not one of this routine's repositories")

	_, _, err = routineRepo(store.Routine{Scope: store.RoutineScopeSystem}, "acme/api")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lists no repositories")
}
