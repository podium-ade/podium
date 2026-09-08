package conductor

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
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

// TestTheAssistantsBriefHasNoContainerInIt. This is the whole security argument for running
// a turn in the conductor's own process, and it is a property of how the brief is BUILT
// rather than of something undoing it afterwards: the assistant's job carries no image, no
// repository and no sidecar, so there is nothing to take away.
func TestTheAssistantsBriefHasNoContainerInIt(t *testing.T) {
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{
		Name:            "podium",
		DisplayName:     "Podium",
		SystemPrompt:    "you are Podium",
		Model:           "claude-opus-5",
		DefaultPlaybook: "coder",
		MaxTurns:        12,
		// The playbook is deliberately the greediest one a profile could hold. None of it
		// may reach the assistant.
		Playbooks: map[string]profiles.Playbook{"coder": {
			Name:         "coder",
			Image:        "podium-agent-runtime-dev:dev",
			SystemPrompt: "you develop Podium",
			AllowedTools: []string{"bash", "read", "write", "edit"},
			MaxTurns:     200,
			Docker:       true,
			Browser:      true,
			Repos:        []profiles.Repo{{Name: "podium", URL: "https://example.test/p", DefaultBranch: "main"}},
		}},
	})}
	profile := c.profiles.Current()
	j := assistantJob(profile.Assistant())

	b := c.brief(store.Session{ID: "sess_1"}, j, "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil,
		j.choose(profile, profiles.Override{}))

	assert.Equal(t, hostTools, b.Playbook.AllowedTools, "a playbook's tools are not the assistant's")
	assert.Nil(t, b.Repos, "the assistant clones nothing")
	assert.Nil(t, b.Browser, "there is no sidecar beside the assistant")
	assert.Equal(t, AssistantName, b.Playbook.Name)
	assert.Equal(t, 12, b.Playbook.MaxTurns, "profile.yaml's max_turns, not the playbook's")
	assert.NotEqual(t, 200, b.Playbook.MaxTurns, "the playbook's cap must not reach the assistant")
	assert.Empty(t, b.Playbook.SystemPrompt,
		"the assistant IS the profile, so its prompt is profile.system_prompt and is not sent twice")
	assert.Equal(t, "you are Podium", b.Profile.SystemPrompt)
}

// TestHostBriefPointsTheTurnAtThisHost: where it runs, what it may delegate to, and memory
// at the address THIS process reaches it on.
func TestHostBriefPointsTheTurnAtThisHost(t *testing.T) {
	c := &Conductor{host: &HostRuntime{
		MemoryMCPURL: "http://127.0.0.1:8888/mcp/podium/",
		TurnURL:      "http://127.0.0.1:8090",
	}}
	b := &Brief{
		Playbook: BriefPlaybook{AllowedTools: hostTools},
		Memory:   &BriefMemory{MCPURL: "http://host.docker.internal:8888/mcp/podium/", APIKeyEnv: "K"},
	}
	menu := []DelegablePlaybook{{Name: "podium", Summary: "develops Podium itself", Docker: true}}
	c.hostBrief(b, menu)

	assert.Equal(t, RunsOnHost, b.RunsOn)
	require.NotNil(t, b.Memory)
	assert.Equal(t, "http://127.0.0.1:8888/mcp/podium/", b.Memory.MCPURL,
		"host.docker.internal resolves in a container and nowhere else")
	assert.Equal(t, "K", b.Memory.APIKeyEnv)

	// The short tool list is only defensible because the work goes to a container, so the
	// menu that makes that possible has to be in the brief.
	require.NotNil(t, b.Delegation, "an assistant with nowhere to delegate is one that can only talk")
	assert.Equal(t, "http://127.0.0.1:8090", b.Delegation.URL)
	assert.Equal(t, TurnTokenEnv, b.Delegation.TokenEnv)
	assert.Equal(t, menu, b.Delegation.Playbooks)
}

func TestHostBriefOffersNoDelegationWhenThereIsNowhereToSendIt(t *testing.T) {
	c := &Conductor{host: &HostRuntime{}}
	b := &Brief{Playbook: BriefPlaybook{AllowedTools: hostTools}}
	c.hostBrief(b, []DelegablePlaybook{{Name: "podium"}})
	assert.Nil(t, b.Delegation, "no address to reach the conductor at is no delegation")

	c = &Conductor{host: &HostRuntime{TurnURL: "http://127.0.0.1:8090"}}
	b = &Brief{Playbook: BriefPlaybook{AllowedTools: hostTools}}
	c.hostBrief(b, nil)
	assert.Nil(t, b.Delegation, "an empty menu is not a menu")
}

func TestHostBriefDropsMemoryThisHostCannotReach(t *testing.T) {
	c := &Conductor{host: &HostRuntime{}}
	b := &Brief{Memory: &BriefMemory{MCPURL: "http://host.docker.internal:8888/", APIKeyEnv: "K"}}
	c.hostBrief(b, nil)
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

// TestTheAssistantsBriefOmitsATurnCapNobodySet. Zero means no cap, and the document has to
// say that by leaving the field out: the runtime's schema refuses a non-positive number, so
// emitting 0 would fail every turn of a profile that set no ceiling.
func TestTheAssistantsBriefOmitsATurnCapNobodySet(t *testing.T) {
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{
		Name: "podium", DisplayName: "Podium", SystemPrompt: "be Podium",
		Model: "claude-opus-5", DefaultPlaybook: "coder",
		Playbooks: map[string]profiles.Playbook{"coder": {Name: "coder", MaxTurns: 200}},
	})}
	profile := c.profiles.Current()
	j := assistantJob(profile.Assistant())
	require.Zero(t, j.maxTurns)

	b := c.brief(store.Session{ID: "sess_1"}, j, "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil,
		j.choose(profile, profiles.Override{}))
	encoded, err := b.Encode()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "max_turns",
		"an absent cap is absent from the document, not a zero the schema would refuse")
}
