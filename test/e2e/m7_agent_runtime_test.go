//go:build e2e

package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/runner"
	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/artifacts/fakes3"
)

// agentRuntimeImage is local and tagged :dev on purpose. The e2e node runs against the
// host's Docker engine, so an image built by `make agent-runtime` is visible to a task
// without a registry in between — which is the whole reason this suite can exercise the
// runtime at all before anything is published to GHCR.
const agentRuntimeImage = "podium-agent-runtime:dev"

// TestAgentRuntimeDryRun is step 16's acceptance path: the agent runtime image submitted as
// an ordinary Podium task, with the model skipped by PODIUM_AGENT_DRY_RUN. It covers the
// three shapes a later step depends on — a turn that works, a brief that does not, and a
// final answer too long for one runner message.
func TestAgentRuntimeDryRun(t *testing.T) {
	requireAgentRuntimeImage(t)

	// Artifacts need an object store: the turn's transcript and summary reach it through
	// the node's auto-collection path, and `podium artifacts` reads them back from there.
	fake := fakes3.Start(t)
	h := newHarness(t, func(c *server.Config) { c.S3 = fake.Config() })
	startNode(t, h)

	// ---------------------------------------------------------------- a turn that works
	code, stdout, stderr := h.podium(agentRun(turnBrief("hello"))...)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.Contains(t, stderr, "→ message (final): dry run: hello",
		"the dry run's final message must reach the CLI:\n%s", stderr)
	taskID := taskIDFrom(t, stderr)

	list := h.podiumOK("artifacts", taskID)
	t.Logf("podium artifacts %s\n%s", taskID, list)
	require.Contains(t, list, "turn.json")
	require.Contains(t, list, "transcript.jsonl")

	summary := turnSummary(t, h, list)
	assert.Equal(t, 0, summary.ExitCode, "turn.json must agree with the container's exit code")
	assert.Equal(t, "dry-run", summary.SDKSessionID)
	assert.Equal(t, 0, summary.NumTurns)
	assert.Zero(t, summary.TotalCostUSD, "a dry run spends nothing")
	assert.NotEmpty(t, summary.SessionID)
	assert.NotEmpty(t, summary.TurnID)

	// ------------------------------------------------------------- a brief that does not
	//
	// A brief the runtime cannot use is still a turn somebody is waiting on, so it exits 2
	// and says why in a final message rather than dying silently.
	bad := turnBrief("hello")
	bad.Version = 2
	code, stdout, stderr = h.podium(agentRun(bad)...)
	require.Equal(t, 2, code, "an invalid brief is exit 2\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.Contains(t, stderr, "→ message (final): turn brief is invalid",
		"an invalid brief must still produce a final message:\n%s", stderr)
	require.Contains(t, stderr, "this runtime speaks version 1")

	// ------------------------------------------------- a final too long for one message
	//
	// The runner refuses text over 32 KiB (step 15), so emit.ts splits on the widest
	// boundary that fits. Three paragraphs of 20 KiB pack into three messages, and the
	// text arrives whole.
	paragraphs := []string{strings.Repeat("a", 20_000), strings.Repeat("b", 20_000), strings.Repeat("c", 20_000)}
	long := turnBrief(strings.Join(paragraphs, "\n\n"))
	code, stdout, stderr = h.podium(agentRun(long)...)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	longID := taskIDFrom(t, stderr)

	rows := taskMessages(t, h.databaseURL, longID)
	require.Len(t, rows, 3, "a 60 KiB final is three messages, split on paragraph boundaries")
	var texts []string
	for _, row := range rows {
		assert.Equal(t, "final", row.Type)
		assert.LessOrEqual(t, len(row.Text), runner.MaxMessageBytes,
			"no chunk may be one the runner would refuse")
		texts = append(texts, row.Text)
	}
	assert.Equal(t, "dry run: "+long.Instruction, strings.Join(texts, "\n\n"),
		"the chunks must reassemble into the whole answer")

	requireNoPodiumResources(t)
}

