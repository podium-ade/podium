//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/artifacts/fakes3"
)

// agentToken is the bearer podium-server would present on a proxied AgentService call, and
// the only thing guarding the conductor's API. A test fixture, not a secret.
const agentToken = "agenttoken-e2e"

// anthropicKeySecret is the reserved secret the conductor attaches to every turn. A dry run
// never reads it, but it has to EXIST or admission refuses the task — which is itself worth
// proving, because it is the first thing an operator gets wrong.
const anthropicKeySecret = "podium.agent.anthropic_api_key"

// ---------------------------------------------------------------------------
// the conductor as a subprocess
// ---------------------------------------------------------------------------

// agentProc is a podium-agent subprocess with the dev source enabled.
type agentProc struct {
	t           *testing.T
	h           *harness
	addr        string
	databaseURL string
	profileDir  string

	mu  sync.Mutex
	cmd *exec.Cmd
	log *bytes.Buffer
}

// startAgent brings up the conductor against the harness's control plane.
func startAgent(t *testing.T, h *harness) *agentProc {
	t.Helper()
	root, err := repoRoot()
	require.NoError(t, err)

	a := &agentProc{
		t:           t,
		h:           h,
		addr:        freeLoopbackAddr(t),
		databaseURL: newAgentDatabase(t),
		// The example profile is what docs/agent.md points at, and its skill's image is the
		// locally built :dev tag, so the e2e node can run it without a registry.
		profileDir: filepath.Join(root, "examples", "agent"),
	}
	a.start()
	t.Cleanup(a.stop)
	a.awaitReady()
	return a
}

func (a *agentProc) start() {
	a.t.Helper()
	cmd := exec.CommandContext(context.Background(), filepath.Join(binDir, "podium-agent")) //nolint:gosec // this test's own build output
	cmd.Env = append(os.Environ(),
		"PODIUM_AGENT_SERVER="+a.h.url(),
		"PODIUM_AGENT_API_TOKEN="+devToken,
		"PODIUM_AGENT_DATABASE_URL="+a.databaseURL,
		"PODIUM_AGENT_LISTEN="+a.addr,
		"PODIUM_AGENT_TOKEN="+agentToken,
		"PODIUM_AGENT_PROFILE_DIR="+a.profileDir,
		"PODIUM_AGENT_DEV_SOURCE=true",
	)
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	require.NoError(a.t, cmd.Start())

	a.mu.Lock()
	a.cmd = cmd
	a.log = buf
	a.mu.Unlock()
}

