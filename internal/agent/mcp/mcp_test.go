package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

// The two names the runtime wires up itself cannot be registered. The harness config is one
// map keyed by name, so a second `memory` would replace the shared memory a turn cannot opt
// out of — which is why this is refused at the name and not merely documented.
func TestTheRuntimesOwnServerNamesAreReserved(t *testing.T) {
	for _, name := range []string{ReservedMemory, ReservedBrowser, ReservedDelegate} {
		err := ValidateName(name)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "reserved")
	}
}

func TestAServerNameIsWhatAPlaybookCanType(t *testing.T) {
	for _, name := range []string{"linear", "notion", "my-wiki", "s3"} {
		assert.NoError(t, ValidateName(name), name)
	}
	for _, name := range []string{"", "Linear", "1linear", "linear_wiki", "linear.app", "-x",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		assert.Error(t, ValidateName(name), name)
	}
}

// The env var is derived from the name and nothing else, and NameRE has already refused
// underscores — so no two names can collide on one variable.
func TestACredentialsSecretAndVariableComeFromTheName(t *testing.T) {
	assert.Equal(t, "podium.agent.mcp.linear_token", TokenSecret("linear"))
	assert.Equal(t, EnvPrefix+"LINEAR_TOKEN", TokenEnv("linear"))
	assert.Equal(t, EnvPrefix+"MY_WIKI_TOKEN", TokenEnv("my-wiki"))
	assert.NotEqual(t, TokenEnv("my-wiki"), TokenEnv("mywiki"))
}

// A secret name the control plane would refuse is a task that cannot start — a failure that
// happens per turn, long after the registration was accepted. So the shape of the name this
// package derives is pinned against the spec's own rule rather than merely read once.
func TestTheDerivedSecretNameIsOneATaskSpecAccepts(t *testing.T) {
	for _, name := range []string{"linear", "my-wiki", "a", strings.Repeat("x", 32)} {
		assert.Regexp(t, spec.SecretNameRE, TokenSecret(name), name)
	}
}

func TestARegistrationHasToNameAnHTTPEndpoint(t *testing.T) {
	ok := Server{Name: "linear", URL: "https://mcp.linear.app/mcp"}
	require.NoError(t, ok.Validate())

	// A loopback or tailnet address over plain http is a real thing to register, so the
	// scheme is checked and the transport is not.
	assert.NoError(t, Server{Name: "wiki", URL: "http://127.0.0.1:9000/mcp"}.Validate())

	for _, raw := range []string{"", "mcp.linear.app/mcp", "ftp://x/mcp", "https://", "not a url"} {
		assert.Error(t, Server{Name: "linear", URL: raw}.Validate(), raw)
	}
}

func TestADescriptionIsBounded(t *testing.T) {
	long := make([]byte, MaxDescriptionLen+1)
	for i := range long {
		long[i] = 'x'
	}
	err := Server{Name: "linear", URL: "https://x/mcp", Description: string(long)}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description")
}
