//go:build integration

package conductor_test

// Delegation, end to end: a host turn that starts a task, and the conversation that owns it.
//
// The fake runtime here is the same re-executed test binary as host_integration_test.go, and
// what it does is what the real MCP server does — read the brief's delegation block, POST to
// TurnService over Connect's JSON protocol with the turn's token in a header, poll until the
// task is terminal, and report what happened. It speaks HTTP rather than importing anything,
// so what these tests exercise is the wire: the token, the header, the codes, the JSON.
//
// What they cannot exercise is opencode deciding to call the tool. That is the one hop with
// no test on this side of the process boundary, and it is why the MCP surface has its own
// suite in agent/runtime/src/mcp.test.ts.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/api"
	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1/agentv1connect"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// The fake runtime's delegation knobs. Not PODIUM_*, for the reason host_integration_test.go
// gives: deploy/env_test.go holds every PODIUM_ name to being documented or excused.
const (
	// hostFakeDelegateEnv is "<playbook>|<instruction>": what the turn should ask for.
	hostFakeDelegateEnv = "CONDUCTOR_TEST_DELEGATE"
	// hostFakePollEnv makes the fake poll until the delegation is terminal before it
	// answers, which is what an agent that waits for its task looks like.
	hostFakePollEnv = "CONDUCTOR_TEST_DELEGATE_POLL"
)