// stop sends SIGTERM, which must leave a running turn's task alone: the conductor is a
// client of the control plane, not the thing running the container.
func (a *agentProc) stop() {
	a.mu.Lock()
	cmd := a.cmd
	a.cmd = nil
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// kill is SIGKILL: the closest a test gets to the machine going away mid-turn.
func (a *agentProc) kill() {
	a.mu.Lock()
	cmd := a.cmd
	a.cmd = nil
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// restart brings the conductor back on the same database, which is what makes its recovery
// pass run against real in-flight turns.
func (a *agentProc) restart() {
	a.t.Helper()
	a.start()
	a.awaitReady()
}

func (a *agentProc) logs() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.log == nil {
		return ""
	}
	return a.log.String()
}

func (a *agentProc) url() string { return "http://" + a.addr }

func (a *agentProc) awaitReady() {
	a.t.Helper()
	waitFor(a.t, 60*time.Second, "the conductor to report ready", func() bool {
		code, _ := a.get("/readyz", "")
		return code == http.StatusOK
	}, func() string { return "agent log:\n" + a.logs() })
}

// get is a plain GET against the conductor, with an optional bearer.
func (a *agentProc) get(path, token string) (int, string) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url()+path, nil)
	require.NoError(a.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// post is a plain POST against the conductor, with an optional bearer.
func (a *agentProc) post(path, token, body string) (int, string) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url()+path, strings.NewReader(body))
	require.NoError(a.t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

// devRecord is one thing the conductor said, as GET /dev/outbound reports it.
type devRecord struct {
	Seq       int64  `json:"seq"`
	Action    string `json:"action"`
	Ref       string `json:"ref"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	MessageID string `json:"message_id"`
	Name      string `json:"name"`
	Reaction  string `json:"reaction"`
}

// send injects one inbound message and returns the source key of its session.
func (a *agentProc) send(body string) string {
	a.t.Helper()
	code, out := a.post("/dev/inbound", agentToken, body)
	require.Equal(a.t, http.StatusAccepted, code, "POST /dev/inbound: %s", out)
	var accepted struct {
		SourceKey string `json:"source_key"`
	}
	require.NoError(a.t, json.Unmarshal([]byte(out), &accepted))
	require.NotEmpty(a.t, accepted.SourceKey)
	return accepted.SourceKey
}

// outbound is everything the conductor has said so far, in order.
func (a *agentProc) outbound() []devRecord {
	a.t.Helper()
	code, out := a.get("/dev/outbound", agentToken)
	require.Equal(a.t, http.StatusOK, code, "GET /dev/outbound: %s", out)
	var body struct {
		Records []devRecord `json:"records"`
	}
	require.NoError(a.t, json.Unmarshal([]byte(out), &body))
	return body.Records
}

// ---------------------------------------------------------------------------
// the conductor's database, read directly
// ---------------------------------------------------------------------------

type turnRow struct {
	ID        string
	TaskID    string
	Status    string
	FinalText string
	NumTurns  *int
	CostUSD   *float64
}

// turnRows reads a session's turns straight out of podium_agent. Asserting through the
// database rather than the API is deliberate: the API is one read path, and the row is the
// thing every later step builds on.
func turnRows(t *testing.T, databaseURL, sourceKey string) []turnRow {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `select t.id, coalesce(t.task_id, ''), t.status,
		coalesce(t.final_text, ''), t.num_turns, t.cost_usd
		from turns t join sessions s on s.id = t.session_id
		where s.source_key = $1 order by t.started_at, t.id`, sourceKey)
	require.NoError(t, err)
	defer rows.Close()

	var out []turnRow
	for rows.Next() {
		var r turnRow
		var turns *int32
		require.NoError(t, rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.FinalText, &turns, &r.CostUSD))
		if turns != nil {
			n := int(*turns)
			r.NumTurns = &n
		}
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// relayedSeqs is every (task_id, seq) the conductor has claimed for one task.
func relayedSeqs(t *testing.T, databaseURL, taskID string) []int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, "select seq from relayed where task_id = $1 order by seq", taskID)
	require.NoError(t, err)
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var seq int64
		require.NoError(t, rows.Scan(&seq))
		out = append(out, seq)
	}
	require.NoError(t, rows.Err())
	return out
}

// ---------------------------------------------------------------------------
// helpers over the outbound record list
// ---------------------------------------------------------------------------

func recordsOfType(records []devRecord, action, kind string) []devRecord {
	var out []devRecord
	for _, r := range records {
		if r.Action == action && (kind == "" || r.Type == kind) {
			out = append(out, r)
		}
	}
	return out
}

func reactionsOf(records []devRecord) []string {
	var out []string
	for _, r := range records {
		if r.Action == "react" {
			out = append(out, r.Reaction)
		}
	}
	return out
}

func hasFinal(records []devRecord, text string) bool {
	for _, r := range recordsOfType(records, "post", "final") {
		if r.Text == text {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// TestAgentTurnRoundTrip
// ---------------------------------------------------------------------------

// TestAgentTurnRoundTrip is story one with Slack replaced by the dev source: a message
// arrives, one Podium task runs the real agent runtime image on a real node, and what the
// runtime says comes back into the conversation. It is the gate for steps 18, 19 and 20.
func TestAgentTurnRoundTrip(t *testing.T) {
	requireAgentRuntimeImage(t)

	// The turn's own accounting (turn.json) reaches the conductor as an artifact, so this
	// test needs an object store. Everything else about the loop works without one.
	fake := fakes3.Start(t)
	h := newHarness(t, func(c *server.Config) { c.S3 = fake.Config() })
	startNode(t, h)
	// The reserved secret must exist before a turn can be admitted. A dry run never reads
	// it; the spec still names it, and admission checks that a named secret exists.
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")

	agent := startAgent(t, h)
	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"hello there","dry_run":true}`)

	// A task appears, and it is the one the conductor made.
	var taskID string
	waitFor(t, 2*time.Minute, "the conductor's task to appear", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		taskID = rows[0].TaskID
		return strings.Contains(h.podiumOK("tasks"), taskID)
	}, func() string { return "agent log:\n" + agent.logs() })
	t.Logf("turn task: %s", taskID)

	waitFor(t, 3*time.Minute, "the turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\noutbound:\n%+v\npodium task:\n%s",
			agent.logs(), agent.outbound(), h.podiumOK("task", "get", taskID))
	})

	records := agent.outbound()
	t.Logf("outbound records:\n%+v", records)

	// 👀 first, ✅ last, and nothing in between.
	assert.Equal(t, []string{"working", "done"}, reactionsOf(records))

	// The placeholder goes up before the work starts.
	require.NotEmpty(t, records)
	assert.Equal(t, "react", records[0].Action)
	require.Greater(t, len(records), 1)
	assert.Equal(t, "post", records[1].Action)
	assert.Equal(t, "👀 working…", records[1].Text)

	// And the runtime's dry-run answer came back verbatim.
	require.True(t, hasFinal(records, "dry run: hello there"),
		"the dry run's final must reach the conversation: %+v", records)
	assert.Len(t, recordsOfType(records, "post", "final"), 1, "one answer, posted once")
	assert.Empty(t, recordsOfType(records, "post", "failure"), "a turn that worked apologises for nothing")

	// The turn row is the record every later step reads, including the runtime's own
	// accounting out of turn.json.
	rows := turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1)
	assert.Equal(t, "succeeded", rows[0].Status)
	assert.Equal(t, "dry run: hello there", rows[0].FinalText)
	require.NotNil(t, rows[0].NumTurns, "turn.json must have been read")
	assert.Equal(t, 0, *rows[0].NumTurns, "a dry run spends no turns")
	require.NotNil(t, rows[0].CostUSD)
	assert.Zero(t, *rows[0].CostUSD, "a dry run spends nothing")

	// Exactly one thing was said, so exactly one seq was claimed.
	assert.Equal(t, []int64{taskIDSeqOf(t, agent.databaseURL, taskID)}, relayedSeqs(t, agent.databaseURL, taskID),
		"one final message is one relayed row")

	// The conductor's API is behind the bearer, and it reports the session it just recorded.
	code, body := agent.post("/podium.agent.v1.AgentService/ListSessions", "", "{}")
	assert.Equal(t, http.StatusUnauthorized, code, "the AgentService must refuse an unauthenticated call: %s", body)
	code, body = agent.post("/podium.agent.v1.AgentService/ListSessions", "wrong-token", "{}")
	assert.Equal(t, http.StatusUnauthorized, code, "and a wrong bearer: %s", body)

	code, body = agent.post("/podium.agent.v1.AgentService/ListSessions", agentToken, "{}")
	require.Equal(t, http.StatusOK, code, "ListSessions with the bearer: %s", body)
	assert.Contains(t, body, sourceKey)
	assert.Contains(t, body, `"skill":"general"`)

	// /healthz is open and says nothing about dependencies; /readyz needs them.
	code, _ = agent.get("/healthz", "")
	assert.Equal(t, http.StatusOK, code)
	code, _ = agent.get("/metrics", "")
	assert.Equal(t, http.StatusOK, code)

	requireNoPodiumResources(t)
}

