package conductor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/store"
)

func TestHostJailIsUnderTheStateDirectoryWhenTheSocketFits(t *testing.T) {
	// A short root, because the assertion is about where the jail goes and not about the
	// length of a temp path: t.TempDir() is already too long for AF_UNIX on macOS, which is
	// the case below.
	root, err := os.MkdirTemp("/tmp", "pod")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	jail, err := hostJail(root, "turn_01")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(jail.dir) })

	assert.Equal(t, root, filepath.Dir(jail.dir))
	assert.Equal(t, filepath.Join(jail.dir, "home"), jail.home)
	assert.Equal(t, filepath.Join(jail.dir, "work"), jail.work)
	assert.Equal(t, filepath.Join(jail.dir, "events.sock"), jail.sock)
	for _, dir := range []string{jail.home, jail.work, jail.tmp} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir(), dir)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), dir)
	}
	assert.LessOrEqual(t, len(jail.sock), hostSockPathMax)
}

func TestHostJailFallsBackWhenTheSocketPathWouldNotFit(t *testing.T) {
	// AF_UNIX has room for about a hundred bytes and no more. A state directory deep enough
	// to overflow it must not fail the turn.
	deep := filepath.Join(t.TempDir(), strings.Repeat("deeper/", 12))
	require.NoError(t, os.MkdirAll(deep, 0o700))

	jail, err := hostJail(deep, "turn_01hzzzzzzzzzzzzzzzzzzzzzzz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(jail.dir) })

	assert.NotEqual(t, deep, filepath.Dir(jail.dir), "the deep root was not usable")
	assert.LessOrEqual(t, len(jail.sock), hostSockPathMax)
	info, err := os.Stat(jail.home)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestHostOutcomeReadsTheRuntimesExitCode(t *testing.T) {
	assert.Equal(t, store.TurnSucceeded, hostOutcome(0, false))
	assert.Equal(t, store.TurnFailed, hostOutcome(2, false), "an unusable brief")
	assert.Equal(t, store.TurnFailed, hostOutcome(3, false), "out of turns, which it says itself")
	assert.Equal(t, store.TurnFailed, hostOutcome(4, false), "the harness or the API failed")
	// A cancelled turn is not a failed one, whatever it exited with: the runtime is killed
	// mid-answer on purpose.
	assert.Equal(t, store.TurnCancelled, hostOutcome(0, true))
	assert.Equal(t, store.TurnCancelled, hostOutcome(143, true))
}

func TestFenceForHostTakesAwayEverythingAHostTurnMustNotHave(t *testing.T) {
	c := &Conductor{host: &HostRuntime{
		MemoryMCPURL: "http://127.0.0.1:8888/mcp/podium/",
		TurnURL:      "http://127.0.0.1:8090",
	}}
	b := &Brief{
		Playbook: BriefPlaybook{AllowedTools: []string{"bash", "read", "write", "edit"}},
		Repos:    []BriefRepo{{Name: "podium", URL: "https://example.test/podium", DefaultBranch: "main"}},
		Browser:  &BriefBrowser{CDPURL: "http://chrome:9222"},
		Memory:   &BriefMemory{MCPURL: "http://host.docker.internal:8888/mcp/podium/", APIKeyEnv: "K"},
	}
	menu := []DelegablePlaybook{{Name: "podium", Summary: "develops Podium itself", Docker: true}}
	c.fenceForHost(b, menu)

	assert.Equal(t, hostTools, b.Playbook.AllowedTools, "the playbook's tools are not a host turn's")
	assert.Nil(t, b.Repos, "a host turn clones nothing")
	assert.Nil(t, b.Browser, "there is no sidecar beside a host turn")
	require.NotNil(t, b.Memory)
	assert.Equal(t, "http://127.0.0.1:8888/mcp/podium/", b.Memory.MCPURL,
		"host.docker.internal resolves in a container and nowhere else")
	assert.Equal(t, "K", b.Memory.APIKeyEnv)

	// The tools were taken away on the understanding that the work goes to a container, so
	// the menu that makes that possible has to be in the brief.
	require.NotNil(t, b.Delegation, "a host turn with nowhere to delegate is a turn that can only talk")
	assert.Equal(t, "http://127.0.0.1:8090", b.Delegation.URL)
	assert.Equal(t, TurnTokenEnv, b.Delegation.TokenEnv)
	assert.Equal(t, menu, b.Delegation.Playbooks)
}

func TestFenceForHostOffersNoDelegationWhenThereIsNowhereToSendIt(t *testing.T) {
	c := &Conductor{host: &HostRuntime{}}
	b := &Brief{Playbook: BriefPlaybook{AllowedTools: []string{"bash"}}}
	c.fenceForHost(b, []DelegablePlaybook{{Name: "podium"}})
	assert.Nil(t, b.Delegation, "no address to reach the conductor at is no delegation")

	c = &Conductor{host: &HostRuntime{TurnURL: "http://127.0.0.1:8090"}}
	b = &Brief{Playbook: BriefPlaybook{AllowedTools: []string{"bash"}}}
	c.fenceForHost(b, nil)
	assert.Nil(t, b.Delegation, "an empty menu is not a menu")
}

func TestFenceForHostDropsMemoryThisHostCannotReach(t *testing.T) {
	c := &Conductor{host: &HostRuntime{}}
	b := &Brief{Memory: &BriefMemory{MCPURL: "http://host.docker.internal:8888/", APIKeyEnv: "K"}}
	c.fenceForHost(b, nil)
	assert.Nil(t, b.Memory, "the runtime fails a turn whose memory server it cannot reach")
}

func TestAHostRuntimeSaysWhatItIsMissing(t *testing.T) {
	credential := func(string) {}
	_ = credential
	for _, tc := range []struct {
		name string
		host HostRuntime
		want string
	}{
		{"no entry", HostRuntime{Runner: "/bin/true"}, "PODIUM_AGENT_HOST_RUNTIME"},
		{"no runner", HostRuntime{Entry: "/main.js"}, "PODIUM_AGENT_RUNNER_BIN"},
		{"no credential", HostRuntime{Entry: "/main.js", Runner: "/bin/true"}, "credential source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.host.validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
