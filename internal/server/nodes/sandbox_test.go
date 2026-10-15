package nodes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

// An admin's change reaches every connected node of that provider at once, and no other node.
func TestPushSandboxConfigReachesTheProvidersNodes(t *testing.T) {
	s := NewService(nil, nil, nil)
	configured := map[string]bool{"boat": true}
	s.SetSandboxResolver(func(_ context.Context, _, provider string) (map[string]string, []byte, bool, error) {
		if !configured[provider] {
			return nil, nil, false, nil
		}
		return map[string]string{"template": "ci-base"}, []byte("boat_key"), true, nil
	})
	boat := newSession("n1", []string{"backend=sandbox", "sandbox.provider=boat"}, store.NodeCapacity{MaxTasks: 2}, 0, nil, false)
	modal := newSession("n2", []string{"backend=sandbox", "sandbox.provider=modal"}, store.NodeCapacity{MaxTasks: 2}, 0, nil, false)
	docker := newSession("n3", []string{"linux/amd64"}, store.NodeCapacity{MaxTasks: 2}, 0, nil, false)
	for _, sess := range []*Session{boat, modal, docker} {
		s.reg.add(sess)
	}

	s.PushSandboxConfig(context.Background(), "boat")
	got := (<-boat.outbound()).GetSandboxConfig()
	require.NotNil(t, got)
	require.True(t, got.GetConfigured())
	require.Equal(t, "boat", got.GetProvider())
	require.Equal(t, "boat_key", string(got.GetSecret()))
	require.Equal(t, "ci-base", got.GetSettings()["template"])
	require.Empty(t, modal.outbound(), "another provider's nodes are told nothing")
	require.Empty(t, docker.outbound())

	configured["boat"] = false
	s.PushSandboxConfig(context.Background(), "boat")
	got = (<-boat.outbound()).GetSandboxConfig()
	require.False(t, got.GetConfigured(), "a deleted provider is pushed as unconfigured")
	require.Empty(t, got.GetSecret())

	require.Equal(t, "modal", sandboxProviderOf([]string{"x", "sandbox.provider=modal"}))
	require.Empty(t, sandboxProviderOf([]string{"backend=sandbox"}))
}
