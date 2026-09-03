package docker

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Where the runner and its event socket appear inside a task container. These are
// canonical names from the design and the runner's own defaults depend on them.
const (
	runnerTarget = "/podium/runner"
	eventsTarget = "/podium/events.sock"

	// eventsSockFile is the socket's basename on the host.
	eventsSockFile = "events.sock"
)

const (
	// runnerConnectTimeout bounds how long the executor holds the socket open waiting for
	// the runner to dial in. The runner connects within milliseconds of the container
	// starting; anything longer means it never will (a foreign entrypoint, a broken
	// bind mount) and the run continues on Docker API events alone.
	runnerConnectTimeout = 5 * time.Second

	// maxRunnerLine caps one line of the newline-delimited JSON protocol.
	maxRunnerLine = 64 * 1024

	// maxUnixPath is the portable AF_UNIX sun_path budget: 104 bytes on Darwin, 108 on
	// Linux, minus room for the terminating NUL and a little slack.
	maxUnixPath = 100

	// taskIDBudget is how many characters a task ID may take when sizing socket paths.
	// A ULID id is "task_" plus 26 characters.
	taskIDBudget = 40
)

// runnerbin holds the cross-compiled runner for every architecture a node can drive. The
// `all:` prefix picks up the committed .gitkeep placeholder: //go:embed of an empty or
// missing directory is a compile error, so a clone that has never run `make runner-embed`
// would otherwise fail `go build ./...`. With the placeholder the build succeeds and New
// reports the missing binary at runtime instead.
//
//go:embed all:runnerbin
var runnerbin embed.FS

// runnerArch maps the architecture the Docker engine reports onto a Go GOARCH.
func runnerArch(engineArch string) (string, error) {
	switch engineArch {
	case "amd64", "x86_64", "x86-64":
		return "amd64", nil
	case "arm64", "aarch64", "armv8l", "armv8b":
		return "arm64", nil
	default:
		return "", fmt.Errorf("docker engine: architecture %q has no podium-runner; podium ships linux/amd64 and linux/arm64", engineArch)
	}
}

// extractRunner writes the embedded runner for arch into <dataDir>/runner/<arch>/ and
// returns the path to bind-mount into task containers. It rewrites the file on every
// start, so upgrading the node upgrades the runner with it.
func extractRunner(dataDir, arch string) (string, error) {
	data, err := runnerbin.ReadFile("runnerbin/runner-linux-" + arch)
	if err != nil {
		return "", fmt.Errorf("docker executor: no podium-runner embedded for linux/%s; run `make runner-embed`: %w", arch, err)
	}

	dir := filepath.Join(dataDir, "runner", arch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("docker executor: create runner dir: %w", err)
	}
	path := filepath.Join(dir, "podium-runner")

	// Write to a sibling and rename: a half-written runner bind-mounted into a container
	// is a task that fails for no visible reason.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil { //nolint:gosec // the runner is meant to be executable
		return "", fmt.Errorf("docker executor: write runner: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("docker executor: install runner: %w", err)
	}
	return path, nil
}

// socketDirFor decides where a task's event socket lives. It belongs in the task's state
// directory, but AF_UNIX paths are capped at ~104 bytes and a deep data dir blows that
// budget, so an executor that cannot fit one keeps its sockets in a private directory
// under /tmp instead. The socket is per-incarnation state — a restarted node must never
// reuse the one its predecessor left behind — so /tmp is a fair home for it.
func socketDirFor(dataDir string) (string, error) {
	longest := filepath.Join(dataDir, "tasks", strings.Repeat("t", taskIDBudget), eventsSockFile)
	if len(longest) <= maxUnixPath {
		return "", nil
	}
	dir, err := os.MkdirTemp("/tmp", "podium-run-")
	if err != nil {
		return "", fmt.Errorf("docker executor: create socket dir: %w", err)
	}
	return dir, nil
}

// eventsSocketPath is the host path of one task's runner event socket.
func (e *Executor) eventsSocketPath(taskID string) string {
	if e.sockDir != "" {
		return filepath.Join(e.sockDir, taskID+".sock")
	}
	return filepath.Join(e.taskDir(taskID), eventsSockFile)
}

// runnerEvent is one line of the runner's newline-delimited JSON protocol. Unknown kinds
// decode into this same shape and are forwarded as opaque step events, so a newer runner
// cannot break an older node.
type runnerEvent struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	TS       string `json:"ts"`
	PID      int    `json:"pid"`
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	// Path and ContentType belong to the artifact kind: the file inside the container
	// the node should collect, and what it is.
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
}

// Runner event kinds the executor understands. Everything else is opaque.
const (
	runnerKindStarted  = "started"
	runnerKindExited   = "exited"
	runnerKindArtifact = "artifact"
)

// runnerLink is the executor's end of one task's event socket: a listener that stays open
// for the whole run and decodes every line anything inside the container sends it.
//
// It accepts more than one connection on purpose. The runner itself dials once, at start,
// and holds that connection for the life of the task — but `podium-runner artifact add` is
// a second process inside the same container dialling the same socket, and it is the
// documented way for a task to hand Podium a file from any shell. A single-connection
// listener would refuse it.
type runnerLink struct {
	path    string
	log     *slog.Logger
	events  chan runnerEvent
	stopped chan struct{}
	once    sync.Once
	conns   sync.WaitGroup

	mu     sync.Mutex
	ln     net.Listener
	active []net.Conn
}