// taskIDSeqOf is the single seq the round-trip test expects to have been relayed. It is read
// back rather than hard-coded because the runner numbers a task's events, not this test.
func taskIDSeqOf(t *testing.T, databaseURL, taskID string) int64 {
	t.Helper()
	seqs := relayedSeqs(t, databaseURL, taskID)
	require.Len(t, seqs, 1, "exactly one message was relayed for %s", taskID)
	return seqs[0]
}

// TestAgentReadyzFailsWhenPodiumIsDown is the other half of the health contract: /healthz is
// about this process and /readyz is about what it needs.
func TestAgentReadyzFailsWhenPodiumIsDown(t *testing.T) {
	h := newHarness(t)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	agent := startAgent(t, h)

	code, body := agent.get("/readyz", "")
	require.Equal(t, http.StatusOK, code, body)

	h.stopServer()
	waitFor(t, 60*time.Second, "readyz to report the Podium API is unreachable", func() bool {
		code, _ := agent.get("/readyz", "")
		return code == http.StatusServiceUnavailable
	}, func() string { return "agent log:\n" + agent.logs() })

	// /healthz never lied about the process being up.
	code, _ = agent.get("/healthz", "")
	assert.Equal(t, http.StatusOK, code)

	h.startServer()
	waitFor(t, 60*time.Second, "readyz to recover", func() bool {
		code, _ := agent.get("/readyz", "")
		return code == http.StatusOK
	})
}

