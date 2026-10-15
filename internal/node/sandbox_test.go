package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/sandbox"
)

// built records the configs the node built a provider from.
var built []map[string]any

func init() {
	sandbox.Register("nodetest", func(cfg map[string]any) (sandbox.Provider, error) {
		if cfg["secret"] == "" {
			return nil, errors.New("no secret")
		}
		built = append(built, cfg)
		return sandbox.NewSwappable("nodetest"), nil
	})
}

func TestTheControlPlaneConfiguresASandboxNode(t *testing.T) {
	swap := sandbox.NewSwappable("nodetest")
	n := &Node{
		cfg: Config{MaxTasks: 2}, tasks: map[string]*buffer{}, maxTasks: 2,
		sandbox: swap, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), wake: make(chan struct{}, 1),
	}
	require.EqualValues(t, 0, n.freeSlots(), "a managed node takes no work until it is configured")

	secret := []byte("boat_secret")
	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{
		Provider: "nodetest", Configured: true, Secret: secret,
		Settings: map[string]string{"template": "ci-base", "cpu": "0.5", "vm": "true"},
	})
	require.True(t, swap.Ready())
	require.EqualValues(t, 2, n.freeSlots())
	cfg := built[len(built)-1]
	require.Equal(t, "boat_secret", cfg["secret"])
	require.Equal(t, "ci-base", cfg["template"])
	require.Equal(t, 0.5, cfg["cpu"])
	require.Equal(t, true, cfg["vm"])
	require.Equal(t, make([]byte, len(secret)), secret, "the plaintext is zeroed once it is used")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "boat", Configured: true, Secret: []byte("x")})
	require.True(t, swap.Ready(), "another provider's config is not this node's")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest", Configured: true})
	require.False(t, swap.Ready(), "a config the provider refuses leaves the node unconfigured")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest", Configured: true, Secret: []byte("s")})
	require.True(t, swap.Ready())
	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest"})
	require.False(t, swap.Ready(), "a deleted provider stops the node taking work")
	require.EqualValues(t, 0, n.freeSlots())

	_, err := swap.Create(context.Background(), sandbox.CreateRequest{})
	_, retry := sandbox.IsRetryable(err)
	require.True(t, retry, "a task assigned in the gap fails retryably")
}
