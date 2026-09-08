//go:build integration

package conductor_test

// A host turn, end to end, with no opencode and no container anywhere near it.
//
// The runtime is THIS TEST BINARY, re-executed: hostFakeEnv makes TestMain hand the process
// to hostFakeRuntime instead of running tests, and that function speaks the runner's own
// newline-JSON protocol back at the conductor exactly as `podium-runner message` does — one
// connection per message, because that is what the real runtime's one-process-per-message
// invoke produces and it is the reason the listener accepts more than once.
//
// What it can prove without a model: the brief that was fenced, the environment the child
// was given (and the environment it was NOT given), the relay, the accounting, the turn row
// with no task on it, and that no task was created at all.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// hostFakeEnv turns this binary into a host turn's runtime. hostFakeExitEnv is the code it
// exits with, and hostCanaryEnv is a variable the test sets in ITS OWN environment to prove
// the child does not inherit one.
// They are deliberately NOT named PODIUM_*: deploy/env_test.go holds every PODIUM_ name in
// the tree to being either documented in .env.example or listed as something an operator
// never sets, and a test's own knobs have no business in either list.
const (
	hostFakeEnv     = "CONDUCTOR_TEST_FAKE_RUNTIME"
	hostFakeExitEnv = "CONDUCTOR_TEST_FAKE_EXIT"
	hostCanaryEnv   = "CONDUCTOR_TEST_LEAK_CANARY"
)

// hostReport is what the fake runtime says as its answer: everything about the turn it was
// handed that the test wants to assert on. It travels as the final's text, because a final
// is the one thing a turn is guaranteed to be able to say.
type hostReport struct {
	Home        string   `json:"home"`
	Cwd         string   `json:"cwd"`
	Tmp         string   `json:"tmp"`
	Runner      string   `json:"runner"`
	Key         string   `json:"key"`
	KeyEnv      string   `json:"key_env"`
	Canary      string   `json:"canary"`
	Instruction string   `json:"instruction"`
	Tools       []string `json:"tools"`
	Repos       int      `json:"repos"`
	Memory      string   `json:"memory"`
}

// hostFakeRuntime is the child process. It is deliberately written against the same
// contract the TypeScript runtime honours: decode PODIUM_AGENT_TURN, say progress, say the
// answer, then report the accounting, then exit with the code that says how it went.
func hostFakeRuntime() {
	sock := os.Getenv("PODIUM_EVENTS_SOCK")
	raw, err := base64.StdEncoding.DecodeString(os.Getenv("PODIUM_AGENT_TURN"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: undecodable brief: %v\n", err)
		os.Exit(2)
	}
	var brief conductor.Brief
	if err := json.Unmarshal(raw, &brief); err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: unparseable brief: %v\n", err)
		os.Exit(2)
	}

	cwd, _ := os.Getwd()
	report := hostReport{
		Home:        os.Getenv("HOME"),
		Cwd:         cwd,
		Tmp:         os.Getenv("TMPDIR"),
		Runner:      os.Getenv("PODIUM_RUNNER_PATH"),
		KeyEnv:      brief.Provider.APIKeyEnv,
		Key:         os.Getenv(brief.Provider.APIKeyEnv),
		Canary:      os.Getenv(hostCanaryEnv),
		Instruction: brief.Instruction,
		Tools:       brief.Playbook.AllowedTools,
		Repos:       len(brief.Repos),
	}
	if brief.Memory != nil {
		report.Memory = brief.Memory.MCPURL
	}
	answer, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: %v\n", err)
		os.Exit(4)
	}

	// One dial per message, as the real one does.
	hostSay(sock, "progress", "reading the handler")
	hostSay(sock, "final", string(answer))
	hostSay(sock, "accounting", `{"num_turns":4,"total_cost_usd":0.0125}`)

	code := 0
	if v := os.Getenv(hostFakeExitEnv); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &code); err != nil {
			code = 4
		}
	}
	os.Exit(code)
}

// hostSay writes one message event and closes the connection, which is what one
// `podium-runner message` invocation looks like on the wire.
func hostSay(sock, typ, text string) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: dial %s: %v\n", sock, err)
		return
	}
	defer func() { _ = conn.Close() }()
	line, err := json.Marshal(map[string]any{
		"v": 1, "kind": "message", "ts": time.Now().UTC().Format(time.RFC3339Nano),
		"type": typ, "text": text,
	})
	if err != nil {
		return
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "fake runtime: write: %v\n", err)
	}
}

// hostRuntime is the HostRuntime that runs this binary as the runtime. The entry is a
// -test.run that matches nothing, so a child that somehow ignored hostFakeEnv would run no
// tests rather than the whole suite again.
func hostRuntime(t *testing.T, key string) *conductor.HostRuntime {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return &conductor.HostRuntime{
		Node:     self,
		Entry:    "-test.run=TestNoSuchTestExists",
		Runner:   "/usr/bin/true",
		StateDir: t.TempDir(),
		Credential: func(_ context.Context, provider string) (string, error) {
			return key + "-for-" + provider, nil
		},
	}
}

