package conductor

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

func repoAt(url string) profiles.Repo {
	return profiles.Repo{Name: "r", URL: url, DefaultBranch: "main"}
}

func TestGitScopeOf(t *testing.T) {
	t.Run("is the bare names of one owner's repositories", func(t *testing.T) {
		scope, err := gitScopeOf(profiles.Playbook{Repos: []profiles.Repo{
			repoAt("https://github.com/affiniti-finance/monorepo"),
			repoAt("https://github.com/affiniti-finance/podium.git"),
		}})
		require.NoError(t, err)
		assert.Equal(t, "affiniti-finance", scope.Owner)
		assert.Equal(t, []string{"monorepo", "podium"}, scope.Repos)
	})

	// One installation token covers one installation, and the container has one credential
	// helper. Two owners would need two tokens and there is nowhere to put the second.
	t.Run("refuses a playbook whose repos span two accounts", func(t *testing.T) {
		_, err := gitScopeOf(profiles.Playbook{Repos: []profiles.Repo{
			repoAt("https://github.com/affiniti-finance/monorepo"),
			repoAt("https://github.com/podium-ade/podium"),
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "two accounts")
	})

	t.Run("is empty for a playbook with no repositories, which is not an error", func(t *testing.T) {
		scope, err := gitScopeOf(profiles.Playbook{})
		require.NoError(t, err)
		assert.Empty(t, scope.Repos)
	})

	t.Run("refuses a host the app's token would be worthless on", func(t *testing.T) {
		_, err := gitScopeOf(profiles.Playbook{Repos: []profiles.Repo{
			repoAt("https://gitlab.com/affiniti/monorepo"),
		}})
		require.Error(t, err)
	})
}

// The capability is the whole of a task's authority, so these are the tests that matter:
// what it says must survive a round trip, and anything a container could do to it must
// invalidate it rather than change what it means.
func TestCapability(t *testing.T) {
	c := &Conductor{mintSecret: "conductor-bearer"}
	scope := GitScope{Owner: "affiniti-finance", Repos: []string{"monorepo"}}

	t.Run("round trips the turn and its scope", func(t *testing.T) {
		capability, err := c.mintCapability("turn_01abc", scope)
		require.NoError(t, err)

		turnID, got, err := c.parseCapability(capability)
		require.NoError(t, err)
		assert.Equal(t, "turn_01abc", turnID)
		assert.Equal(t, scope, got)
	})

	t.Run("carries no secret of its own: it is a name and a scope, signed", func(t *testing.T) {
		capability, err := c.mintCapability("turn_01abc", scope)
		require.NoError(t, err)
		assert.NotContains(t, capability, "conductor-bearer")
	})

	t.Run("refuses one signed by another conductor", func(t *testing.T) {
		capability, err := c.mintCapability("turn_01abc", scope)
		require.NoError(t, err)

		other := &Conductor{mintSecret: "someone-elses-bearer"}
		_, _, err = other.parseCapability(capability)
		require.ErrorIs(t, err, ErrBadCapability)
	})

	// The attack this design exists to stop: a turn rewriting its own scope to reach a
	// repository its playbook never named.
	t.Run("refuses a widened scope", func(t *testing.T) {
		capability, err := c.mintCapability("turn_01abc", scope)
		require.NoError(t, err)
		parts := strings.Split(capability, ".")
		require.Len(t, parts, 3)

		wider, err := json.Marshal(GitScope{Owner: "affiniti-finance", Repos: []string{"monorepo", "secrets"}})
		require.NoError(t, err)
		forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(wider) + "." + parts[2]

		_, _, err = c.parseCapability(forged)
		require.ErrorIs(t, err, ErrBadCapability)
	})

	t.Run("refuses a capability moved to another turn", func(t *testing.T) {
		capability, err := c.mintCapability("turn_01abc", scope)
		require.NoError(t, err)
		parts := strings.Split(capability, ".")
		forged := "turn_09zzz." + parts[1] + "." + parts[2]

		_, _, err = c.parseCapability(forged)
		require.ErrorIs(t, err, ErrBadCapability)
	})

	for _, tc := range []struct{ name, value string }{
		{"empty", ""},
		{"no signature", "turn_01abc.eyJvd25lciI6ImEifQ"},
		{"signature that is not hex", "turn_01abc.eyJvd25lciI6ImEifQ.zzzz"},
		{"too many parts", "a.b.c.d"},
	} {
		t.Run("refuses a malformed capability: "+tc.name, func(t *testing.T) {
			_, _, err := c.parseCapability(tc.value)
			require.ErrorIs(t, err, ErrBadCapability)
		})
	}
}

// A conductor with no App must say so rather than answering with an empty credential a
// caller would then try to push with.
func TestMintGitTokenWithoutAnApp(t *testing.T) {
	c := &Conductor{mintSecret: "conductor-bearer"}
	_, err := c.MintGitToken(t.Context(), "anything")
	require.ErrorIs(t, err, ErrNoGitApp)
}

func TestGitCapabilitySecretIsAValidSecretName(t *testing.T) {
	name := GitCapabilitySecret("turn_01k4v8xq2m3n4p5q6r7s8t9v0w")
	assert.True(t, strings.HasPrefix(name, profiles.GitCapabilityPrefix))
	// The control plane holds every secret name to this, and a name it refuses is a task
	// that cannot be admitted.
	assert.Regexp(t, `^[A-Za-z_][A-Za-z0-9_.-]*$`, name)
}
