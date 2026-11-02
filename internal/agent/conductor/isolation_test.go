package conductor

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnUnfencedHostTurnIsTheRuntimeAlone(t *testing.T) {
	h := &HostRuntime{Node: "node", Entry: "/opt/runtime/dist/main.js"}
	name, args := h.command(hostPaths{dir: "/tmp/turn", work: "/tmp/turn/work"})
	assert.Equal(t, "node", name)
	assert.Equal(t, []string{"/opt/runtime/dist/main.js"}, args)
	assert.False(t, h.isolated())
}

func TestAFencedHostTurnRunsTheRuntimeInsideBwrapInsideAScope(t *testing.T) {
	h := &HostRuntime{
		Node: "node", Entry: "/opt/runtime/dist/main.js", Runner: "/opt/podium/podium-runner",
		Isolation: IsolationBwrap, Bwrap: "/usr/bin/bwrap", MemoryMax: "1G",
	}
	jail := hostPaths{dir: "/var/lib/podium/turn-1", work: "/var/lib/podium/turn-1/work"}
	name, args := h.command(jail)
	argv := append([]string{name}, args...)

	require.Equal(t, "systemd-run", argv[0], "the scope is outermost, so the memory cap covers the sandbox too")
	assert.Contains(t, argv, "MemoryMax=1G")
	bw := slices.Index(argv, "/usr/bin/bwrap")
	require.Positive(t, bw)
	assert.Equal(t, "--", argv[bw-1], "systemd-run's options end before bwrap")

	tail := argv[len(argv)-3:]
	assert.Equal(t, []string{"--", "node", "/opt/runtime/dist/main.js"}, tail, "the runtime is the same argv inside")

	joined := strings.Join(argv, " ")
	assert.Contains(t, joined, "--unshare-all --share-net", "a PID namespace of its own, and the network shared")
	assert.Contains(t, joined, "--bind /var/lib/podium/turn-1 /var/lib/podium/turn-1", "the jail is the one writable place")
	assert.Contains(t, joined, "--chdir /var/lib/podium/turn-1/work")
	assert.Contains(t, joined, "--ro-bind-try /opt/runtime /opt/runtime", "the runtime's root, for dist and node_modules")
	assert.Contains(t, joined, "--ro-bind-try /opt/podium/podium-runner /opt/podium/podium-runner")
	assert.NotContains(t, joined, "--ro-bind-try /etc /etc", "an install's own configuration lives in /etc")
	assert.NotContains(t, joined, os.Getenv("HOME")+" "+os.Getenv("HOME"), "never the whole home")
}

func TestIsolationValidation(t *testing.T) {
	require.NoError(t, ValidateIsolation("", ""))
	require.Error(t, ValidateIsolation("firejail", ""))
	require.Error(t, ValidateIsolation(IsolationBwrap, "/nonexistent/bwrap"))
}

// TestABwrapTurnCannotSeeOtherTurnsOrTheHome runs a real sandbox, when this machine has
// bubblewrap and lets an unprivileged user make namespaces. PODIUM_TEST_BWRAP names the binary
// when it is not on PATH.
func TestABwrapTurnCannotSeeOtherTurnsOrTheHome(t *testing.T) {
	bwrap := os.Getenv("PODIUM_TEST_BWRAP")
	if bwrap == "" {
		var err error
		if bwrap, err = exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is not installed; set PODIUM_TEST_BWRAP to run this")
		}
	}
	root, err := os.MkdirTemp("/tmp", "pod")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	jail, err := hostJail(root, "turn_1")
	require.NoError(t, err)

	// Something of this user's that is not the runtime: it must not be reachable.
	secretDir, err := os.MkdirTemp(os.Getenv("HOME"), ".podium-isolation-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(secretDir) })
	secret := filepath.Join(secretDir, "master.key")
	require.NoError(t, os.WriteFile(secret, []byte("hunter2"), 0o600))

	script := filepath.Join(jail.work, "probe.sh")
	require.NoError(t, os.WriteFile(script, []byte(`
set -u
[ ! -e /proc/`+strconv.Itoa(os.Getpid())+`/environ ] || { echo "sees the conductor's own process"; exit 10; }
pids=$(ls /proc | grep -c '^[0-9]')
[ "$pids" -le 8 ] || { echo "sees $pids processes"; exit 10; }
[ ! -e "`+secret+`" ] || { echo "reads the home"; exit 11; }
echo written > "`+jail.work+`/out" || { echo "cannot write its jail"; exit 12; }
touch /usr/podium-probe 2>/dev/null && { echo "writes the system"; exit 13; }
[ -r /etc/resolv.conf ] || [ ! -e /etc/resolv.conf ] || { echo "cannot resolve names"; exit 14; }
exit 0
`), 0o700))

	h := &HostRuntime{Node: "/bin/sh", Entry: script, Isolation: IsolationBwrap, Bwrap: bwrap}
	name, args := h.command(jail)
	cmd := exec.Command(name, args...)
	cmd.Dir = jail.work
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "No permissions to create new namespace") {
		t.Skip("this kernel does not let an unprivileged user make namespaces")
	}
	require.NoError(t, err, string(out))
	got, err := os.ReadFile(filepath.Join(jail.work, "out"))
	require.NoError(t, err)
	assert.Equal(t, "written\n", string(got))
}