// TestAgentSecondMessageWaitsForTheRunningTurn is the one-turn-per-session rule against a
// real task: the second message does not start a second task while the first is in flight,
// and when its turn comes the brief has both human messages in it.
func TestAgentSecondMessageWaitsForTheRunningTurn(t *testing.T) {
	requireAgentRuntimeImage(t)

	h := newHarness(t)
	startNode(t, h)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	agent := startAgent(t, h)

	// The first turn sleeps, so the second message lands while it is still running.
	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"first question","dry_run":true,"dry_run_sleep_ms":8000}`)

	waitFor(t, 2*time.Minute, "the first task to be running", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		_, out, _, err := runCLI(t, h.url(), 30*time.Second, "task", "get", rows[0].TaskID, "--json")
		return err == nil && strings.Contains(out, "TASK_STATUS_RUNNING")
	}, func() string { return "agent log:\n" + agent.logs() })

	agent.send(`{"channel":"C1","thread":"1.1","author":"alice","text":"actually, also this","dry_run":true}`)

	// While the first turn runs there is exactly one turn and one task.
	rows := turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1, "a second turn must not start while one is running")

	waitFor(t, 4*time.Minute, "both turns to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 2 {
			return false
		}
		return rows[0].Status == "succeeded" && rows[1].Status == "succeeded"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\nturns: %+v", agent.logs(), turnRows(t, agent.databaseURL, sourceKey))
	})

	rows = turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 2, "two messages, two turns")
	assert.NotEqual(t, rows[0].TaskID, rows[1].TaskID, "two tasks")
	assert.Equal(t, "dry run: first question", rows[0].FinalText)

	// The second turn's answer proves what its brief said: the runtime echoes the
	// instruction, and the instruction is the latest human message.
	assert.Equal(t, "dry run: actually, also this", rows[1].FinalText)

	// And the second turn's brief carried both human messages, so nothing said during a
	// running turn was lost.
	brief := briefOf(t, h, rows[1].TaskID)
	var texts []string
	for _, entry := range brief.Transcript {
		texts = append(texts, entry.Text)
	}
	assert.Contains(t, texts, "first question")
	assert.Contains(t, texts, "actually, also this")
	assert.Equal(t, "actually, also this", brief.Instruction)

	requireNoPodiumResources(t)
}

// TestAgentSurvivesARestartMidTurn kills the conductor while a turn is in flight and brings
// it back. The task keeps running on the node — the conductor is not what runs it — and the
// answer is posted exactly once, which the relayed table proves.
func TestAgentSurvivesARestartMidTurn(t *testing.T) {
	requireAgentRuntimeImage(t)

	h := newHarness(t)
	startNode(t, h)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	agent := startAgent(t, h)

	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"take your time","dry_run":true,"dry_run_sleep_ms":10000}`)

	var taskID string
	waitFor(t, 2*time.Minute, "the task to be running", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		taskID = rows[0].TaskID
		_, out, _, err := runCLI(t, h.url(), 30*time.Second, "task", "get", taskID, "--json")
		return err == nil && strings.Contains(out, "TASK_STATUS_RUNNING")
	}, func() string { return "agent log:\n" + agent.logs() })

	// SIGKILL, before the runtime has said anything. The turn stays running in the
	// conductor's database with no finished_at.
	agent.kill()
	rows := turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1)
	require.Equal(t, "running", rows[0].Status, "the turn was in flight when the process died")
	require.Empty(t, relayedSeqs(t, agent.databaseURL, taskID), "nothing had been said yet")

	// The dev source's records die with the process, so what the restarted conductor says
	// is all that is left — which is exactly what makes "exactly once" observable.
	agent.restart()
	waitFor(t, 3*time.Minute, "the resumed turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\nturns: %+v", agent.logs(), turnRows(t, agent.databaseURL, sourceKey))
	})

	rows = turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1, "a resumed turn is not a second turn")
	assert.Equal(t, taskID, rows[0].TaskID, "and it is the same task")
	assert.Equal(t, "dry run: take your time", rows[0].FinalText,
		"the answer was relayed after the restart")

	// One thing was said, and the ledger holds exactly one row for it.
	assert.Len(t, relayedSeqs(t, agent.databaseURL, taskID), 1,
		"the answer must be claimed exactly once across the restart")

	records := agent.outbound()
	t.Logf("outbound after the restart:\n%+v", records)
	assert.Len(t, recordsOfType(records, "post", "final"), 1, "and posted exactly once")
	assert.Contains(t, reactionsOf(records), "done")

	requireNoPodiumResources(t)
}

