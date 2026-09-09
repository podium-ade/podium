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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// hostFakeEnv turns this binary into a host turn's runtime. hostFakeExitEnv is the code it
// exits with, and hostCanaryEnv is a variable the test sets in ITS OWN environment to prove
// the child does not inherit one.
// They are deliberately NOT named PODIUM_*: deploy/env_test.go holds every PODIUM_ name in
// the tree to being either documented in .env.example or listed as something an operator
// never sets, and a test's own knobs have no business in either list.
const (
	hostFakeEnv       = "CONDUCTOR_TEST_FAKE_RUNTIME"
	hostFakeExitEnv   = "CONDUCTOR_TEST_FAKE_EXIT"
	hostCanaryEnv     = "CONDUCTOR_TEST_LEAK_CANARY"
	hostFakeSilentEnv = "CONDUCTOR_TEST_FAKE_SILENT"
	hostFakeHangEnv   = "CONDUCTOR_TEST_FAKE_HANG"
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

	// The delegating turn: it talks to TurnService over HTTP and reports what happened,
	// which is what delegate_integration_test.go asserts on.
	if os.Getenv(hostFakeDelegateEnv) != "" {
		var asMap map[string]any
		if err := json.Unmarshal(raw, &asMap); err != nil {
			fmt.Fprintf(os.Stderr, "fake runtime: %v\n", err)
			os.Exit(4)
		}
		answer, err := json.Marshal(hostFakeDelegate(asMap))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake runtime: %v\n", err)
			os.Exit(4)
		}
		hostSay(sock, "final", string(answer))
		hostSay(sock, "accounting", `{"num_turns":2,"total_cost_usd":0.001}`)
		os.Exit(0)
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

	// A runtime that says nothing at all, which is the case where the placeholder above it
	// would otherwise read "working on it" for ever.
	if os.Getenv(hostFakeSilentEnv) != "" {
		os.Exit(0)
	}

	// A turn that is still working when somebody stops it. SIGTERM is what the conductor
	// sends, and the real runtime forwards it to the harness and still reports — so this
	// does the same, and the turn has an answer to relay even though it was cancelled.
	if os.Getenv(hostFakeHangEnv) != "" {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		hostSay(sock, "progress", "still working")
		select {
		case <-term:
			hostSay(sock, "final", "cancelled before finishing")
			os.Exit(0)
		case <-time.After(60 * time.Second):
			fmt.Fprintln(os.Stderr, "fake runtime: never told to stop")
			os.Exit(4)
		}
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

	// The assistant: the short tool list, and no repository to point a tool at.
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

	// And the session records NO playbook, because the assistant is not one. The playbooks
	// this turn could have delegated to are credited on their own rows, per task.
	sess, err := st.GetSessionByKey(context.Background(), ev.SourceKey)
	require.NoError(t, err)
	assert.Empty(t, sess.Playbook, "a conversation runs the assistant, not a playbook")
	require.NotNil(t, turn.NumTurns)
	assert.Equal(t, 4, *turn.NumTurns)
	require.NotNil(t, turn.CostUSD)
	assert.InDelta(t, 0.0125, *turn.CostUSD, 1e-9)
	assert.Equal(t, finals[0].Text, turn.FinalText)

	// And the jail is gone, so a turn leaves nothing on the host it ran on.
	_, err = os.Stat(filepath.Dir(report.Home))
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

func TestAHostTurnThatSaysNothingStillLeavesASentence(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ev := inbound("C1/4.1", "say nothing")
	ev.Env = hostEnv(map[string]string{hostFakeSilentEnv: "1"})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the silent turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	// The exit code said the turn was fine and the runtime never spoke. Without a sentence
	// here the chat is left showing the placeholder — "working on it" — for ever.
	records := src.Records()
	assert.Empty(t, posts(records, conductor.OutFinal))
	failures := posts(records, conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "without saying anything")
}

func TestAHostTurnCanBeStoppedMidAnswer(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ev := inbound("C1/5.1", "take your time")
	ev.Env = hostEnv(map[string]string{hostFakeHangEnv: "1"})
	require.NoError(t, src.Send(context.Background(), ev))

	// Wait until it is actually working, so the cancel lands mid-turn and not before the
	// child was forked.
	waitFor(t, 30*time.Second, "the turn to be working", func() bool {
		for _, rec := range src.Records() {
			if rec.Action == fakesource.ActionEdit && strings.Contains(rec.Text, "still working") {
				return true
			}
		}
		return false
	})

	assert.True(t, r.cond.CancelHostTurn(ev.Ref), "there is a host turn to cancel")

	waitFor(t, 30*time.Second, "the cancelled turn to be recorded", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnCancelled
	})

	finals := posts(src.Records(), conductor.OutFinal)
	require.Len(t, finals, 1, "a cancelled turn still says what it managed")
	assert.Equal(t, "cancelled before finishing", finals[0].Text)

	turn := turnOf(t, st, ev.SourceKey)
	assert.Equal(t, "cancelled before finishing", turn.FinalText)
	assert.Empty(t, turn.TaskID)
}

func TestCancellingAHostTurnThatIsNotRunningSaysSo(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })
	assert.False(t, r.cond.CancelHostTurn("C1/nothing-here"),
		"nothing to cancel is an answer, not an error")
}

