package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shortDir is a temp directory guaranteed to leave room for a Unix socket path: on macOS
// t.TempDir() alone is already close to the 104-byte sun_path budget.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pdmrun")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// capture gives Run somewhere to send the child's stdout and stderr.
func capture(t *testing.T, dir, name string) (*os.File, func() string) {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
}

// fakeNode is the node's half of the event socket: it accepts one connection and collects
// the newline-delimited JSON the runner writes.
type fakeNode struct {
	path string
	ln   net.Listener

	mu    sync.Mutex
	conn  net.Conn
	lines []map[string]any
	done  chan struct{}
}

func newFakeNode(t *testing.T, dir string) *fakeNode {
	t.Helper()
	path := filepath.Join(dir, "events.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	n := &fakeNode{path: path, ln: ln, done: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		defer close(n.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		n.mu.Lock()
		n.conn = conn
		n.mu.Unlock()

		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			n.mu.Lock()
			n.lines = append(n.lines, m)
			n.mu.Unlock()
		}
	}()
	return n
}

// vanish drops the node's end of the socket the way a killed process would.
func (n *fakeNode) vanish(t *testing.T) {
	t.Helper()
	require := func(err error) {
		if err != nil {
			t.Fatalf("vanish: %v", err)
		}
	}
	require(n.ln.Close())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		conn := n.conn
		n.mu.Unlock()
		if conn != nil {
			require(conn.Close())
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the fake node never accepted a connection")
}

func (n *fakeNode) events(t *testing.T) []map[string]any {
	t.Helper()
	select {
	case <-n.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never closed the event socket")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lines
}

// baseConfig is a config that never touches the real stdio or the real socket path.
func baseConfig(t *testing.T, dir string) (Config, func() string, func() string) {
	t.Helper()
	stdout, readOut := capture(t, dir, "stdout")
	stderr, readErr := capture(t, dir, "stderr")
	return Config{
		TaskID:      "task_test",
		LeaseID:     "lease_test",
		WorkDir:     dir,
		DialTimeout: 200 * time.Millisecond,
		Stdout:      stdout,
		Stderr:      stderr,
	}, readOut, readErr
}

func TestRunPassesThroughBothStreamsAndTheExitCode(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, readOut, readErr := baseConfig(t, dir)
	cfg.EventsSock = node.path

	code := Run(context.Background(), cfg, []string{"sh", "-c", "echo out; echo err >&2; exit 3"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if got := readOut(); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
	if got := readErr(); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
}

func TestRunUsesTheConfiguredWorkingDirectory(t *testing.T) {
	dir := shortDir(t)
	cfg, readOut, _ := baseConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if code := Run(context.Background(), cfg, []string{"ls", "marker"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(readOut()); got != "marker" {
		t.Errorf("stdout = %q, want %q", got, "marker")
	}
}

func TestRunEmitsStartedThenExited(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, _, _ := baseConfig(t, dir)
	cfg.EventsSock = node.path

	if code := Run(context.Background(), cfg, []string{"sh", "-c", "exit 3"}); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}

	evs := node.events(t)
	if len(evs) != 2 {
		t.Fatalf("events = %v, want started then exited", evs)
	}
	if evs[0]["kind"] != "started" || evs[0]["v"] != float64(1) {
		t.Errorf("first event = %v, want started v1", evs[0])
	}
	if pid, ok := evs[0]["pid"].(float64); !ok || pid <= 0 {
		t.Errorf("started.pid = %v, want a positive pid", evs[0]["pid"])
	}
	if _, err := time.Parse(time.RFC3339Nano, evs[0]["ts"].(string)); err != nil {
		t.Errorf("started.ts = %v: %v", evs[0]["ts"], err)
	}
	if evs[1]["kind"] != "exited" || evs[1]["exit_code"] != float64(3) {
		t.Errorf("second event = %v, want exited{exit_code:3}", evs[1])
	}
	if _, present := evs[1]["signal"]; present {
		t.Errorf("a clean exit must not report a signal: %v", evs[1])
	}
}

func TestRunWithoutASocketWarnsAndStillRuns(t *testing.T) {
	dir := shortDir(t)
	cfg, readOut, readErr := baseConfig(t, dir)
	cfg.EventsSock = filepath.Join(dir, "nothing-is-listening.sock")

	if code := Run(context.Background(), cfg, []string{"sh", "-c", "echo alive; exit 7"}); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if got := readOut(); got != "alive\n" {
		t.Errorf("stdout = %q, want %q", got, "alive\n")
	}
	if got := readErr(); !strings.Contains(got, "no event socket") {
		t.Errorf("stderr = %q, want a warning about the missing socket", got)
	}
}

func TestRunForwardsSIGTERMToTheChild(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, readOut, _ := baseConfig(t, dir)
	cfg.EventsSock = node.path

	ready := filepath.Join(dir, "ready")
	go signalSelfWhenReady(ready)

	start := time.Now()
	code := Run(context.Background(), cfg, []string{"sh", "-c",
		`trap 'echo trapped; exit 143' TERM; : > ` + ready + `; while :; do sleep 0.1; done`})
	elapsed := time.Since(start)

	if code != 143 {
		t.Fatalf("exit code = %d, want 143", code)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("took %s; SIGTERM must be forwarded immediately", elapsed)
	}
	if got := readOut(); !strings.Contains(got, "trapped") {
		t.Errorf("stdout = %q, want the child's TERM handler to have run", got)
	}
	t.Logf("handled SIGTERM: exit %d after %s", code, elapsed)

	evs := node.events(t)
	last := evs[len(evs)-1]
	if last["kind"] != "exited" || last["exit_code"] != float64(143) {
		t.Errorf("last event = %v, want exited{exit_code:143}", last)
	}
}

func TestRunKillsAChildThatIgnoresSIGTERM(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, _, readErr := baseConfig(t, dir)
	cfg.EventsSock = node.path
	cfg.KillAfter = 500 * time.Millisecond

	ready := filepath.Join(dir, "ready")
	go signalSelfWhenReady(ready)

	start := time.Now()
	code := Run(context.Background(), cfg, []string{"sh", "-c",
		`trap '' TERM; : > ` + ready + `; while :; do sleep 0.1; done`})
	elapsed := time.Since(start)

	if code != 137 {
		t.Fatalf("exit code = %d, want 137 (128+SIGKILL)", code)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("took %s; the kill timer is %s", elapsed, cfg.KillAfter)
	}
	if got := readErr(); !strings.Contains(got, "killing the process group") {
		t.Errorf("stderr = %q, want the escalation warning", got)
	}
	t.Logf("ignored SIGTERM: exit %d after %s", code, elapsed)

	evs := node.events(t)
	last := evs[len(evs)-1]
	if last["signal"] != "SIGKILL" {
		t.Errorf("last event = %v, want signal SIGKILL", last)
	}
}

// TestEventsSurviveANodeThatDisappears is the node-restart case seen from inside the
// container: the socket the runner connected to dies with the process that listened on it.
// The write fails, the client retires itself, and nothing blocks or panics.
func TestEventsSurviveANodeThatDisappears(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)

	c := dialEvents(node.path, time.Second)
	if c == nil {
		t.Fatal("dial failed")
	}
	c.started(1)
	node.vanish(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 5 {
			c.exited(0, "")
		}
		c.close()
		c.exited(0, "") // a closed client is still a working no-op
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sending to a dead node blocked")
	}
}

func TestRunReportsAMissingCommand(t *testing.T) {
	dir := shortDir(t)
	cfg, _, readErr := baseConfig(t, dir)

	if code := Run(context.Background(), cfg, []string{"podium-no-such-command"}); code != 127 {
		t.Fatalf("exit code = %d, want 127", code)
	}
	if got := readErr(); !strings.Contains(got, "podium-no-such-command") {
		t.Errorf("stderr = %q, want the failing command named", got)
	}
	if code := Run(context.Background(), cfg, nil); code != 2 {
		t.Fatalf("exit code for an empty command = %d, want 2", code)
	}
}

func TestConfigFromEnvReadsTheContainerEnvironment(t *testing.T) {
	t.Setenv(EnvTaskID, "task_01")
	t.Setenv(EnvLeaseID, "lease_01")
	t.Setenv(EnvEventsSock, "/podium/events.sock")
	t.Setenv(EnvWorkDir, "/srv")
	t.Setenv(EnvKillAfter, "1s")

	cfg := ConfigFromEnv()
	if cfg.TaskID != "task_01" || cfg.LeaseID != "lease_01" {
		t.Errorf("ids = %q/%q", cfg.TaskID, cfg.LeaseID)
	}
	if cfg.EventsSock != "/podium/events.sock" || cfg.WorkDir != "/srv" {
		t.Errorf("paths = %q/%q", cfg.EventsSock, cfg.WorkDir)
	}
	if cfg.KillAfter != time.Second {
		t.Errorf("kill after = %s, want 1s", cfg.KillAfter)
	}

	t.Setenv(EnvKillAfter, "not-a-duration")
	if got := ConfigFromEnv(); got.KillAfter != 0 {
		t.Errorf("an unparseable %s must leave the default in place, got %s", EnvKillAfter, got.KillAfter)
	}
}

func TestChildEnvDropsTheSocketPath(t *testing.T) {
	t.Setenv(EnvEventsSock, "/podium/events.sock")
	t.Setenv(EnvTaskID, "task_01")
	for _, kv := range childEnv() {
		if strings.HasPrefix(kv, EnvEventsSock+"=") {
			t.Fatalf("%s reached the child: %v", EnvEventsSock, kv)
		}
	}
}

func TestExitStatusNamesTheKillingSignal(t *testing.T) {
	var ws syscall.WaitStatus
	if code, name := exitStatus(ws); code != 0 || name != "" {
		t.Errorf("clean exit = %d/%q", code, name)
	}
	// 0x0f is "terminated by signal 15" in the wait(2) encoding on both Linux and Darwin.
	ws = syscall.WaitStatus(0x000f)
	code, name := exitStatus(ws)
	if code != 143 || name != "SIGTERM" {
		t.Errorf("signalled exit = %d/%q, want 143/SIGTERM", code, name)
	}
}

// signalSelfWhenReady sends this process a SIGTERM once the child has said it is ready.
// Run installs a handler for the duration of the call, so the test binary survives it. It
// reports nothing on failure: the test it serves then blocks and fails on its own timeout,
// which is safer than logging from a goroutine that may outlive the test.
func signalSelfWhenReady(marker string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
