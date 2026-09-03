// Package runner is podium-runner: PID 1 of every Podium task container.
//
// It runs the task command as a child in its own process group, forwards signals to that
// group, reaps orphaned grandchildren, and reports structured events to the node over a
// Unix socket. Being PID 1 is the whole point: the kernel discards a default-disposition
// signal sent to a PID namespace's init, so a task command running as PID 1 itself cannot
// be asked to stop — only killed. The runner has a real handler, so `podium task cancel`
// becomes a prompt SIGTERM instead of a 30-second wait for SIGKILL.
//
// This package depends on nothing outside the standard library. The binary is
// bind-mounted read-only into images Podium does not control.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Environment variables the node sets on the task container.
const (
	EnvTaskID     = "PODIUM_TASK_ID"
	EnvLeaseID    = "PODIUM_LEASE_ID"
	EnvEventsSock = "PODIUM_EVENTS_SOCK"
	EnvWorkDir    = "PODIUM_WORKDIR"
	EnvKillAfter  = "PODIUM_KILL_AFTER"
)

// Defaults for the values above and for the timers this package owns.
const (
	DefaultEventsSock = "/podium/events.sock"
	DefaultWorkDir    = "/workspace"

	// DefaultKillAfter is how long a child gets between the SIGTERM we forward and the
	// SIGKILL we send its group. It stays inside the node's own 30s cancel grace so the
	// container is already gone when the engine's SIGKILL would land.
	DefaultKillAfter = 25 * time.Second

	// DefaultDialTimeout is how long the event socket is retried before giving up.
	// Events are best-effort: the command runs either way.
	DefaultDialTimeout = 5 * time.Second
)

// Exit codes the runner reports for failures of its own, borrowed from the shell so they
// read the same way in `podium task get`.
const (
	exitUsage    = 2   // no command to run
	exitNoExec   = 126 // found but not executable
	exitNotFound = 127 // command not found
	exitInternal = 125 // the runner itself failed
)

// Config is everything the runner needs. The zero value is usable: Run fills in the
// defaults above.
type Config struct {
	TaskID  string
	LeaseID string
	// EventsSock is the Unix socket the node listens on; empty means DefaultEventsSock.
	EventsSock string
	// WorkDir is the child's working directory.
	WorkDir string
	// KillAfter is the SIGTERM → SIGKILL delay for the child's process group.
	KillAfter time.Duration
	// DialTimeout bounds the retry window for the event socket.
	DialTimeout time.Duration
	// Stdout and Stderr are the files the child inherits as fd 1 and fd 2, and Stderr is
	// also where the runner writes its own diagnostics. Both default to the process's.
	// Stdin is always inherited.
	Stdout *os.File
	Stderr *os.File
}

// ConfigFromEnv reads the configuration the node passes in the container environment.
func ConfigFromEnv() Config {
	cfg := Config{
		TaskID:     os.Getenv(EnvTaskID),
		LeaseID:    os.Getenv(EnvLeaseID),
		EventsSock: os.Getenv(EnvEventsSock),
		WorkDir:    os.Getenv(EnvWorkDir),
	}
	if d, err := time.ParseDuration(os.Getenv(EnvKillAfter)); err == nil && d > 0 {
		cfg.KillAfter = d
	}
	return cfg
}