// TestAgentRelaysAFailureInPlainWords is the failure half of the relay: a turn that ends
// badly says something a human can act on, names the task, and leaks no raw error text.
func TestAgentRelaysAFailureInPlainWords(t *testing.T) {
	requireAgentRuntimeImage(t)

	h := newHarness(t)
	startNode(t, h)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	agent := startAgent(t, h)

	// Exit 3 is the runtime's "I ran out of turns" (step 16).
	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"do the big thing","dry_run":true,"dry_run_exit":3}`)

	waitFor(t, 3*time.Minute, "the turn to fail", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "failed"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\nturns: %+v", agent.logs(), turnRows(t, agent.databaseURL, sourceKey))
	})

	records := agent.outbound()
	t.Logf("outbound records:\n%+v", records)
	failures := recordsOfType(records, "post", "failure")
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "ran out of turns")

	rows := turnRows(t, agent.databaseURL, sourceKey)
	require.Len(t, rows, 1)
	assert.Contains(t, failures[0].Text, rows[0].TaskID, "the task id is how an operator digs in")

	// Nothing a human sees may carry the container's own error text.
	for _, r := range records {
		assert.NotContains(t, r.Text, "exit", "no raw exit text: %q", r.Text)
		assert.NotContains(t, r.Text, "podium-runner", "no internals: %q", r.Text)
	}
	// The dry run still said its piece before exiting 3.
	assert.Len(t, recordsOfType(records, "post", "final"), 1)
	assert.Contains(t, reactionsOf(records), "failed")

	requireNoPodiumResources(t)
}

// ---------------------------------------------------------------------------
// reading a turn's brief back off its task
// ---------------------------------------------------------------------------

// e2eBrief is as much of the turn brief as these tests assert on.
type e2eBrief struct {
	Instruction string `json:"instruction"`
	Transcript  []struct {
		Role   string `json:"role"`
		Author string `json:"author"`
		Text   string `json:"text"`
	} `json:"transcript"`
	TranscriptTruncated bool `json:"transcript_truncated"`
}

// briefOf decodes PODIUM_AGENT_TURN off a task's own spec, which is the only place the brief
// a turn ran with survives.
func briefOf(t *testing.T, h *harness, taskID string) e2eBrief {
	t.Helper()
	out := h.podiumOK("task", "get", taskID, "--json")
	var task struct {
		Spec struct {
			Env map[string]string `json:"env"`
		} `json:"spec"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &task), "task get --json: %s", out)
	encoded := task.Spec.Env["PODIUM_AGENT_TURN"]
	require.NotEmpty(t, encoded, "the spec must carry the brief: %s", out)

	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var brief e2eBrief
	require.NoError(t, json.Unmarshal(raw, &brief), "the brief must parse: %s", raw)
	return brief
}