// TestAgentRuntimeCancelledTurn is the SIGTERM half of the contract. The runner forwards
// the signal, the runtime aborts, says it was cancelled, flushes both artifacts and exits
// **0** — the command ended by itself, so there is no 143 here. `podium task cancel` is
// what makes the task cancelled, from the server side.
func TestAgentRuntimeCancelledTurn(t *testing.T) {
	requireAgentRuntimeImage(t)

	fake := fakes3.Start(t)
	h := newHarness(t, func(c *server.Config) { c.S3 = fake.Config() })
	startNode(t, h)

	brief := turnBrief("take your time")
	taskID := strings.TrimSpace(h.podiumOK("run", "--detach", "--image", agentRuntimeImage,
		"--env", "PODIUM_AGENT_DRY_RUN=1",
		"--env", "PODIUM_AGENT_DRY_RUN_SLEEP_MS=30000",
		"--env", envTurn(brief)))
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)

	// `running` means the runner forked node, not that node has finished booting, and a
	// signal that lands before the handler is installed kills the process outright (143).
	// The runtime's first act after decoding the brief is to say so on stderr, which becomes
	// a log chunk — so that line is the real "the turn is under way" signal. `podium logs`
	// keeps the streams apart, so a stderr chunk comes back on stderr.
	waitFor(t, 60*time.Second, "the runtime to report the turn started", func() bool {
		_, _, logs := h.podium("logs", taskID)
		return strings.Contains(logs, "turn "+brief.TurnID+" of session")
	})

	// The sleep is 30s and the cancel grace is 30s, so a runtime that ignored SIGTERM
	// would be killed rather than exit — which is what the exit code below catches.
	started := time.Now()
	require.Contains(t, h.podiumOK("task", "cancel", taskID, "--reason", "e2e"), taskID)
	task := waitForTaskStatus(t, h, taskID, "TASK_STATUS_CANCELLED", 30*time.Second)
	t.Logf("cancelled after %s with exit code %d", time.Since(started).Round(time.Millisecond), task.ExitCode)
	require.Equal(t, 0, task.ExitCode,
		"a cancelled turn exits 0; 143 would mean the runtime never handled SIGTERM")

	rows := taskMessages(t, h.databaseURL, taskID)
	require.Len(t, rows, 1, "a cancelled turn still says one thing")
	assert.Equal(t, "final", rows[0].Type)
	assert.Equal(t, "cancelled before finishing", rows[0].Text)

	list := h.podiumOK("artifacts", taskID)
	t.Logf("podium artifacts %s\n%s", taskID, list)
	require.Contains(t, list, "turn.json", "a cancelled turn still leaves its summary")
	require.Contains(t, list, "transcript.jsonl")
	assert.Equal(t, 0, turnSummary(t, h, list).ExitCode)

	requireNoPodiumResources(t)
}

// TestBriefScriptMatchesTheGoBrief keeps the acceptance script and this suite honest about
// each other: `examples/agent/brief.sh` is what the index's acceptance block and the README
// tell a reader to run, and the briefs above are what CI actually exercises. If the two ever
// drift, one of them is testing something nobody runs. Needs no node and no Docker.
func TestBriefScriptMatchesTheGoBrief(t *testing.T) {
	root, err := repoRoot()
	require.NoError(t, err)
	script := filepath.Join(root, "examples", "agent", "brief.sh")

	for _, instruction := range []string{
		"hello",
		`quotes " and a backslash \ and <angles> & an ampersand`,
		"two\nlines",
		"unicode: é ✓ 🙂",
	} {
		t.Run(instruction, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, script, instruction) //nolint:gosec // the repository's own script
			cmd.Dir = root
			out, runErr := cmd.Output()
			require.NoError(t, runErr, "brief.sh failed: %s", out)

			want := encodeBrief(turnBrief(instruction))
			require.Equal(t, want, string(out),
				"examples/agent/brief.sh and turnBrief must produce the same bytes")

			// And what they agree on is a brief the runtime accepts.
			raw, decErr := base64.StdEncoding.DecodeString(string(out))
			require.NoError(t, decErr)
			require.True(t, json.Valid(raw), "the brief must be JSON: %s", raw)
		})
	}
}

// ---------------------------------------------------------------------------
// the turn brief
// ---------------------------------------------------------------------------

