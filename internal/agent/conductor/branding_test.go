package conductor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisplayNameFollowsTheDeployment(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Podium", displayNameFor("Podium", branding{}))
	require.Equal(t, "Podium", displayNameFor("", branding{}))
	require.Equal(t, "Acme AI", displayNameFor("Podium", branding{Deployment: "Acme AI"}))
	require.Equal(t, "Acme AI", displayNameFor("", branding{Deployment: "Acme AI"}))
	// An assistant an admin named keeps its name.
	require.Equal(t, "Ada", displayNameFor("Ada", branding{Deployment: "Acme AI"}))
}

func TestBrandingGoesInFrontOfThePrompt(t *testing.T) {
	t.Parallel()
	require.Equal(t, "base", withBranding("base", branding{}))

	got := withBranding("base", branding{Deployment: "Acme AI", Organization: "Acme Corp"})
	require.Contains(t, got, "This deployment is called Acme AI.")
	require.Contains(t, got, "a Podium task, a Podium chat, Podium people), they mean Acme AI.")
	require.Contains(t, got, "You work for Acme Corp")
	require.True(t, len(got) > len("base") && got[len(got)-len("\n\nbase"):] == "\n\nbase", got)

	only := withBranding("", branding{Organization: "Acme Corp"})
	require.Equal(t, "You work for Acme Corp, and the people you talk to here are its members.", only)
}

func TestBrandingWithoutAServerIsEmpty(t *testing.T) {
	t.Parallel()
	c := &Conductor{}
	require.Equal(t, branding{}, c.branding(t.Context()))
	require.Equal(t, branding{}, c.cachedBranding())
}
