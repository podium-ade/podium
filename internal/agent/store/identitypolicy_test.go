package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnEmptyIdentityPolicyIsTheDefault(t *testing.T) {
	require.NoError(t, IdentityPolicy{}.Validate())
	assert.Equal(t, DefaultIdentityPolicy, IdentityPolicy{}.withDefaults())
	assert.Equal(t, IdentityPersonOnly, IdentityPolicy{GitHubRead: IdentityPersonOnly}.withDefaults().GitHubRead)
}

func TestAnIdentityPolicyOffersEachCapabilityOnlyItsChoices(t *testing.T) {
	for name, p := range map[string]IdentityPolicy{
		"linear":            {Linear: "sometimes"},
		"github read app":   {GitHubRead: IdentityApp},
		"model key person":  {ModelKey: IdentityPersonOnly},
		"shared key theirs": {SharedModelKey: IdentityPersonThenApp},
		"slack fixed":       {SlackOutsiders: OutsidersFixed},
		"linear review":     {LinearOutsiders: "review"},
	} {
		err := p.Validate()
		require.ErrorIs(t, err, ErrInvalidIdentityPolicy, name)
	}
	require.NoError(t, IdentityPolicy{
		Linear: IdentityApp, GitHubWrite: IdentityPersonOnly, GitHubRead: IdentityPersonThenApp,
		ModelKey: IdentityApp, SharedModelKey: IdentityPlanThenApp, SlackOutsiders: OutsidersAnswer, LinearOutsiders: OutsidersFixed,
	}.Validate())
}