// hostEnv is the child environment the conductor has to build: the fake-runtime marker, and
// whatever else a case needs. It is set on the DEV SOURCE's event, which is the only source
// whose environment the conductor honours.
func hostEnv(extra map[string]string) map[string]string {
	env := map[string]string{hostFakeEnv: "1"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestAHostTurnAnswersInThisProcessAndCreatesNoTask(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	// The conductor's own environment, which the child must not inherit any of.
	t.Setenv(hostCanaryEnv, "leaked")

	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ev := inbound("C1/1.1", "why does it 500?")
	ev.Env = hostEnv(nil)
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the host turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	// Nothing was asked of the control plane. That is the point of a host turn.
	assert.Empty(t, fake.Specs(), "a host turn creates no task")

	records := src.Records()
	assert.Equal(t, []string{string(conductor.ReactionWorking), string(conductor.ReactionDone)},
		reactions(records))

	var edits []fakesource.Record
	for _, rec := range records {
		if rec.Action == fakesource.ActionEdit {
			edits = append(edits, rec)
		}
	}
	require.Len(t, edits, 1, "the progress replaces the placeholder rather than adding a message")
	assert.Equal(t, "⏳ reading the handler", edits[0].Text)

	finals := posts(records, conductor.OutFinal)
	require.Len(t, finals, 1)
	assert.Empty(t, posts(records, conductor.OutFailure), "a turn that worked apologises for nothing")

	var report hostReport
	require.NoError(t, json.Unmarshal([]byte(finals[0].Text), &report))

	// The credential: the same one a task's secret would carry, resolved for the provider
	// the turn's backend spends.
	assert.Equal(t, "ANTHROPIC_API_KEY", report.KeyEnv)
	assert.Equal(t, "sk-test-for-anthropic", report.Key)
	assert.Equal(t, "/usr/bin/true", report.Runner)
	assert.Equal(t, "why does it 500?", report.Instruction)

	// The fence: the short tool list, and no repository to point a tool at.
	assert.Equal(t, []string{"webfetch", "todoread", "todowrite"}, report.Tools)
	assert.Zero(t, report.Repos)

	// The jail: a HOME of its own, not the operator's, and a working directory beside it.
	// Which root it is under is hostJail's business and is tested there — a state directory
	// too deep for an AF_UNIX path falls back to the OS temp directory, and t.TempDir() on
	// macOS is exactly that deep.
	assert.NotEqual(t, os.Getenv("HOME"), report.Home)
	assert.True(t, strings.HasSuffix(report.Home, "/home"), report.Home)
	assert.True(t, strings.HasSuffix(report.Cwd, "/work"), report.Cwd)
	// The same jail for both, compared by its directory name: getwd resolves symlinks and
	// the environment does not, so on macOS the two spell /var and /private/var.
	assert.Equal(t, filepath.Base(filepath.Dir(report.Home)), filepath.Base(filepath.Dir(report.Cwd)),
		"one jail per turn")

	// The environment was built from empty: this process's own variables are not in it.
	assert.Empty(t, report.Canary, "the child must not inherit the conductor's environment")

	// The turn row: the runtime's accounting, and NO TASK — which is what the recovery pass
	// reads to mean a turn that died with the process running it.
	turn := turnOf(t, st, ev.SourceKey)
	assert.Empty(t, turn.TaskID, "a host turn has no task")
	require.NotNil(t, turn.NumTurns)
	assert.Equal(t, 4, *turn.NumTurns)
	require.NotNil(t, turn.CostUSD)
	assert.InDelta(t, 0.0125, *turn.CostUSD, 1e-9)
	assert.Equal(t, finals[0].Text, turn.FinalText)

	// And the jail is gone, so a turn leaves nothing on the host it ran on.
	_, err := os.Stat(filepath.Dir(report.Home))
	assert.True(t, os.IsNotExist(err), "the turn's directory is removed when it ends")
}

func TestAHostTurnThatFailsSaysSoAndRecordsIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ev := inbound("C1/2.1", "break it")
	// Exit 4 is the runtime's "the harness or the API failed". It still said its piece
	// first, so the conductor must relay that and add nothing of its own.
	ev.Env = hostEnv(map[string]string{hostFakeExitEnv: "4"})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the host turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})

	records := src.Records()
	assert.Equal(t, []string{string(conductor.ReactionWorking), string(conductor.ReactionFailed)},
		reactions(records))
	assert.Len(t, posts(records, conductor.OutFinal), 1, "the answer it managed is still relayed")
	assert.Empty(t, posts(records, conductor.OutFailure),
		"a runtime that spoke for itself is not apologised for twice")
}

func TestAHostTurnWithNoCredentialNeverStarts(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "")
	host.Credential = func(_ context.Context, _ string) (string, error) { return "", nil }
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = host })

	ev := inbound("C1/3.1", "answer me")
	ev.Env = hostEnv(nil)
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the host turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})

	failures := posts(src.Records(), conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "no model credential")
	assert.Empty(t, fake.Specs(), "nothing ran")
}
