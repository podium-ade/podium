package sandboxtest

// Bwrap is a sandbox provider whose sandboxes are bubblewrap namespaces on this machine: no
// Docker engine and no cloud account, so the whole sandbox path — the node's executor, its pool,
// podium-sandbox-agent, podium-runner and the event relay — runs where neither is available.
//
// Each sandbox is a directory and a user namespace of its own, as this machine's user. Its /podium,
// /workspace and /tmp are its own directories; the system is mounted read-only, as a provider's
// base image would be. Every Exec is a new namespace over the same directories, the way a
// provider's exec is a new process in the same sandbox. The network is the host's, so each
// sandbox's ports are moved to free ones on loopback and Expose says where.
//
// It is a test fixture: it fences nothing it should not have to, and an image request is
// accepted and ignored.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

// Bwrap is the provider. Dir is where sandboxes are made, Bin the bubblewrap binary.
type Bwrap struct {
	Dir string
	Bin string
	// Ro are extra host paths every sandbox can read, beyond the system.
	Ro []string

	mu        sync.Mutex
	sandboxes map[string]*bwrapSandbox
}

type bwrapSandbox struct {
	root  string
	ports map[int]int
	procs []*exec.Cmd
}

var bwrapSystem = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc"}

// Name implements sandbox.Provider.
func (b *Bwrap) Name() string { return "bwrap" }

// Capabilities implements sandbox.Provider. Not Root: the system is the host's, read-only, so
// the agent cannot add the account it would run a task as, and runs it as itself.
func (b *Bwrap) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{NativeImage: true}
}

// Create implements sandbox.Provider. It is idempotent on req.Key.
func (b *Bwrap) Create(_ context.Context, req sandbox.CreateRequest) (string, error) {
	sum := sha256.Sum256([]byte(req.Key))
	id := "bwrap-" + hex.EncodeToString(sum[:6])
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sandboxes == nil {
		b.sandboxes = map[string]*bwrapSandbox{}
	}
	if _, ok := b.sandboxes[id]; ok {
		return id, nil
	}
	root := filepath.Join(b.Dir, id)
	for _, d := range []string{"podium", "workspace", "tmp", "root"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return "", err
		}
	}
	sb := &bwrapSandbox{root: root, ports: map[int]int{}}
	for _, p := range req.Ports {
		free, err := freePort()
		if err != nil {
			return "", err
		}
		sb.ports[p] = free
	}
	b.sandboxes[id] = sb
	return id, nil
}

// WaitReady implements sandbox.Provider: a directory is ready at once.
func (b *Bwrap) WaitReady(context.Context, string) error { return nil }

// PutFile implements sandbox.Provider.
func (b *Bwrap) PutFile(_ context.Context, id, path string, r io.Reader, _ int64) error {
	sb, err := b.get(id)
	if err != nil {
		return err
	}
	host, err := sb.hostPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		return err
	}
	f, err := os.Create(host)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Exec implements sandbox.Provider.
func (b *Bwrap) Exec(ctx context.Context, id, cmd string, detached bool) (sandbox.ExecResult, error) {
	sb, err := b.get(id)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	// The sandbox's ports are loopback ports of this machine.
	for inside, outside := range sb.ports {
		cmd = strings.ReplaceAll(cmd, ":"+strconv.Itoa(inside)+" ", ":"+strconv.Itoa(outside)+" ")
	}
	argv := b.argv(sb, cmd)
	if detached {
		c := exec.Command(argv[0], argv[1:]...)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			return sandbox.ExecResult{}, err
		}
		b.mu.Lock()
		sb.procs = append(sb.procs, c)
		b.mu.Unlock()
		go func() { _ = c.Wait() }()
		return sandbox.ExecResult{}, nil
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	res := sandbox.ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

func (b *Bwrap) argv(sb *bwrapSandbox, cmd string) []string {
	bin := b.Bin
	if bin == "" {
		bin = "bwrap"
	}
	args := []string{bin, "--unshare-user", "--unshare-pid", "--unshare-ipc",
		"--proc", "/proc", "--dev", "/dev"}
	for _, p := range bwrapSystem {
		args = append(args, "--ro-bind-try", p, p)
	}
	args = append(args,
		"--bind", filepath.Join(sb.root, "podium"), "/podium",
		"--bind", filepath.Join(sb.root, "workspace"), "/workspace",
		"--bind", filepath.Join(sb.root, "tmp"), "/tmp",
		"--bind", filepath.Join(sb.root, "root"), "/root",
		// A provider's sandbox has no systemd to make scopes in; this one would find the host's.
		"--ro-bind", "/dev/null", "/usr/bin/systemd-run")
	// After the sandbox's own directories, so a path under the host's /tmp is still there.
	for _, p := range b.Ro {
		args = append(args, "--ro-bind-try", p, p)
	}
	args = append(args,
		"--setenv", "HOME", "/root",
		"--chdir", "/",
		"--", "/bin/sh", "-c", cmd)
	return args
}

// Expose implements sandbox.Provider.
func (b *Bwrap) Expose(_ context.Context, id string, port int) (sandbox.Endpoint, error) {
	sb, err := b.get(id)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	outside, ok := sb.ports[port]
	if !ok {
		return sandbox.Endpoint{}, &sandbox.Error{Op: "expose", Code: "not_published",
			Err: fmt.Errorf("port %d was not in CreateRequest.Ports", port)}
	}
	return sandbox.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(outside)}, nil
}

// ExtendTTL implements sandbox.Provider: these sandboxes live until deleted.
func (b *Bwrap) ExtendTTL(context.Context, string, time.Duration) error { return nil }

// Delete implements sandbox.Provider.
func (b *Bwrap) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	sb, ok := b.sandboxes[id]
	delete(b.sandboxes, id)
	b.mu.Unlock()
	if !ok {
		return nil
	}
	for _, c := range sb.procs {
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	}
	return os.RemoveAll(sb.root)
}

// Limits implements sandbox.Provider.
func (b *Bwrap) Limits(context.Context) (sandbox.Limits, error) {
	return sandbox.Limits{MaxSandboxes: -1, FreeSandboxes: -1, StartsLeft: -1}, nil
}

// IDs is every sandbox that exists right now.
func (b *Bwrap) IDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.sandboxes))
	for id := range b.sandboxes {
		out = append(out, id)
	}
	return out
}

// Count is how many sandboxes exist right now.
func (b *Bwrap) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sandboxes)
}

func (b *Bwrap) get(id string) (*bwrapSandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	if !ok {
		return nil, &sandbox.Error{Op: "get", Code: "not_found", Err: fmt.Errorf("no sandbox %s", id)}
	}
	return sb, nil
}

// hostPath is where a path inside the sandbox is on this machine.
func (sb *bwrapSandbox) hostPath(path string) (string, error) {
	for _, d := range []string{"/podium", "/workspace", "/tmp", "/root"} {
		if path == d || strings.HasPrefix(path, d+"/") {
			return filepath.Join(sb.root, strings.TrimPrefix(d, "/"), strings.TrimPrefix(path, d)), nil
		}
	}
	return "", fmt.Errorf("sandboxtest: %s is outside every writable directory of a bwrap sandbox", path)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
