//go:build e2e

// Package e2e_test drives the whole MVP-0 loop the way an operator does: a real Postgres,
// a real podium-server, a real podium-node against the host's Docker, and the podium CLI
// as a subprocess. Nothing here reaches inside the binaries; everything is asserted
// through the CLI, the API and the database.
//
// The tests share one Docker engine and one node identity per test, so they must not run
// in parallel: internal/node adopts every container on the engine labelled podium.task,
// and two nodes would fight over them.
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alvaroibarguen/podium/internal/node/docker"
	"github.com/alvaroibarguen/podium/internal/server"
)

// devToken is the shared bearer token every process in this package presents. It is a test
// fixture, not a secret.
const devToken = "devtoken-e2e"

// nodeReadyTimeout is how long a freshly started node gets to enroll, connect and show up
// as online. Docker Desktop is slow to answer the first Info call of a process.
const nodeReadyTimeout = 60 * time.Second

var (
	adminURL string
	binDir   string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "podium-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create bin dir: %v\n", err)
		os.Exit(1)
	}
	binDir = dir
	if err := buildBinaries(ctx, dir); err != nil {
		fmt.Fprintf(os.Stderr, "build binaries: %v\n", err)
		os.Exit(1)
	}

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("podium"),
		postgres.WithUsername("podium"),
		postgres.WithPassword("podium"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(ctr)
		os.Exit(1)
	}

	code := m.Run()

	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintf(os.Stderr, "terminate postgres: %v\n", err)
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// buildBinaries compiles the two binaries the tests drive, so a stale bin/ can never make
// a green run mean nothing.
func buildBinaries(ctx context.Context, dir string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	for _, name := range []string{"podium", "podium-node"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, name), "./cmd/"+name)
		cmd.Dir = root
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
	}
	return nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the test directory")
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// harness is one control plane on a fixed loopback port, with its own database, plus the
// CLI configured to talk to it.
type harness struct {
	t           *testing.T
	addr        string
	databaseURL string

	mu  sync.Mutex
	srv *server.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, addr: freeLoopbackAddr(t), databaseURL: newDatabase(t)}
	h.startServer()
	t.Cleanup(h.stopServer)
	return h
}

func (h *harness) url() string { return "http://" + h.addr }

// startServer binds the harness's fixed address. The address is fixed rather than
// ephemeral because the node and the CLI have to find the server again after a restart.
func (h *harness) startServer() {
	h.t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var srv *server.Server
	var err error
	// The port was free a moment ago; a previous incarnation's listener may still be in
	// the kernel's hands for a beat.
	deadline := time.Now().Add(20 * time.Second)
	for {
		srv, err = server.New(context.Background(), server.Config{
			DatabaseURL: h.databaseURL,
			Transport:   server.TransportDev,
			DevListen:   h.addr,
			DevToken:    devToken,
		}, logger)
		if err == nil {
			err = srv.Start(context.Background())
			if err == nil {
				break
			}
			srv.Close()
		}
		if time.Now().After(deadline) {
			require.NoError(h.t, err, "starting podium-server on %s", h.addr)
		}
		time.Sleep(100 * time.Millisecond)
	}

	h.mu.Lock()
	h.srv = srv
	h.mu.Unlock()
}

func (h *harness) stopServer() {
	h.mu.Lock()
	srv := h.srv
	h.srv = nil
	h.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), server.ShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(ctx)
	srv.Close()
}

// podium runs the CLI as a subprocess and returns its exit status, stdout and stderr.
func (h *harness) podium(args ...string) (int, string, string) {
	h.t.Helper()
	code, stdout, stderr, err := runCLI(h.t, h.url(), 3*time.Minute, args...)
	require.NoError(h.t, err)
	return code, stdout, stderr
}

// podiumOK is podium for commands that must succeed.
func (h *harness) podiumOK(args ...string) string {
	h.t.Helper()
	code, stdout, stderr := h.podium(args...)
	require.Equal(h.t, 0, code, "podium %s failed\nstdout:\n%s\nstderr:\n%s",
		strings.Join(args, " "), stdout, stderr)
	return stdout
}

func runCLI(t *testing.T, serverURL string, timeout time.Duration, args ...string) (int, string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := append([]string{"--server", serverURL, "--token", devToken}, args...)
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "podium"), full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String(), nil
	case errors.As(err, &exit):
		return exit.ExitCode(), stdout.String(), stderr.String(), nil
	default:
		return -1, stdout.String(), stderr.String(), err
	}
}