// delegateReport is what the fake runtime says as its answer, so a test can assert on what
// the wire actually did rather than on what it hopes happened.
type delegateReport struct {
	URL      string `json:"url"`
	TokenSet bool   `json:"token_set"`
	// Token is the turn's actual token. A REAL runtime would never report one — it is a
	// bearer for somebody's conversation — and the fake does exactly so that a test can
	// present it again after the turn has ended and watch it be refused.
	Token      string   `json:"token"`
	Menu       []string `json:"menu"`
	RunsOnHost bool     `json:"runs_on_host"`
	Status     int      `json:"status"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	ID         string   `json:"id"`
	TaskID     string   `json:"task_id"`
	Final      string   `json:"final"`
	Progress   string   `json:"progress"`
	Polls      int      `json:"polls"`
}

// hostFakeDelegate is the fake runtime's delegation half, called from hostFakeRuntime when
// hostFakeDelegateEnv is set. It returns the answer the turn should report.
func hostFakeDelegate(brief map[string]any) delegateReport {
	report := delegateReport{RunsOnHost: brief["runs_on"] == "host"}
	block, _ := brief["delegation"].(map[string]any)
	if block == nil {
		report.Message = "the brief carries no delegation block"
		return report
	}
	report.URL, _ = block["url"].(string)
	tokenEnv, _ := block["token_env"].(string)
	token := os.Getenv(tokenEnv)
	report.TokenSet = token != ""
	report.Token = token
	for _, p := range block["playbooks"].([]any) {
		entry, _ := p.(map[string]any)
		name, _ := entry["name"].(string)
		report.Menu = append(report.Menu, name)
	}

	playbook, instruction, _ := strings.Cut(os.Getenv(hostFakeDelegateEnv), "|")
	status, body := turnCall(report.URL, token, "Delegate", map[string]string{
		"playbook": playbook, "instruction": instruction,
	})
	report.Status = status
	if status != http.StatusOK {
		report.Code, report.Message = connectError(body)
		return report
	}
	var res struct {
		Delegation struct {
			ID     string `json:"id"`
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		} `json:"delegation"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		report.Message = "undecodable Delegate response: " + err.Error()
		return report
	}
	report.ID = res.Delegation.ID
	report.TaskID = res.Delegation.TaskID

	if os.Getenv(hostFakePollEnv) == "" {
		return report
	}
	// Poll, as an agent does: until the task is terminal or the patience runs out.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		report.Polls++
		status, body := turnCall(report.URL, token, "GetDelegation", map[string]string{"id": report.ID})
		if status != http.StatusOK {
			report.Code, report.Message = connectError(body)
			return report
		}
		var got struct {
			Delegation struct {
				Status    string `json:"status"`
				FinalText string `json:"finalText"`
				TaskID    string `json:"taskId"`
			} `json:"delegation"`
			Progress string `json:"progress"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			report.Message = "undecodable GetDelegation response: " + err.Error()
			return report
		}
		if got.Progress != "" {
			report.Progress = got.Progress
		}
		if got.Delegation.Status != "running" {
			report.Final = got.Delegation.FinalText
			report.Message = got.Delegation.Status
			report.TaskID = got.Delegation.TaskID
			return report
		}
		time.Sleep(100 * time.Millisecond)
	}
	report.Message = "the delegated task never finished"
	return report
}

// turnCall is one Connect JSON call, exactly as agent/runtime/src/delegate.ts makes it.
func turnCall(url, token, method string, body map[string]string) (int, []byte) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, []byte(err.Error())
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimSuffix(url, "/")+"/podium.agent.v1.TurnService/"+method, bytes.NewReader(raw))
	if err != nil {
		return 0, []byte(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.TurnTokenHeader, token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, []byte(err.Error())
	}
	return res.StatusCode, out
}

func connectError(body []byte) (code, message string) {
	var doc struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", string(body)
	}
	return doc.Code, doc.Message
}

// turnAPI stands the TurnService up in front of a conductor, on its own listener, the way
// agent.go mounts it: no operator bearer, and a turn's token as the only credential.
func turnAPI(t *testing.T, cond *conductor.Conductor) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewTurnServiceHandler(api.NewTurnService(cond, quietLogger())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// delegated is the fake runtime's report, read back out of the host turn's answer.
func delegated(t *testing.T, records []fakesource.Record) delegateReport {
	t.Helper()
	finals := posts(records, conductor.OutFinal)
	require.NotEmpty(t, finals, "the host turn said nothing at all")
	var report delegateReport
	require.NoError(t, json.Unmarshal([]byte(finals[len(finals)-1].Text), &report),
		"the host turn's answer is not the fake runtime's report: %s", finals[len(finals)-1].Text)
	return report
}

func TestAHostTurnDelegatesAndTheConversationOwnsTheTask(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	// What the DELEGATED task says. This is the whole point of the feature: the container
	// does the work and the conversation hears about it.
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutProgress, "running the tests"),
			messageEvent(taskID, 2, conductor.OutFinal, "opened #51 and the suite is green"),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "sk-test")
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = host })
	// The address the turn will be told, set before any turn is built.
	host.TurnURL = turnAPI(t, r.cond).URL

	ev := inbound("C1/10.1", "make the tests pass")
	ev.Env = hostEnv(map[string]string{
		hostFakeDelegateEnv: "dogfood|fix the alignment and run the suite",
		hostFakePollEnv:     "1",
	})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 60*time.Second, "the host turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	// ONE task, and it is the delegated one: the host turn itself never had a task.
	specs := fake.Specs()
	require.Len(t, specs, 1, "a host turn creates no task of its own, and delegated exactly one")

	report := delegated(t, src.Records())
	assert.True(t, report.RunsOnHost, "the brief must say where the turn is running")
	assert.True(t, report.TokenSet, "the token reaches the runtime through the environment")
	assert.Equal(t, host.TurnURL, report.URL)
	assert.Equal(t, []string{"coder", "dogfood", "general", "looker"}, report.Menu,
		"every playbook is delegable, the conversation's own included: running it as a task "+
			"is a container with a repository and a shell, which is the point")
	assert.Equal(t, http.StatusOK, report.Status)
	assert.NotEmpty(t, report.ID)
	assert.Equal(t, fake.TaskIDs()[0], report.TaskID)
	assert.Equal(t, "succeeded", report.Message, "the turn polled until the task was terminal")
	assert.GreaterOrEqual(t, report.Polls, 1)
	assert.Equal(t, "opened #51 and the suite is green", report.Final,
		"the task's answer is readable by the turn that asked for it")

	// The DELEGATED task's brief: the playbook's own, and with no delegation of its own —
	// which is what stops a delegated task delegating again.
	brief := decodeBrief(t, specs[0])
	assert.Equal(t, "dogfood", brief.Playbook.Name)
	assert.Equal(t, "fix the alignment and run the suite", brief.Instruction)
	assert.Nil(t, brief.Delegation, "a delegated task must not be able to delegate")
	assert.Empty(t, brief.RunsOn, "a delegated task runs in a container, like any other task")
	assert.NotEmpty(t, brief.Transcript, "the task gets the conversation it is working for")

	// The conversation heard all of it: the announcement, the task's progress, its answer.
	records := src.Records()
	texts := make([]string, 0, len(records))
	for _, rec := range records {
		texts = append(texts, rec.Text)
	}
	joined := strings.Join(texts, "\n")
	assert.Contains(t, joined, "Working on this in a `dogfood` task",
		"a container doing work somebody asked for must not be invisible")
	assert.Contains(t, joined, "fix the alignment", "the announcement says what was asked")
	assert.Contains(t, joined, "running the tests", "the task's progress reaches the conversation")
	assert.Contains(t, joined, "opened #51 and the suite is green")

	// And the row: the durable half, which is what owns the task after the turn is over.
	dlgs := delegationsOf(t, st, ev.SourceKey)
	require.Len(t, dlgs, 1)
	assert.Equal(t, store.TurnSucceeded, dlgs[0].Status)
	assert.Equal(t, "dogfood", dlgs[0].Playbook)
	assert.Equal(t, fake.TaskIDs()[0], dlgs[0].TaskID)
	assert.Equal(t, "opened #51 and the suite is green", dlgs[0].FinalText)
	assert.Equal(t, "C1/10.1", dlgs[0].TriggerRef)
	require.NotNil(t, dlgs[0].FinishedAt)
}

func TestATurnMayNotDelegateToAPlaybookItWasNotOffered(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "sk-test")
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = host })
	host.TurnURL = turnAPI(t, r.cond).URL

	ev := inbound("C1/11.1", "do something clever")
	ev.Env = hostEnv(map[string]string{hostFakeDelegateEnv: "root-shell|rm -rf /"})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 60*time.Second, "the host turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	report := delegated(t, src.Records())
	assert.Equal(t, http.StatusBadRequest, report.Status)
	assert.Equal(t, "invalid_argument", report.Code)
	assert.Contains(t, report.Message, "not offered to this turn")
	assert.Contains(t, report.Message, "root-shell")
	assert.Empty(t, fake.Specs(), "a playbook that was not offered starts nothing")
	assert.Empty(t, delegationsOf(t, st, ev.SourceKey), "and records nothing")
}

func TestATokenIsRefusedOnceItsTurnIsOver(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	host := hostRuntime(t, "sk-test")
	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = host })
	turns := turnAPI(t, r.cond)
	host.TurnURL = turns.URL

	ev := inbound("C1/12.1", "delegate once")
	ev.Env = hostEnv(map[string]string{hostFakeDelegateEnv: "dogfood|do the thing"})
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 60*time.Second, "the host turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	report := delegated(t, src.Records())
	require.Equal(t, http.StatusOK, report.Status, "the delegation itself worked")

	// The turn is over, so its authority is gone. This is the whole reason the token is
	// per-turn: a token that outlived its turn would let anything that ever read one start
	// tasks in somebody's conversation.
	require.NotEmpty(t, report.Token)
	status, body := turnCall(turns.URL, report.Token, "ListDelegations", map[string]string{})
	assert.Equal(t, http.StatusUnauthorized, status)
	code, message := connectError(body)
	assert.Equal(t, "unauthenticated", code)
	assert.Contains(t, message, "no turn is running")
}

func TestADelegatedTaskIsPickedUpAgainAfterARestart(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutFinal, "finished while nobody was watching"),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	// A delegation left in flight by a conductor that died: a row saying running, and a
	// task on a node that does not care.
	ctx := context.Background()
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.KindDev, SourceKey: conductor.KindDev + ":C1:13.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := st.CreateTurn(ctx, sess.ID, "C1/13.1", testBackend())
	require.NoError(t, err)
	task := fakeTaskID(t, fake)
	dlg, err := st.CreateDelegation(ctx, store.NewDelegation{
		SessionID: sess.ID, TurnID: turn.ID, TriggerRef: "C1/13.1",
		Playbook: "dogfood", Instruction: "keep going",
	})
	require.NoError(t, err)
	require.NoError(t, st.SetDelegationTask(ctx, dlg.ID, task))

	// A conductor starting up finds it and takes over.
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	waitFor(t, 60*time.Second, "the resumed delegation to finish", func() bool {
		got, err := st.GetDelegation(context.Background(), dlg.ID)
		return err == nil && got.Status == store.TurnSucceeded
	})
	got, err := st.GetDelegation(ctx, dlg.ID)
	require.NoError(t, err)
	assert.Equal(t, "finished while nobody was watching", got.FinalText,
		"the answer reaches the conversation even though the turn that asked is long gone")

	joined := ""
	for _, rec := range src.Records() {
		joined += rec.Text + "\n"
	}
	assert.Contains(t, joined, "finished while nobody was watching")
}

func TestADelegationWhoseTaskNeverStartedIsFailedOnRecovery(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.KindDev, SourceKey: conductor.KindDev + ":C1:14.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := st.CreateTurn(ctx, sess.ID, "C1/14.1", testBackend())
	require.NoError(t, err)
	// Recorded, and the control plane never accepted the task: the row is all there is.
	dlg, err := st.CreateDelegation(ctx, store.NewDelegation{
		SessionID: sess.ID, TurnID: turn.ID, TriggerRef: "C1/14.1",
		Playbook: "dogfood", Instruction: "never started",
	})
	require.NoError(t, err)

	startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	waitFor(t, 30*time.Second, "the orphaned delegation to be failed", func() bool {
		got, err := st.GetDelegation(context.Background(), dlg.ID)
		return err == nil && got.Status == store.TurnFailed
	})
}

func TestDeletingAConversationStopsTheTasksItDelegated(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.KindDev, SourceKey: conductor.KindDev + ":C1:15.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	turn, err := st.CreateTurn(ctx, sess.ID, "C1/15.1", testBackend())
	require.NoError(t, err)
	task := fakeTaskID(t, fake)
	dlg, err := st.CreateDelegation(ctx, store.NewDelegation{
		SessionID: sess.ID, TurnID: turn.ID, TriggerRef: "C1/15.1",
		Playbook: "dogfood", Instruction: "long job",
	})
	require.NoError(t, err)
	require.NoError(t, st.SetDelegationTask(ctx, dlg.ID, task))

	r := startWith(t, st, fake, src, func(o *conductor.Options) { o.Host = hostRuntime(t, "sk-test") })

	stopped, err := r.cond.CancelDelegationsForRef(ctx, "C1/15.1", "the chat was deleted")
	require.NoError(t, err)
	assert.Equal(t, 1, stopped)

	cancels := fake.Cancels()
	require.Len(t, cancels, 1, "the node is asked to stop the container, not just the row")
	assert.Equal(t, task, cancels[0].GetTaskId())
	assert.Contains(t, cancels[0].GetReason(), "deleted")
}

// fakeTaskID creates a task on the fake control plane directly, for a test that needs one a
// conductor did not start — a delegation left in flight by a process that died.
func fakeTaskID(t *testing.T, fake *fakePodium) string {
	t.Helper()
	res, err := fake.CreateTask(context.Background(), connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{Image: "podium-agent-runtime:test"},
	}))
	require.NoError(t, err)
	return res.Msg.GetTask().GetId()
}

// delegationsOf is every delegation of a session's turns, for a test that knows the session
// by its source key.
func delegationsOf(t *testing.T, st *store.Store, sourceKey string) []store.Delegation {
	t.Helper()
	ctx := context.Background()
	sess, err := st.GetSessionByKey(ctx, sourceKey)
	require.NoError(t, err)
	turns, err := st.ListTurns(ctx, sess.ID, 50)
	require.NoError(t, err)
	var out []store.Delegation
	for _, turn := range turns {
		got, err := st.DelegationsForTurn(ctx, turn.ID)
		require.NoError(t, err)
		out = append(out, got...)
	}
	return out
}

// testBackend is what a turn ran on. These tests are about delegation and read none of it.
func testBackend() store.Backend {
	return store.Backend{Agent: "claude", Model: "claude-opus-5", Provider: "anthropic"}
}