// listenRunner opens the task's event socket. It must be called before ContainerStart so
// the runner never finds a missing socket. The listener has no deadline: it stays open
// until the run stops accepting, because a task may write an artifact event at any point.
func (e *Executor) listenRunner(taskID string) (*runnerLink, error) {
	path := e.eventsSocketPath(taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create runner socket dir for task %s: %w", taskID, err)
	}
	// A path left behind by a crashed incarnation would make Listen fail with EADDRINUSE.
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on runner event socket %s: %w", path, err)
	}
	// The container may run as a user other than the one the node runs as.
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // a socket, not a secret
		e.log.Warn("chmod runner event socket", "task", taskID, "error", err)
	}

	l := &runnerLink{
		path:    path,
		log:     e.log,
		events:  make(chan runnerEvent, 32),
		stopped: make(chan struct{}),
		ln:      ln,
	}
	go l.serve(taskID, ln)
	return l, nil
}

// serve accepts connections until the listener is closed and decodes the lines each one
// sends. Closing the events channel — once every connection has ended — is how every
// reader learns there is nothing more coming.
func (l *runnerLink) serve(taskID string, ln net.Listener) {
	defer func() {
		l.conns.Wait()
		close(l.events)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !l.isClosed() {
				l.log.Debug("runner event socket stopped accepting", "task", taskID, "error", err)
			}
			return
		}
		l.mu.Lock()
		l.active = append(l.active, conn)
		l.mu.Unlock()

		l.conns.Add(1)
		go func() {
			defer l.conns.Done()
			defer func() { _ = conn.Close() }()
			l.read(taskID, conn)
		}()
	}
}

// read decodes one connection's newline-delimited JSON until it ends.
func (l *runnerLink) read(taskID string, conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), maxRunnerLine)
	for sc.Scan() {
		var ev runnerEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			l.log.Warn("undecodable runner event", "task", taskID, "error", err)
			continue
		}
		select {
		case l.events <- ev:
		case <-l.stopped:
			return
		}
	}
	if err := sc.Err(); err != nil && !l.isClosed() {
		l.log.Warn("runner event stream ended badly", "task", taskID, "error", err)
	}
}

// awaitStarted blocks until the runner reports that it has forked the task command, the
// runner gives up connecting, or timeout elapses. ok is false when no runner reported in,
// which is when the caller falls back to the Docker API's own notion of "started".
func (l *runnerLink) awaitStarted(timeout time.Duration) (pid int, ok bool) {
	deadline := time.After(timeout)
	for {
		select {
		case ev, open := <-l.events:
			if !open {
				return 0, false
			}
			if ev.Kind == runnerKindStarted {
				return ev.PID, true
			}
		case <-deadline:
			return 0, false
		}
	}
}

// drain forwards the rest of the runner's events and closes the returned channel once the
// runner is done. The Docker API is authoritative for the exit, so `exited` is only
// logged; `artifact` is handed to onArtifact, which copies the file out and uploads it;
// every other kind becomes an opaque step event, which is how the node stays forward
// compatible with a runner that learns to report playbook steps.
func (l *runnerLink) drain(taskID string, em *emitter, onArtifact func(name, path, contentType string)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range l.events {
			switch ev.Kind {
			case runnerKindExited:
				l.log.Debug("runner reported exit", "task", taskID,
					"exit_code", ev.ExitCode, "signal", ev.Signal)
			case runnerKindStarted:
				// Already consumed by awaitStarted; a second one is a broken runner.
			case runnerKindArtifact:
				if ev.Path == "" {
					l.log.Warn("runner artifact event with no path", "task", taskID)
					continue
				}
				name := ev.Name
				if name == "" {
					name = path.Base(ev.Path)
				}
				if onArtifact != nil {
					onArtifact(name, ev.Path, ev.ContentType)
				}
			default:
				name := ev.Name
				if name == "" {
					name = ev.Kind
				}
				em.emit(KindStep, StepPayload{Name: name, Status: ev.Status, ExitCode: ev.ExitCode})
			}
		}
	}()
	return done
}

// close releases the socket and every connection on it. It is safe to call more than once
// and unblocks serve.
func (l *runnerLink) close() {
	if l == nil {
		return
	}
	l.once.Do(func() { close(l.stopped) })

	l.mu.Lock()
	ln, active := l.ln, l.active
	l.ln, l.active = nil, nil
	l.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range active {
		_ = c.Close()
	}
	_ = os.Remove(l.path)
}

// stopAccepting closes the listener without touching the connections that are already
// open, so the run can wait for the events still in flight and then see the channel close.
func (l *runnerLink) stopAccepting() {
	if l == nil {
		return
	}
	l.mu.Lock()
	ln := l.ln
	l.ln = nil
	l.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (l *runnerLink) isClosed() bool {
	select {
	case <-l.stopped:
		return true
	default:
		return false
	}
}

// imageCommand is the command an image runs by itself: its entrypoint followed by its
// default arguments. The runner replaces the image's entrypoint, so a task spec that
// names no command has to get it back from here.
func (e *Executor) imageCommand(ctx context.Context, ref string) ([]string, error) {
	insp, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if insp.Config == nil {
		return nil, nil
	}
	cmd := make([]string, 0, len(insp.Config.Entrypoint)+len(insp.Config.Cmd))
	cmd = append(cmd, insp.Config.Entrypoint...)
	return append(cmd, insp.Config.Cmd...), nil
}
