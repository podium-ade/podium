package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/mcp"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

func registered(name string, enabled bool, version int32) mcp.Server {
	return mcp.Server{
		Name:               name,
		URL:                "https://mcp." + name + ".example/mcp",
		Enabled:            enabled,
		TokenSecretVersion: version,
	}
}

// The brief names the server and the variable; the task spec carries the secret that fills
// it. Neither carries the token, exactly as memory and the model credentials do not.
func TestATurnsMCPServersAreNamedInTheBriefAndCredentialedOnTheSpec(t *testing.T) {
	playbook := profiles.Playbook{MCPServers: []string{"linear", "wiki"}}
	c := skillsConductor(t, "", playbook)
	playbook = c.profiles.Current().Playbooks["coder"]
	servers := []mcp.Server{registered("linear", true, 3), registered("wiki", true, 0)}

	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, servers, choice)

	require.Len(t, b.Playbook.MCPServers, 2)
	assert.Equal(t, "linear", b.Playbook.MCPServers[0].Name)
	assert.Equal(t, "https://mcp.linear.example/mcp", b.Playbook.MCPServers[0].URL)
	assert.Equal(t, mcp.TokenEnv("linear"), b.Playbook.MCPServers[0].TokenEnv)
	// A server with no stored credential promises no variable: a brief naming one the
	// container does not have is a turn that fails on its harness config.
	assert.Empty(t, b.Playbook.MCPServers[1].TokenEnv)

	encoded, err := b.Encode()
	require.NoError(t, err)
	assert.NotContains(t, encoded, "token")

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, servers, choice)
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name:   mcp.TokenSecret("linear"),
		Target: spec.SecretTargetEnv,
		Key:    mcp.TokenEnv("linear"),
	})
	for _, ref := range sp.Secrets {
		assert.NotEqual(t, mcp.TokenSecret("wiki"), ref.Name)
	}
}

// A playbook that names none gets none: nothing in the brief, and no MCP secret on the spec.
func TestAPlaybookWithNoMCPServersGetsNone(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{})
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	b := c.brief(store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil, choice)
	assert.Nil(t, b.Playbook.MCPServers)

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice)
	for _, ref := range sp.Secrets {
		assert.NotContains(t, ref.Name, mcp.SecretPrefix)
	}
}

// The playbook's order is the brief's order, whatever order the registry came back in.
func TestTheResolvedServersFollowThePlaybooksOrder(t *testing.T) {
	playbook := profiles.Playbook{MCPServers: []string{"wiki", "linear"}}
	got, err := resolveMCPServers(playbook.MCPServers, []mcp.Server{
		registered("linear", true, 1), registered("wiki", true, 1),
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "wiki", got[0].Name)
	assert.Equal(t, "linear", got[1].Name)
}

// A name that does not resolve fails the turn rather than quietly narrowing it. A turn that
// ran with fewer tools than its playbook describes is the one outcome nobody can diagnose
// afterwards — the same rule a disabled skill follows.
func TestAnUnknownOrDisabledServerFailsTheTurn(t *testing.T) {
	rows := []mcp.Server{registered("linear", false, 1)}

	_, err := resolveMCPServers([]string{"linear"}, rows)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "turned off")

	_, err = resolveMCPServers([]string{"notion"}, rows)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no MCP server named "notion"`)
}
