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

// deletingProvider deletes anything and creates nothing; enough to see which calls a
// disabled provider still serves.
type deletingProvider struct {
	*sandbox.Swappable
	deleted []string
}

func (d *deletingProvider) Delete(_ context.Context, id string) error {
	d.deleted = append(d.deleted, id)
	return nil
}

// templateRecorder is an executor that only remembers the template it was given.
type templateRecorder struct {
	executor
	tmpl string
}

func (r *templateRecorder) SetTemplate(t string) { r.tmpl = t }

var last *deletingProvider

func init() {
	sandbox.Register("nodetest", func(cfg map[string]any) (sandbox.Provider, error) {
		if cfg["secret"] == "" {
			return nil, errors.New("no secret")
		}
		built = append(built, cfg)
		last = &deletingProvider{Swappable: sandbox.NewSwappable("nodetest")}
		return last, nil
	})
}

func TestTheControlPlaneConfiguresASandboxNode(t *testing.T) {
	swap := sandbox.NewSwappable("nodetest")
	exec := &templateRecorder{}
	n := &Node{
		cfg: Config{MaxTasks: 2, Sandbox: SandboxConfig{Template: "from-the-file"}}, tasks: map[string]*buffer{}, maxTasks: 2,
		sandbox: swap, exec: exec, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), wake: make(chan struct{}, 1),
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
	require.Equal(t, "ci-base", exec.tmpl, "the admin's template is the one image tasks start from")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "boat", Configured: true, Secret: []byte("x")})
	require.True(t, swap.Ready(), "another provider's config is not this node's")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest", Configured: true})
	require.False(t, swap.Ready(), "a config the provider refuses leaves the node unconfigured")

	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest", Configured: true, Secret: []byte("s")})
	require.True(t, swap.Ready())
	require.Equal(t, "from-the-file", exec.tmpl, "with no template saved, the node's own file has the say")
	n.applySandboxConfig(context.Background(), &podiumv1.SandboxConfig{Provider: "nodetest"})
	require.False(t, swap.Ready(), "a deleted provider stops the node taking work")
	require.EqualValues(t, 0, n.freeSlots())

	_, err := swap.Create(context.Background(), sandbox.CreateRequest{})
	_, retry := sandbox.IsRetryable(err)
	require.True(t, retry, "a task assigned in the gap fails retryably")
	require.NoError(t, swap.Delete(context.Background(), "bx_still_running"),
		"a sandbox made before the provider was removed can still be deleted")
	require.Equal(t, []string{"bx_still_running"}, last.deleted)
}

// reapRecorder is an executor that deletes what it is told to, failing for one id.
type reapRecorder struct {
	executor
	reaped chan string
}

func (r *reapRecorder) Reap(_ context.Context, id string) error {
	r.reaped <- id
	if id == "bx_stuck" {
		return errors.New("provider says no")
	}
	return nil
}

func (r *reapRecorder) AccountLimit() int32 { return 0 }

// A node deletes the sandboxes of its own provider it is asked to and queues each answer for
// the stream, the failures with their reason.
func TestASandboxNodeReapsForTheControlPlane(t *testing.T) {
	exec := &reapRecorder{reaped: make(chan string, 4)}
	n := &Node{
		cfg: Config{Sandbox: SandboxConfig{Provider: "boat"}}, exec: exec, runCtx: context.Background(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), wake: make(chan struct{}, 1),
	}
	n.reapSandbox(context.Background(), &podiumv1.ReapSandbox{Provider: "modal", SandboxId: "sb-other"})
	n.reapSandbox(context.Background(), &podiumv1.ReapSandbox{Provider: "boat", SandboxId: "bx_gone"})
	<-n.wake
	n.reapSandbox(context.Background(), &podiumv1.ReapSandbox{Provider: "boat", SandboxId: "bx_stuck"})
	<-n.wake
	require.Equal(t, "bx_gone", <-exec.reaped, "another provider's sandbox is not this node's to delete")
	require.Equal(t, "bx_stuck", <-exec.reaped)

	n.mu.Lock()
	defer n.mu.Unlock()
	require.Len(t, n.reaped, 2)
	require.Equal(t, "bx_gone", n.reaped[0].GetSandboxId())
	require.Empty(t, n.reaped[0].GetError())
	require.Equal(t, "provider says no", n.reaped[1].GetError())
	require.Empty(t, n.reaping)
}
