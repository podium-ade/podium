package conductor

// A host turn's fence.
//
// Without one, a host turn's "jail" is a directory and an environment built from empty, and
// that is all: the runtime runs as the conductor's own user, so it can read another turn's
// /proc/<pid>/environ — the model credential and its MCP tokens — and one turn that balloons
// can take the machine with it. The assistant's short tool list is what keeps a model from
// trying, and that is one allow-list away from not holding.
//
// IsolationBwrap puts each turn in a bubblewrap sandbox: its own PID namespace (so its /proc
// shows no other turn), its own mount namespace with the system read-only and nothing of this
// user's home but the runtime it needs, and its own jail as the only writable directory. The
// network is shared, because the turn talks to its model, its memory and this conductor over
// loopback. MemoryMax adds a transient systemd scope with a memory ceiling, so a turn that
// runs away is killed alone.
//
// Each is opt-in. Both are an argv prefix and nothing else: the runtime, its environment and
// its protocol are the same inside the fence as outside it.

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// The isolation a host turn can run under.
const (
	IsolationNone  = ""
	IsolationBwrap = "bwrap"
)

// bwrapSystemPaths are the parts of the system a turn may read: binaries, libraries, and the
// few files in /etc that name resolution and TLS need. Missing ones are skipped. All of /etc is
// deliberately NOT here — an install's own configuration, the conductor's included, lives there.
var bwrapSystemPaths = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64",
	"/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/alternatives",
	"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/host.conf",
	"/etc/passwd", "/etc/group", "/etc/localtime",
}

// isolated reports whether host turns run inside a bubblewrap sandbox.
func (h *HostRuntime) isolated() bool {
	return h != nil && h.Isolation == IsolationBwrap
}

// command is the argv a host turn runs: the runtime, behind whatever fence is configured.
func (h *HostRuntime) command(jail hostPaths) (string, []string) {
	argv := []string{h.node(), h.Entry}
	if h.isolated() {
		argv = append(h.bwrapArgs(jail), argv...)
	}
	if h.MemoryMax != "" {
		argv = append(h.scopeArgs(), argv...)
	}
	return argv[0], argv[1:]
}

// scopeArgs runs what follows in a transient systemd scope with a memory ceiling. --user,
// because the conductor is not root and should not be; --collect, so a scope whose turn was
// killed for its memory does not linger as a failed unit.
func (h *HostRuntime) scopeArgs() []string {
	bin := h.SystemdRun
	if bin == "" {
		bin = "systemd-run"
	}
	return []string{
		bin, "--user", "--scope", "--quiet", "--collect",
		"-p", "MemoryMax=" + h.MemoryMax,
		"-p", "MemorySwapMax=0",
		"--",
	}
}

// bwrapArgs is the sandbox: everything is unshared but the network, the system is mounted
// read-only, and the only things of this machine's own a turn can see are the runtime that
// runs it and its own jail.
func (h *HostRuntime) bwrapArgs(jail hostPaths) []string {
	bin := h.Bwrap
	if bin == "" {
		bin = "bwrap"
	}
	args := []string{
		bin,
		"--die-with-parent",
		"--unshare-all", "--share-net",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}
	for _, p := range bwrapSystemPaths {
		args = append(args, "--ro-bind-try", p, p)
	}
	for _, p := range h.runtimePaths() {
		args = append(args, "--ro-bind-try", p, p)
	}
	// The jail last, and writable: a later mount wins over an earlier one, so a jail under
	// one of the read-only paths above is still a jail the turn can write.
	args = append(args,
		"--bind", jail.dir, jail.dir,
		"--chdir", jail.work,
		"--",
	)
	return args
}

// runtimePaths are the directories a turn needs that may live outside the system paths: the
// node install, the built runtime with its node_modules, podium-runner, and the global npm
// prefix opencode is installed under. On a developer machine all of them are in somebody's
// home, which is exactly why the home itself is not mounted and these are.
func (h *HostRuntime) runtimePaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || p == "/" {
			return
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		for _, sys := range bwrapSystemPaths {
			if p == sys || strings.HasPrefix(p, sys+"/") {
				return
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if node, err := exec.LookPath(h.node()); err == nil {
		if resolved, err := filepath.EvalSymlinks(node); err == nil {
			node = resolved
		}
		// <prefix>/bin/node: the prefix holds the node install's own lib, and on an npm
		// install it is also where the global packages are.
		add(filepath.Dir(filepath.Dir(node)))
	}
	if h.Entry != "" {
		// <runtime>/dist/main.js: the runtime's root holds dist and node_modules both.
		add(filepath.Dir(filepath.Dir(h.Entry)))
	}
	add(h.Runner)
	if prefix, err := exec.Command("npm", "prefix", "-g").Output(); err == nil {
		add(strings.TrimSpace(string(prefix)))
	}
	return out
}

// ValidateIsolation checks a host isolation setting before the conductor starts with it.
func ValidateIsolation(isolation, bwrap string) error {
	switch isolation {
	case IsolationNone:
		return nil
	case IsolationBwrap:
		bin := bwrap
		if bin == "" {
			bin = "bwrap"
		}
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("host isolation %s needs bubblewrap: %w", IsolationBwrap, err)
		}
		return nil
	default:
		return errors.New("host isolation " + isolation + " is not " + IsolationBwrap + " or empty")
	}
}