// A host turn dies with the process running it, so the conductor stopping mid-turn is the
// case that has to write the row: there is no task for the recovery pass to resume from, and
// the context the turn is unwinding on can no longer write anything — which is why recording
// it happens on a context of its own. Without that, this row says "running" until a restart
// finds it and calls it an orphan.
func TestTheConductorStoppingMidHostTurnStillRecordsIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ev := inbound("C1/6.1", "keep going")
	ev.Env = hostEnv(map[string]string{hostFakeHangEnv: "1"})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to be working", func() bool {
		for _, rec := range src.Records() {
			if rec.Action == fakesource.ActionEdit && strings.Contains(rec.Text, "still working") {
				return true
			}
		}
		return false
	})

	// The closest a test gets to killing the process.
	r.stop()

	assert.Equal(t, store.TurnCancelled, turnStatus(st, ev.SourceKey),
		"a host turn cannot be resumed, so the turn it was must be written down as it ends")
	turn := turnOf(t, st, ev.SourceKey)
	assert.Equal(t, "cancelled before finishing", turn.FinalText)
	assert.Empty(t, turn.TaskID)
}

// TestAnAssistantTurnThatRunsOutOfTimeIsStopped. It is the only automatic stop an assistant
// turn has: no container, no node, and no step cap unless profile.yaml asks for one. So the
// clock has to actually fire, kill the runtime, and leave the conversation told why.
func TestAnAssistantTurnThatRunsOutOfTimeIsStopped(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	// A profile whose assistant gets two seconds, and a runtime that will not finish.
	profile := testProfile(t)
	profile.Timeout = spec.Duration(2 * time.Second)
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Host = hostRuntime(t, "sk-test")
		o.Profiles = profiles.NewLive(profile)
	})

	ev := inbound("C1/18.1", "wait for ever")
	// The hang mode reports on SIGTERM and never exits on its own, which is what a stuck
	// turn looks like from here.
	ev.Env = hostEnv(map[string]string{hostFakeHangEnv: "1"})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 60*time.Second, "the turn to be stopped by its clock", func() bool {
		return turnStatus(st, ev.SourceKey) != "" && turnStatus(st, ev.SourceKey) != store.TurnRunning
	})

	// Failed, not cancelled: nobody asked for this turn to end.
	assert.Equal(t, store.TurnFailed, turnStatus(st, ev.SourceKey))
	// It still said what it managed — the runtime gets SIGTERM and hostGrace, exactly as a
	// cancelled turn does — so the clock is a stop and not a gag.
	joined := strings.Join(textsOf(src.Records()), "\n")
	assert.Contains(t, joined, "still working", "a stopped turn still relays what it had")
	// And no task was created: the assistant's clock bounds the assistant.
	assert.Empty(t, fake.Specs())
}

// textsOf is every record's text, for asserting on what a conversation was told.
func textsOf(records []fakesource.Record) []string {
	out := make([]string, 0, len(records))
	for _, rec := range records {
		out = append(out, rec.Text)
	}
	return out
}

// The change a Slack thread exists for. With a host runtime, a mention is a CONVERSATION
// answered in this process — so it creates no task, and its session is pinned to no
// playbook, which is what lets the assistant delegate the work to any of them instead of the
// channel's routing picking one up front.
//
// The turn itself fails here, because only the dev source may drive the fake runtime. That
// is not what this is about: where the turn WENT is.
func TestASlackThreadIsAnsweredHereAndPinnedToNoPlaybook(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	ctx := context.Background()
	ev := threadInbound("C1/1.1", "what does this repo do?")
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "the turn to reach a terminal status", func() bool {
		s := turnStatus(st, ev.SourceKey)
		return s != "" && s != store.TurnRunning
	})

	assert.Empty(t, fake.Specs(), "a mention is answered on this host now, not run as a task")

	sess, err := st.GetSessionByKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Empty(t, sess.Playbook,
		"a thread used to be pinned to the playbook its channel routed to, and that is the "+
			"whole reason a mention could never reach any other one")
}

// And with no host runtime it still runs a playbook as a task. A conductor whose host has no
// runtime has nothing to answer with, and answering on a worker beats refusing.
func TestASlackThreadStillRunsAPlaybookWithNoHostRuntime(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	start(t, st, fake, src)

	ctx := context.Background()
	ev := threadInbound("C1/1.1", "what does this repo do?")
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	sess, err := st.GetSessionByKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "general", sess.Playbook, "the profile's default, as it always was")
}
