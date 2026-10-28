package modal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modal-labs/modal-client/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/podium-ade/podium/internal/sandbox"
)

func TestNewRejectsBadConfig(t *testing.T) {
	for _, c := range []Config{
		{Runtime: "firecracker"},
		{CPU: -1},
		{TokenID: "ak-x"},
		{TokenSecretFile: "/nope"},
		{TokenID: "ak-x", TokenSecretFile: filepath.Join(t.TempDir(), "missing")},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
	if _, err := sandbox.New("modal", map[string]any{"runtime": 3}); err == nil {
		t.Error("a non-string runtime parsed")
	}
}

func TestCreateParams(t *testing.T) {
	p := &Provider{cfg: Config{CPU: 0.5, MemoryMB: 1024, Runtime: "vm"}}
	got, err := p.createParams(sandbox.CreateRequest{Key: "lease-1", TTL: 90*time.Minute + 500*time.Millisecond, Ports: []int{7799}}, p.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU != 0.5 || got.MemoryMiB != 1024 || got.Runtime != sdk.SandboxRuntimeVM || got.Timeout != MaxTTL ||
		got.Name != sandboxName("lease-1") || got.Tags["podium.key"] != "lease-1" || len(got.EncryptedPorts) != 0 ||
		!strings.Contains(got.Command[2], "+ 5400))") || !strings.HasPrefix(got.Command[2], dockerd) {
		t.Errorf("%+v", got)
	}
	p.cfg.Runtime = "gvisor"
	got, _ = p.createParams(sandbox.CreateRequest{Key: "k", CPU: 2, MemoryMB: 4096}, p.cfg)
	if got.CPU != 2 || got.MemoryMiB != 4096 || !strings.Contains(got.Command[2], "+ 300))") || strings.Contains(got.Command[2], "dockerd") {
		t.Errorf("%+v", got)
	}
	for _, ttl := range []time.Duration{MaxTTL + time.Second, time.Millisecond} {
		_, err := p.createParams(sandbox.CreateRequest{Key: "k", TTL: ttl}, p.cfg)
		var pe *sandbox.Error
		if !errors.As(err, &pe) || pe.Code != "invalid" || pe.Retryable {
			t.Errorf("ttl %v: %v", ttl, err)
		}
	}
}

// A task may run under the vm runtime on a gvisor provider, which gives it Docker, and in an
// environment of its own.
func TestATaskOverridesRuntimeAndEnvironment(t *testing.T) {
	p := &Provider{cfg: Config{Runtime: "gvisor", Image: DefaultImage, Environment: "main"}}
	if p.Capabilities().Docker {
		t.Fatal("gvisor has no Docker")
	}
	caps, err := p.Override(map[string]string{"runtime": "vm", "environment": "staging"})
	if err != nil || !caps.Docker || !caps.Tmpfs {
		t.Fatalf("%+v, %v", caps, err)
	}
	for _, bad := range []map[string]string{{"runtime": "docker"}, {"app": "other"}} {
		if _, err := p.Override(bad); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	cfg := p.cfg.with(map[string]string{"runtime": "vm", "environment": "staging"})
	if cfg.Image != DefaultVMImage || cfg.Environment != "staging" || p.cfg.Runtime != "gvisor" {
		t.Errorf("%+v, provider's own %+v", cfg, p.cfg)
	}
	got, _ := p.createParams(sandbox.CreateRequest{Key: "k"}, cfg)
	if got.Runtime != sdk.SandboxRuntimeVM || !strings.HasPrefix(got.Command[2], dockerd) {
		t.Errorf("%+v", got)
	}
	custom := Config{Runtime: "gvisor", Image: "my/image"}.with(map[string]string{"runtime": "vm"})
	if custom.Image != "my/image" {
		t.Errorf("an operator's own image is kept: %q", custom.Image)
	}
}

func TestSandboxNameIsAValidModalName(t *testing.T) {
	n := sandboxName(strings.Repeat("lease/with spaces/", 10))
	if len(n) > 63 || strings.ContainsAny(n, "/ ") || n != sandboxName(strings.Repeat("lease/with spaces/", 10)) || n == sandboxName("other") {
		t.Fatal(n)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		err       error
		code      string
		retryable bool
	}{
		{sdk.NotFoundError{Exception: "x"}, "not_found", false},
		{fmt.Errorf("wrapped: %w", sdk.ResourceExhaustedError{}), "resource_exhausted", true},
		{sdk.InternalFailure{}, "internal", true},
		{sdk.InvalidError{}, "invalid", false},
		{sdk.SandboxTimeoutError{}, "timeout", true},
		{status.Error(codes.Unavailable, "down"), "unavailable", true},
		{status.Error(codes.ResourceExhausted, "slow down"), "resource_exhausted", true},
		{status.Error(codes.NotFound, "Modal Sandbox with container ID ta-1 not found"), "not_found", false},
		{status.Error(codes.FailedPrecondition, "Sandbox has already finished"), "invalid", false},
		{status.Error(codes.Unauthenticated, "bad token"), "unauthorized", false},
		{errors.New("boom"), "modal", false},
	} {
		var pe *sandbox.Error
		if err := toError("op", tc.err); !errors.As(err, &pe) || pe.Code != tc.code || pe.Retryable != tc.retryable {
			t.Errorf("%v: got %+v", tc.err, err)
		}
	}
	if toError("op", nil) != nil {
		t.Error("nil is an error")
	}
	if err := toError("op", fmt.Errorf("x: %w", context.Canceled)); !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}

// The TTL scripts run in the sandbox's sh; here they run in the local one.
func TestTTLScripts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ttl")
	ep := entrypoint(file, time.Second, true)
	start := time.Now()
	if out, err := exec.Command(ep[0], ep[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("entrypoint outlived its ttl by %v", took)
	}

	now := time.Now().Unix()
	write := func(created int64) {
		if err := os.WriteFile(file, []byte(fmt.Sprintf("%d %d\n", created, now)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(d time.Duration) int {
		err := exec.Command("sh", "-c", extendScript(file, d)).Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	write(now)
	if code := run(time.Hour); code != 0 {
		t.Fatalf("extend: exit %d", code)
	}
	b, _ := os.ReadFile(file)
	f := strings.Fields(string(b))
	d, _ := strconv.ParseInt(f[1], 10, 64)
	if f[0] != strconv.FormatInt(now, 10) || d < now+3599 || d > now+3602 {
		t.Fatalf("ttl file: %q", b)
	}
	write(now - int64(MaxTTL.Seconds()) + 60)
	if code := run(time.Hour); code != errMaxTTL {
		t.Fatalf("extend past the max: exit %d", code)
	}
}
