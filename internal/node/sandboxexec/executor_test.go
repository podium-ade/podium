package sandboxexec

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/pkg/spec"
)

// stubProvider makes sandboxes that never become ready.
type stubProvider struct {
	sandbox.Provider
	deleted []string
	missing bool
	limits  chan sandbox.Limits
}

func (p *stubProvider) Name() string { return "stub" }
func (p *stubProvider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{NativeImage: true}
}
func (p *stubProvider) Create(context.Context, sandbox.CreateRequest) (string, error) {
	return "sb-1", nil
}
func (p *stubProvider) WaitReady(context.Context, string) error { return errors.New("never ready") }
func (p *stubProvider) Delete(_ context.Context, id string) error {
	p.deleted = append(p.deleted, id)
	if p.missing {
		return &sandbox.Error{Op: "delete", Code: "not_found"}
	}
	return nil
}
func (p *stubProvider) Limits(context.Context) (sandbox.Limits, error) { return <-p.limits, nil }

// The node says which sandbox it made before anything else can fail, so the control plane
// can always find it again.
func TestRunReportsItsSandboxFirst(t *testing.T) {
	p := &stubProvider{}
	e, err := New(Options{Provider: p, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	sp := spec.TaskSpec{Image: "alpine:3", Command: []string{"true"}}
	sp.ApplyDefaults()
	ch := make(chan docker.Event, 16)
	_, err = e.Run(context.Background(), docker.Request{TaskID: "t1", LeaseID: "l1", Spec: sp}, ch)
	require.Error(t, err)
	close(ch)
	var kinds []string
	var handle docker.SandboxPayload
	for ev := range ch {
		kinds = append(kinds, ev.Kind)
		if p, ok := ev.Payload.(docker.SandboxPayload); ok {
			handle = p
		}
	}
	require.Equal(t, []string{docker.KindProvisioning, docker.KindSandbox, docker.KindError}, kinds)
	require.Equal(t, docker.SandboxPayload{Provider: "stub", ID: "sb-1"}, handle)
	require.Equal(t, []string{"sb-1"}, p.deleted, "a failed run deletes its sandbox")
}

func TestReapTreatsAMissingSandboxAsDeleted(t *testing.T) {
	p := &stubProvider{missing: true}
	e, err := New(Options{Provider: p, DataDir: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, e.Reap(context.Background(), "sb-gone"))
}

// The account limit is read in the background and answered from the last read.
func TestAccountLimitIsReadInTheBackground(t *testing.T) {
	p := &stubProvider{limits: make(chan sandbox.Limits, 1)}
	e, err := New(Options{Provider: p, DataDir: t.TempDir()})
	require.NoError(t, err)
	require.Zero(t, e.AccountLimit(), "unknown until the first read lands")
	p.limits <- sandbox.Limits{MaxSandboxes: 2}
	require.Eventually(t, func() bool { return e.AccountLimit() == 2 }, 5*time.Second, 10*time.Millisecond)
}