// agentBrief mirrors agent/runtime/src/brief.ts, which is the shape's single source of
// truth. It is deliberately the *minimal* brief — a chat source, no repos, no memory —
// because that is what examples/agent/brief.sh prints and what a dry run needs. Step 17
// writes the full Go mirror in internal/agent/conductor; this is not it.
//
// The field order is the schema's order and the JSON is compact, because brief.sh's `jq -cn`
// emits keys in the order they are written and TestBriefScriptMatchesTheGoBrief compares
// bytes.
type agentBrief struct {
	Version             int              `json:"version"`
	SessionID           string           `json:"session_id"`
	TurnID              string           `json:"turn_id"`
	Source              agentSource      `json:"source"`
	Profile             agentProfile     `json:"profile"`
	Skill               agentSkill       `json:"skill"`
	Transcript          []agentUtterance `json:"transcript"`
	TranscriptTruncated bool             `json:"transcript_truncated"`
	Instruction         string           `json:"instruction"`
}

type agentSource struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	URL  string `json:"url,omitempty"`
}

type agentProfile struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model"`
}

type agentSkill struct {
	Name         string   `json:"name"`
	SystemPrompt string   `json:"system_prompt"`
	AllowedTools []string `json:"allowed_tools"`
	MaxTurns     int      `json:"max_turns"`
}

type agentUtterance struct {
	Role   string `json:"role"`
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

// turnBrief is the brief examples/agent/brief.sh prints, for one instruction.
func turnBrief(instruction string) agentBrief {
	return agentBrief{
		Version:   1,
		SessionID: "sess_00000000000000000000000000",
		TurnID:    "turn_00000000000000000000000000",
		Source:    agentSource{Kind: "chat", Ref: "chat_00000000000000000000000000"},
		Profile: agentProfile{
			Name:         "podium",
			DisplayName:  "Podium",
			SystemPrompt: "You are Podium, an agent that runs on Podium.",
			Model:        "claude-opus-5",
		},
		Skill: agentSkill{
			Name:         "general",
			SystemPrompt: "Answer the question. Use the tools you have to check before you answer.",
			AllowedTools: []string{"read", "grep", "glob", "bash"},
			MaxTurns:     20,
		},
		Transcript:          []agentUtterance{},
		TranscriptTruncated: false,
		Instruction:         instruction,
	}
}

// encodeBrief renders a brief the way the conductor will: compact JSON, base64, no newline.
// HTML escaping is off because jq does not escape `<`, `>` or `&` either, and the bytes have
// to match.
func encodeBrief(brief agentBrief) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(brief); err != nil {
		panic(err) // a fixed struct of strings cannot fail to marshal
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.TrimSuffix(buf.String(), "\n")))
}

func envTurn(brief agentBrief) string { return "PODIUM_AGENT_TURN=" + encodeBrief(brief) }

// agentRun is the `podium run` a conductor will submit, minus the secrets: the image only,
// no command, so the image's own ENTRYPOINT runs behind podium-runner.
func agentRun(brief agentBrief) []string {
	return []string{"run", "--image", agentRuntimeImage,
		"--env", "PODIUM_AGENT_DRY_RUN=1", "--env", envTurn(brief)}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// requireAgentRuntimeImage skips the test when the image has not been built, with the
// command that builds it. The Docker API answers this, never the docker CLI.
func requireAgentRuntimeImage(t *testing.T) {
	t.Helper()
	cli := dockerClient(t)
	if _, err := cli.ImageInspect(context.Background(), agentRuntimeImage); err != nil {
		t.Skipf("%s is not on this engine (%v); run `make agent-runtime` first", agentRuntimeImage, err)
	}
}

// agentTurnSummary is agent/runtime/src/artifacts.ts's TurnSummary, read back as an artifact.
type agentTurnSummary struct {
	SessionID    string  `json:"session_id"`
	TurnID       string  `json:"turn_id"`
	SDKSessionID string  `json:"sdk_session_id"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	ExitCode     int     `json:"exit_code"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   string  `json:"finished_at"`
}

// turnSummary downloads turn.json through the CLI and parses it.
func turnSummary(t *testing.T, h *harness, artifactList string) agentTurnSummary {
	t.Helper()
	out := filepath.Join(t.TempDir(), "turn.json")
	id := artifactIDFor(t, artifactList, "turn.json")
	code, stdout, stderr := h.podium("artifact", "get", id, "-o", out)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)

	raw, err := os.ReadFile(out) //nolint:gosec // this test's own download
	require.NoError(t, err)
	var summary agentTurnSummary
	require.NoError(t, json.Unmarshal(raw, &summary), "turn.json must parse: %s", raw)
	return summary
}
