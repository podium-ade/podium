package conductor

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
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
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil,
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
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil,
		j.choose(profile, profiles.Override{}))
	encoded, err := b.Encode()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "max_turns",
		"an absent cap is absent from the document, not a zero the schema would refuse")
}

// TestTheAssistantsTurnIsBoundedByAClock. With no step cap by default and no container, a
// wall clock is the only automatic stop an assistant turn has — so it must never be absent.
func TestTheAssistantsTurnIsBoundedByAClock(t *testing.T) {
	t.Run("profile.yaml's own", func(t *testing.T) {
		p := &profiles.Profile{Timeout: spec.Duration(90 * time.Second)}
		assert.Equal(t, 90*time.Second, assistantJob(p.Assistant()).assistantTimeout())
	})

	t.Run("defaulted when the file names none", func(t *testing.T) {
		j := assistantJob((&profiles.Profile{}).Assistant())
		assert.Equal(t, profiles.DefaultAssistantTimeout.Std(), j.assistantTimeout())
		assert.Positive(t, j.assistantTimeout(), "there is no 'off': it is the only bound left")
	})

	// Belt and braces on the one that matters. A job that reached here with no timeout — a
	// zero value from somewhere this test cannot see — must still be bounded rather than
	// run for ever on the conductor's own machine.
	t.Run("a zero timeout still bounds the turn", func(t *testing.T) {
		assert.Equal(t, profiles.DefaultAssistantTimeout.Std(), job{onHost: true}.assistantTimeout())
	})
}

// A playbook's job carries no wall clock of its own: a task is bounded by its own timeout on
// the node, which is a different mechanism in a different process.
func TestAPlaybooksJobCarriesNoAssistantClock(t *testing.T) {
	j := playbookJob(profiles.Playbook{Name: "coder", Timeout: spec.Duration(2 * time.Hour)})
	assert.Zero(t, j.timeout)
}

// The accounting is the last thing a turn says, and it is said microseconds before the
// runtime exits: one dial, one write, one close, and `podium-runner message` returns as soon
// as the bytes are in the socket buffer rather than when this process has read them. So the
// connection carrying it can still be sitting unaccepted in the listener's queue when the
// child is reaped and the link is closed — and a listener that closes then discards it, which
// is a turn recorded with no num_turns and no cost_usd.
//
// The loop is the test: the window is a scheduling one, so a single pass proves nothing. Two
// hundred of them fail within a few iterations against a link that closes its listener
// outright.
func TestClosingTheLinkDeliversWhatTheRuntimeAlreadySent(t *testing.T) {
	// The grace is what close waits out, and this test pays it two hundred times. Shrinking
	// it keeps the run short without weakening the test: the drop this catches is a message
	// discarded outright, which no grace at all would have saved.
	restore := hostDrainGrace
	hostDrainGrace = time.Millisecond
	t.Cleanup(func() { hostDrainGrace = restore })

	// Not t.TempDir(): on macOS that is already too deep for an AF_UNIX path, which is the
	// same reason hostJail has a fallback.
	dir, err := os.MkdirTemp("", "hl")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for i := range 200 {
		func() {
			sock := filepath.Join(dir, fmt.Sprintf("%d.sock", i))
			link, err := listenHost(sock)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			say(t, sock, `{"v":1,"kind":"message","type":"accounting","text":"{}"}`)
			// Exactly what the conductor does the moment cmd.Wait returns.
			link.close()

			var got []hostEvent
			for ev := range link.events {
				got = append(got, ev)
			}
			if len(got) != 1 {
				t.Fatalf("iteration %d: the runtime's last message was dropped: got %d events, want 1", i, len(got))
			}
			if got[0].Type != "accounting" {
				t.Fatalf("iteration %d: got type %q, want accounting", i, got[0].Type)
			}
		}()
	}
}

// say is one `podium-runner message`: dial, write the line, close, and return without
// waiting for anybody to read it.
func say(t *testing.T, sock, line string) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