func (c *Config) applyDefaults() {
	if c.EventsSock == "" {
		c.EventsSock = DefaultEventsSock
	}
	if c.WorkDir == "" {
		c.WorkDir = DefaultWorkDir
	}
	if c.KillAfter <= 0 {
		c.KillAfter = DefaultKillAfter
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
}

// forwarded are the signals the runner relays to the child's process group. SIGKILL and
// SIGSTOP cannot be caught, and everything else either kills the runner (which is what a
// broken runner deserves) or is none of its business.
var forwarded = []os.Signal{
	syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT,
	syscall.SIGUSR1, syscall.SIGUSR2,
}

// Run executes argv to completion and returns the exit code the runner should exit with:
// the child's own code, or 128+signo when a signal killed it. It never calls os.Exit, so
// it is testable.
func Run(ctx context.Context, cfg Config, argv []string) int {
	cfg.applyDefaults()
	warn := func(format string, a ...any) {
		fmt.Fprintf(cfg.Stderr, "podium-runner: "+format+"\n", a...)
	}

	if len(argv) == 0 {
		warn("no command to run")
		return exitUsage
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		warn("%v", err)
		if errors.Is(err, exec.ErrNotFound) {
			return exitNotFound
		}
		return exitNoExec
	}

	events := dialEvents(cfg.EventsSock, cfg.DialTimeout)
	if events == nil {
		warn("no event socket at %s; continuing without structured events", cfg.EventsSock)
	}
	defer events.close()

	// Install the handlers before the fork: a cancel that arrives in the moment between
	// process start and fork must not be lost, and the buffered channel keeps it until
	// there is a process group to forward it to.
	sigs := make(chan os.Signal, len(forwarded))
	signal.Notify(sigs, forwarded...)
	defer signal.Stop(sigs)

	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{
		Dir:   cfg.WorkDir,
		Env:   childEnv(),
		Files: []uintptr{os.Stdin.Fd(), cfg.Stdout.Fd(), cfg.Stderr.Fd()},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
	if err != nil {
		warn("exec %s in %s: %v", path, cfg.WorkDir, err)
		return exitNoExec
	}
	events.started(pid)

	// Setpgid with no Pgid makes the child the leader of a new group whose id is its pid,
	// so -pid addresses the child and everything it spawns.
	kill := func(sig syscall.Signal) {
		if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			warn("signal %s to process group %d: %v", sig, pid, err)
		}
	}

	exits := reap(pid)
	var deadline <-chan time.Time
	aborted := ctx.Done()
	for {
		select {
		case s := <-sigs:
			sig, ok := s.(syscall.Signal)
			if !ok {
				continue
			}
			kill(sig)
			if sig == syscall.SIGTERM && deadline == nil {
				deadline = time.After(cfg.KillAfter)
			}
		case <-deadline:
			warn("child did not exit %s after SIGTERM; killing the process group", cfg.KillAfter)
			deadline = nil
			kill(syscall.SIGKILL)
		case <-aborted:
			aborted = nil // a nil channel blocks, so this fires exactly once
			kill(syscall.SIGKILL)
		case ws, ok := <-exits:
			if !ok {
				warn("lost track of child %d", pid)
				return exitInternal
			}
			code, signame := exitStatus(ws)
			events.exited(code, signame)
			return code
		}
	}
}

// childEnv is the runner's environment minus the socket path, which is the runner's
// business and not the task's.
func childEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvEventsSock+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// reap waits on every child this process collects and reports the direct child's status.
// Reaping is the other half of being PID 1: a grandchild orphaned by its own parent is
// re-parented here, and without this loop it would stay a zombie for the life of the task.
func reap(child int) <-chan syscall.WaitStatus {
	out := make(chan syscall.WaitStatus, 1)
	go func() {
		defer close(out)
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, 0, nil)
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				return
			}
			if pid == child {
				out <- ws
				return
			}
		}
	}()
	return out
}

// exitStatus turns a wait status into the code the runner exits with and the name of the
// signal that killed the child, if one did.
func exitStatus(ws syscall.WaitStatus) (code int, signame string) {
	if ws.Signaled() {
		sig := ws.Signal()
		return 128 + int(sig), signalName(sig)
	}
	return ws.ExitStatus(), ""
}

// signalNames is the canonical SIGxxx spelling for the signals a task can plausibly die
// from. syscall.Signal.String() reports the human description ("terminated"), which is not
// what the event protocol documents.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL: "SIGILL", syscall.SIGABRT: "SIGABRT", syscall.SIGFPE: "SIGFPE",
	syscall.SIGKILL: "SIGKILL", syscall.SIGSEGV: "SIGSEGV", syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	syscall.SIGUSR1: "SIGUSR1", syscall.SIGUSR2: "SIGUSR2",
}

func signalName(sig syscall.Signal) string {
	if name, ok := signalNames[sig]; ok {
		return name
	}
	return fmt.Sprintf("SIG%d", int(sig))
}