// enrollToken mints a token through the CLI, which is also how the acceptance script does
// it: stdout carries the token and nothing else.
func (h *harness) enrollToken(labels ...string) string {
	h.t.Helper()
	args := []string{"node", "enroll-token"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out := strings.TrimSpace(h.podiumOK(args...))
	require.NotEmpty(h.t, out)
	require.NotContains(h.t, out, "\n", "the token is the only thing on stdout")
	return out
}

// ---------------------------------------------------------------------------
// node subprocess
// ---------------------------------------------------------------------------

// nodeProc is a podium-node subprocess and the data dir it keeps its identity in.
type nodeProc struct {
	t       *testing.T
	h       *harness
	dataDir string
	metrics string
	labels  []string

	cmd *exec.Cmd
	log *bytes.Buffer
	mu  sync.Mutex
}

// startNode enrolls and starts a node. Restarting it later reuses the data dir, so it
// keeps its identity and adopts whatever containers it left behind.
func startNode(t *testing.T, h *harness, labels ...string) *nodeProc {
	t.Helper()
	n := &nodeProc{
		t:       t,
		h:       h,
		dataDir: t.TempDir(),
		metrics: freeLoopbackAddr(t),
		labels:  labels,
	}
	n.start(h.enrollToken(labels...))
	t.Cleanup(func() {
		n.stop()
		cleanupEngine(t)
	})
	n.awaitOnline()
	return n
}

func (n *nodeProc) start(enrollToken string) {
	n.t.Helper()
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "podium-node")) //nolint:gosec // the path is this test's own build output
	cmd.Env = append(os.Environ(),
		"PODIUM_NODE_SERVER="+n.h.url(),
		"PODIUM_NODE_TRANSPORT=dev",
		"PODIUM_NODE_DEV_TOKEN="+devToken,
		"PODIUM_NODE_ENROLL_TOKEN="+enrollToken,
		"PODIUM_NODE_DATA_DIR="+n.dataDir,
		"PODIUM_NODE_METRICS_LISTEN="+n.metrics,
		"PODIUM_NODE_LABELS="+strings.Join(n.labels, ","),
	)
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	require.NoError(n.t, cmd.Start())

	n.mu.Lock()
	n.cmd = cmd
	n.log = buf
	n.mu.Unlock()
}

// restart brings the node back with the same data dir and no enrollment token, which is
// what a real restart looks like once identity.json exists.
func (n *nodeProc) restart() {
	n.t.Helper()
	n.stop()
	n.start("")
	n.awaitOnline()
}

// stop sends SIGTERM, which must leave running containers alone.
func (n *nodeProc) stop() {
	n.mu.Lock()
	cmd := n.cmd
	n.cmd = nil
	n.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
	}
}

func (n *nodeProc) logs() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.log == nil {
		return ""
	}
	return n.log.String()
}

func (n *nodeProc) awaitOnline() {
	n.t.Helper()
	waitFor(n.t, nodeReadyTimeout, "the node to report online", func() bool {
		code, stdout, _, err := runCLI(n.t, n.h.url(), 30*time.Second, "nodes")
		if err != nil || code != 0 {
			return false
		}
		return strings.Contains(stdout, "online")
	}, func() string { return "node log:\n" + n.logs() })
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// freeLoopbackAddr picks a port the kernel says is free right now. The tests never assume
// 8080 or 5432 are available.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_e2e_%d", dbSeq.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool, context ...func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			extra := ""
			for _, c := range context {
				extra += "\n" + c()
			}
			t.Fatalf("timed out after %s waiting for %s%s", timeout, what, extra)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// taskIDFrom pulls the task ID out of `podium run`'s first progress line.
func taskIDFrom(t *testing.T, stderr string) string {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(stderr))
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		for i, f := range fields {
			if f == "task" && i+1 < len(fields) && strings.HasPrefix(fields[i+1], "task_") {
				return fields[i+1]
			}
		}
	}
	t.Fatalf("no task ID in:\n%s", stderr)
	return ""
}

// ---------------------------------------------------------------------------
// docker assertions
// ---------------------------------------------------------------------------

func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// requireNoPodiumResources is the "nothing leaked" half of the acceptance criteria.
func requireNoPodiumResources(t *testing.T) {
	t.Helper()
	cli := dockerClient(t)
	ctx := context.Background()

	waitFor(t, 60*time.Second, "every podium container, network and volume to be gone", func() bool {
		containers, err := cli.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
		})
		if err != nil || len(containers) > 0 {
			return false
		}
		nets, err := cli.NetworkList(ctx, network.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
		})
		if err != nil || len(nets) > 0 {
			return false
		}
		vols, err := cli.VolumeList(ctx, volume.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
		})
		if err != nil || len(vols.Volumes) > 0 {
			return false
		}
		return true
	})
}

// cleanupEngine removes anything a failed test left on the engine, so the next test starts
// from a clean slate rather than adopting a stranger's container.
func cleanupEngine(t *testing.T) {
	t.Helper()
	cli := dockerClient(t)
	ctx := context.Background()

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
	})
	if err != nil {
		return
	}
	for _, c := range containers {
		_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
	nets, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
	})
	if err == nil {
		for _, n := range nets {
			_ = cli.NetworkRemove(ctx, n.ID)
		}
	}
	vols, err := cli.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask)),
	})
	if err == nil {
		for _, v := range vols.Volumes {
			_ = cli.VolumeRemove(ctx, v.Name, true)
		}
	}
}

// containerState is the Docker state of a task's container, or "" when it is gone.
func containerState(t *testing.T, taskID string) string {
	t.Helper()
	cli := dockerClient(t)
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", docker.LabelTask+"="+taskID)),
	})
	require.NoError(t, err)
	if len(containers) == 0 {
		return ""
	}
	return string(containers[0].State)
}

// drain copies a pipe into a buffer that the test can read while the process is running.
type drain struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (d *drain) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}

func (d *drain) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

var _ io.Writer = (*drain)(nil)
