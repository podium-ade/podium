//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/linear"
)

// githubTokenSecret is the reserved secret the coder skill — and only the coder skill —
// names. It has to exist before a coder turn can be admitted.
const githubTokenSecret = "podium.agent.github_token"

// fakeLinearAPIKey is an obvious fixture. Nothing in Podium validates the shape of a Linear
// key, so a stub is happy with this.
const fakeLinearAPIKey = "not-a-real-linear-key"

// ticketTitle is what the fake workspace's one issue is called. The assertion that the
// answer reached Linear looks for it, because the dry-run runtime echoes its instruction.
const ticketTitle = "Add a copy button to the task page"

const (
	fakeIssueID    = "5a1f0c3e-0000-4000-8000-000000000001"
	fakeTeamID     = "b2e1d4a5-0000-4000-8000-000000000002"
	fakeInProgress = "c3f2e5b6-0000-4000-8000-000000000003"
	fakeIdentifier = "ENG-42"
)

// ---------------------------------------------------------------------------
// a fake Linear workspace
// ---------------------------------------------------------------------------

// fakeLinear is Linear's GraphQL endpoint, as much of it as the conductor uses: the viewer
// lookup, the assigned-issues poll, one issue with its comments, a team's workflow states,
// commentCreate, commentUpdate and issueUpdate.
//
// There is no Linear credential on this machine, so this is the boundary the suite stops
// at. Every response body below is the shape Linear's published schema declares — in
// particular a comment's `user` is nullable and a workflow state's `type` is a plain string.
type fakeLinear struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// ops is every operation name that arrived, in order.
	ops []string
	// comments is what the bot has posted, in order, by id.
	comments []fakeComment
	// moves is every issueUpdate, as the state id it asked for.
	moves []string
	// auths is the Authorization header of every call.
	auths []string
	// unauthorized makes every call answer the way Linear answers a bad key.
	unauthorized bool
}

type fakeComment struct {
	ID   string
	Body string
	// Edits counts how often the body was replaced.
	Edits int
}

func startFakeLinear(t *testing.T) *fakeLinear {
	t.Helper()
	f := &fakeLinear{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinear) endpoint() string { return f.srv.URL + "/graphql" }

func (f *fakeLinear) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	}
	_ = json.Unmarshal(raw, &req)

	f.mu.Lock()
	f.ops = append(f.ops, req.OperationName)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	unauthorized := f.unauthorized
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if unauthorized {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required, not authenticated",` +
			`"extensions":{"code":"AUTHENTICATION_ERROR"}}]}`))
		return
	}

	switch req.OperationName {
	case "PodiumViewer":
		f.write(w, map[string]any{"viewer": map[string]any{
			"id": "user-bot", "name": "podium", "displayName": "Podium", "isMe": true,
		}})
	case "PodiumAssignedIssues":
		f.write(w, map[string]any{"issues": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":    []map[string]any{f.issueNode()},
		}})
	case "PodiumIssue":
		f.write(w, map[string]any{"issue": f.issueNode()})
	case "PodiumTeamStates":
		f.write(w, map[string]any{"team": map[string]any{
			"id": fakeTeamID, "key": "ENG",
			"states": map[string]any{"nodes": []map[string]any{
				{"id": "todo", "name": "Todo", "type": "unstarted", "position": 0.0},
				{"id": fakeInProgress, "name": "In Progress", "type": "started", "position": 1.0},
			}},
		}})
	case "PodiumAddComment":
		id, _ := req.Variables["id"].(string)
		body, _ := req.Variables["body"].(string)
		f.mu.Lock()
		f.comments = append(f.comments, fakeComment{ID: id, Body: body})
		f.mu.Unlock()
		f.write(w, map[string]any{"commentCreate": map[string]any{
			"success": true, "comment": map[string]any{"id": id},
		}})
	case "PodiumEditComment":
		id, _ := req.Variables["id"].(string)
		body, _ := req.Variables["body"].(string)
		f.mu.Lock()
		for i := range f.comments {
			if f.comments[i].ID == id {
				f.comments[i].Body = body
				f.comments[i].Edits++
			}
		}
		f.mu.Unlock()
		f.write(w, map[string]any{"commentUpdate": map[string]any{"success": true}})
	case "PodiumMoveIssue":
		state, _ := req.Variables["stateId"].(string)
		f.mu.Lock()
		f.moves = append(f.moves, state)
		f.mu.Unlock()
		f.write(w, map[string]any{"issueUpdate": map[string]any{"success": true}})
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"the fake has no ` + req.OperationName + `"}]}`))
	}
}

func (f *fakeLinear) write(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// issueNode is the one ticket in this workspace, assigned to the bot and unstarted. Its
// comments carry only what the bot itself has said, so nothing here ever produces a
// follow-up: the assignment is the story under test.
func (f *fakeLinear) issueNode() map[string]any {
	f.mu.Lock()
	nodes := make([]map[string]any, 0, len(f.comments))
	for i, c := range f.comments {
		nodes = append(nodes, map[string]any{
			"id":         c.ID,
			"body":       c.Body,
			"createdAt":  time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			"parentId":   nil,
			"quotedText": nil,
			// The bot posted through this API key, so Linear reports isMe.
			"user": map[string]any{
				"id": "user-bot", "name": "podium", "displayName": "Podium", "isMe": true,
			},
			"botActor": nil,
		})
	}
	f.mu.Unlock()
	return map[string]any{
		"id":          fakeIssueID,
		"identifier":  fakeIdentifier,
		"title":       ticketTitle,
		"description": "There is no way to copy a task id out of the UI.",
		"url":         "https://linear.app/acme/issue/" + fakeIdentifier,
		"createdAt":   time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		"updatedAt":   time.Now().UTC().Format(time.RFC3339Nano),
		"state":       map[string]any{"id": "todo", "name": "Todo", "type": "unstarted"},
		"team":        map[string]any{"id": fakeTeamID, "key": "ENG"},
		"assignee": map[string]any{
			"id": "user-bot", "name": "podium", "displayName": "Podium", "isMe": true,
		},
		"comments": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":    nodes,
		},
	}
}

func (f *fakeLinear) seenOps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func (f *fakeLinear) posted() []fakeComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeComment(nil), f.comments...)
}

func (f *fakeLinear) stateMoves() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.moves...)
}

func (f *fakeLinear) seenAuths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

func (f *fakeLinear) refuseEverything() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unauthorized = true
}

// countOp is how often one operation arrived.
func countOp(ops []string, want string) int {
	n := 0
	for _, op := range ops {
		if op == want {
			n++
		}
	}
	return n
}

// firstIndexOf is where an operation first appeared, or -1.
func firstIndexOf(ops []string, want string) int {
	for i, op := range ops {
		if op == want {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// a profile whose coder skill runs a dry run on the plain runtime image
// ---------------------------------------------------------------------------

// linearProfileDir copies examples/agent and rewrites the coder skill so a turn runs the
// plain runtime image in dry run: no browser needed to prove the machinery, and no model
// key spent. The rewrite is the skill schema's own `env:` map (step 17) plus the image, and
// nothing else about the skill changes — the GitHub token secret in particular stays, which
// is what the secret assertions below read.
func linearProfileDir(t *testing.T, extra map[string]string) string {
	t.Helper()
	dst := copyExampleProfile(t)

	coder := filepath.Join(dst, "skills", "coder.yaml")
	raw, err := os.ReadFile(coder) //nolint:gosec // this test's own copy
	require.NoError(t, err)
	body := strings.Replace(string(raw),
		"image: podium-agent-runtime-browser:dev",
		"image: "+agentRuntimeImage, 1)
	require.NotEqual(t, string(raw), body, "examples/agent/skills/coder.yaml no longer names the browser image")
	// The label goes too: this node is not labelled `browser`, and the point of the test is
	// the Linear machinery rather than Chromium.
	body = strings.Replace(body, "labels: [browser]\n", "", 1)
	body += "env:\n  PODIUM_AGENT_DRY_RUN: \"1\"\n"
	for k, v := range extra {
		body += fmt.Sprintf("  %s: %q\n", k, v)
	}
	require.NoError(t, os.WriteFile(coder, []byte(body), 0o600))
	return dst
}

// startAgentWithLinear brings up the conductor with the Linear source pointed at the fake.
func startAgentWithLinear(t *testing.T, h *harness, profileDir, endpoint string) *agentProc {
	t.Helper()
	a := &agentProc{
		t:           t,
		h:           h,
		addr:        freeLoopbackAddr(t),
		databaseURL: newAgentDatabase(t),
		profileDir:  profileDir,
		extraEnv: []string{
			"PODIUM_AGENT_LINEAR_API_KEY=" + fakeLinearAPIKey,
			"PODIUM_AGENT_LINEAR_URL=" + endpoint,
			// The floor. A test must not wait 30 seconds for the first tick, and 10s is
			// what the config refuses to go below.
			"PODIUM_AGENT_LINEAR_POLL_INTERVAL=10s",
			"PODIUM_AGENT_UI_URL=https://podium.example",
		},
	}
	a.start()
	t.Cleanup(a.stop)
	a.awaitReady()
	return a
}

// linearTurnRows reads the turns of the Linear session straight out of podium_agent.
func linearTurnRows(t *testing.T, databaseURL string) []turnRow {
	t.Helper()
	return turnRows(t, databaseURL, linear.SourceKey(fakeIssueID))
}

// ---------------------------------------------------------------------------
// TestLinearAssignmentStartsATurn
// ---------------------------------------------------------------------------

// TestLinearAssignmentStartsATurn is story two, end to end, with Linear replaced by a fake
// GraphQL endpoint and the model replaced by the dry run: an issue assigned to the bot
// becomes one session, one turn and one Podium task on a real node, the ticket is moved to
// In Progress, and what the runtime says comes back as a comment on the issue.
//
// It also restarts the conductor while the task is running, which is the acceptance item
// that the relay ledger and the poll watermark together stop a double post.
func TestLinearAssignmentStartsATurn(t *testing.T) {
	requireAgentRuntimeImage(t)

	// No object store: this test asserts the Linear machinery, not the runtime's own
	// accounting, and a turn works end to end without one (its turn.json simply never
	// becomes an artifact).
	h := newHarness(t)
	startNode(t, h)
	// Both reserved secrets must exist before admission lets a coder turn through. A dry
	// run reads neither; the spec names both, and that is what is checked.
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	setSecret(t, h, githubTokenSecret, "ghp-not-a-real-token")

	fake := startFakeLinear(t)
	// A slow dry run, so the restart below lands while the task is genuinely running.
	profileDir := linearProfileDir(t, map[string]string{"PODIUM_AGENT_DRY_RUN_SLEEP_MS": "20000"})
	agent := startAgentWithLinear(t, h, profileDir, fake.endpoint())

	// The poll turns the assignment into a task.
	var taskID string
	waitFor(t, 2*time.Minute, "the assignment to become a task", func() bool {
		rows := linearTurnRows(t, agent.databaseURL)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		taskID = rows[0].TaskID
		return true
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\nlinear ops: %v", agent.logs(), fake.seenOps())
	})
	t.Logf("linear turn task: %s", taskID)

	// The key went out bare, with no Bearer prefix. This is the one thing about Linear's
	// auth that is easy to get wrong and impossible to notice from a 401.
	auths := fake.seenAuths()
	require.NotEmpty(t, auths)
	for _, got := range auths {
		assert.Equal(t, fakeLinearAPIKey, got, "a personal API key goes in Authorization raw")
	}

	// The task is the conductor's, on the coder skill, with exactly the credentials that
	// skill's file names plus the two the conductor reserves.
	spec := taskSpecJSON(t, h, taskID)
	assert.Equal(t, agentRuntimeImage, spec.Spec.Image)
	assert.NotEmpty(t, spec.RequestedBy, "the task must record who asked for it")
	assertCoderSecrets(t, spec)

	// The ticket moved to In Progress, and it moved BEFORE anything was said: a human
	// scanning the board sees it picked up before they see a comment.
	waitFor(t, time.Minute, "the ticket to move to In Progress", func() bool {
		return len(fake.stateMoves()) > 0
	}, func() string { return "linear ops: " + strings.Join(fake.seenOps(), ", ") })
	assert.Equal(t, []string{fakeInProgress}, fake.stateMoves(),
		"one move, to the state named In Progress")
	ops := fake.seenOps()
	move, comment := firstIndexOf(ops, "PodiumMoveIssue"), firstIndexOf(ops, "PodiumAddComment")
	require.NotEqual(t, -1, move)
	require.NotEqual(t, -1, comment)
	assert.Less(t, move, comment, "the state move comes before the first comment")

	// Restart the conductor mid-turn. The task keeps running — the conductor is a client
	// of the control plane, not the thing running the container — and the recovery pass
	// picks the turn back up.
	waitForTaskStatus(t, h, taskID, "TASK_STATUS_RUNNING", 2*time.Minute)
	agent.restart()

	waitFor(t, 4*time.Minute, "the turn to succeed", func() bool {
		rows := linearTurnRows(t, agent.databaseURL)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return fmt.Sprintf("agent log:\n%s\nlinear comments: %+v\npodium task:\n%s",
			agent.logs(), fake.posted(), h.podiumOK("task", "get", taskID))
	})

	rows := linearTurnRows(t, agent.databaseURL)
	require.Len(t, rows, 1, "one assignment is one turn, restart or no restart")
	assert.Equal(t, "succeeded", rows[0].Status)

	// The answer reached Linear exactly once, with the ticket's own words in it — the dry
	// run echoes its instruction, which is the ticket title and description.
	comments := fake.posted()
	t.Logf("linear comments:\n%+v", comments)
	finals := 0
	for _, c := range comments {
		if strings.Contains(c.Body, "dry run:") {
			finals++
			assert.Contains(t, c.Body, ticketTitle, "the ticket's title reached the turn")
			assert.Contains(t, c.Body, fakeIdentifier, "so did its identifier, for the branch name")
			assert.Contains(t, c.Body, "_Podium task "+taskID+"_", "the footer names the task")
		}
	}
	assert.Equal(t, 1, finals, "the answer is posted once, not twice: %+v", comments)

	// Two comments per turn is the budget: one working comment and one final. The restart
	// costs one more, because the placeholder's id died with the old process — which is
	// the documented behaviour, not a surprise.
	assert.LessOrEqual(t, len(comments), 3,
		"a turn produces two comments, or three across a restart: %+v", comments)

	// The relay ledger claimed exactly one seq for the one thing that was said.
	assert.Len(t, relayedSeqs(t, agent.databaseURL, taskID), 1,
		"one final message is one relayed row, across the restart")

	// And nothing was left behind on the engine.
	requireNoPodiumResources(t)
}

// TestOnlyTheCoderSkillGetsTheGitHubToken is the acceptance item, read through the API the
// step file names: `podium task get --json`. A skill decides what THIS bot hands a turn, so
// the general skill's turns must not carry a credential it never asked for. (It is not a
// boundary around the secret store — see docs/security.md — but it is still the difference
// between a public channel's turns holding a GitHub token and not.)
func TestOnlyTheCoderSkillGetsTheGitHubToken(t *testing.T) {
	requireAgentRuntimeImage(t)

	h := newHarness(t)
	startNode(t, h)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	setSecret(t, h, githubTokenSecret, "ghp-not-a-real-token")

	fake := startFakeLinear(t)
	profileDir := linearProfileDir(t, nil)
	// Both sources at once: the dev source drives the general skill and the Linear source
	// drives the coder skill, so the two specs are produced by one conductor from one
	// profile and the difference between them is only the skill file.
	agent := startAgentWithLinear(t, h, profileDir, fake.endpoint())

	var coderTask string
	waitFor(t, 2*time.Minute, "the coder task to appear", func() bool {
		rows := linearTurnRows(t, agent.databaseURL)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		coderTask = rows[0].TaskID
		return true
	}, func() string { return "agent log:\n" + agent.logs() })

	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"just answer this","dry_run":true}`)
	var generalTask string
	waitFor(t, 2*time.Minute, "the general task to appear", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		generalTask = rows[0].TaskID
		return true
	}, func() string { return "agent log:\n" + agent.logs() })

	coder := taskSpecJSON(t, h, coderTask)
	general := taskSpecJSON(t, h, generalTask)
	t.Logf("coder secrets:   %+v", coder.Spec.Secrets)
	t.Logf("general secrets: %+v", general.Spec.Secrets)

	assertCoderSecrets(t, coder)
	for _, ref := range general.Spec.Secrets {
		assert.NotEqual(t, githubTokenSecret, ref.Name,
			"the general skill never names the GitHub token, so its turns must not get it")
		assert.NotEqual(t, "GITHUB_TOKEN", ref.Key)
	}
	// And it still gets the one credential the conductor attaches to every turn.
	assert.Contains(t, secretNames(general), anthropicKeySecret)
}

// TestAMisconfiguredLinearKeyStopsTheConductor is the acceptance item: a key that is set
// and does not work is fatal at boot, with a message naming Linear. Discovering it an hour
// later, because no ticket was ever picked up, is the failure this prevents.
func TestAMisconfiguredLinearKeyStopsTheConductor(t *testing.T) {
	h := newHarness(t)
	fake := startFakeLinear(t)
	fake.refuseEverything()

	a := &agentProc{
		t:           t,
		h:           h,
		addr:        freeLoopbackAddr(t),
		databaseURL: newAgentDatabase(t),
		profileDir:  linearProfileDir(t, nil),
		extraEnv: []string{
			"PODIUM_AGENT_LINEAR_API_KEY=" + fakeLinearAPIKey,
			"PODIUM_AGENT_LINEAR_URL=" + fake.endpoint(),
			"PODIUM_AGENT_LINEAR_POLL_INTERVAL=10s",
		},
	}
	a.start()
	t.Cleanup(a.stop)

	waitFor(t, time.Minute, "the conductor to give up on Linear", func() bool {
		return strings.Contains(a.logs(), "PODIUM_AGENT_LINEAR_API_KEY")
	}, func() string { return "agent log:\n" + a.logs() })
	logs := a.logs()
	assert.Contains(t, logs, "linear")
	assert.Contains(t, logs, "Authentication required")
	assert.NotContains(t, logs, fakeLinearAPIKey, "a credential never reaches a log line")
}

// ---------------------------------------------------------------------------
// reading a task spec back through the CLI
// ---------------------------------------------------------------------------

// taskSpecView is as much of `podium task get --json` as these assertions read. It is a
// separate shape from m1's taskJSON on purpose: that one watches statuses, this one reads
// the spec a turn was given.
type taskSpecView struct {
	ID          string `json:"id"`
	RequestedBy string `json:"requestedBy"`
	Spec        struct {
		Image   string `json:"image"`
		Labels  []string
		Secrets []struct {
			Name   string `json:"name"`
			Target string `json:"target"`
			Key    string `json:"key"`
		} `json:"secrets"`
	} `json:"spec"`
}

// taskSpecJSON reads a task back the way the acceptance item asks for it: through the CLI,
// as JSON. It is deliberately not a database query — the point is that an operator can see
// which credentials a turn was given.
func taskSpecJSON(t *testing.T, h *harness, taskID string) taskSpecView {
	t.Helper()
	out := h.podiumOK("task", "get", taskID, "--json")
	var task taskSpecView
	require.NoError(t, json.Unmarshal([]byte(out), &task), "podium task get --json:\n%s", out)
	require.Equal(t, taskID, task.ID)
	return task
}

func secretNames(task taskSpecView) []string {
	out := make([]string, 0, len(task.Spec.Secrets))
	for _, ref := range task.Spec.Secrets {
		out = append(out, ref.Name)
	}
	return out
}

// assertCoderSecrets is the acceptance item's positive half: a coder turn gets GITHUB_TOKEN
// and nothing else out of the secret store beyond the two credentials the conductor
// attaches to every turn.
func assertCoderSecrets(t *testing.T, task taskSpecView) {
	t.Helper()
	var fromSkill []string
	for _, ref := range task.Spec.Secrets {
		switch ref.Name {
		case anthropicKeySecret, "podium.agent.memory_api_key":
			// Reserved: the conductor adds these itself, whatever a skill file says.
			continue
		}
		fromSkill = append(fromSkill, ref.Name)
		assert.Equal(t, "env", ref.Target)
	}
	require.Equal(t, []string{githubTokenSecret}, fromSkill,
		"the coder skill gets the GitHub token and nothing else: %+v", task.Spec.Secrets)
	for _, ref := range task.Spec.Secrets {
		if ref.Name == githubTokenSecret {
			assert.Equal(t, "GITHUB_TOKEN", ref.Key)
		}
	}
}

// TestTheLinearSourceKeyIsStable pins the session identity the conductor stores, because a
// change to it silently orphans every conversation in an existing database.
func TestTheLinearSourceKeyIsStable(t *testing.T) {
	assert.Equal(t, "linear:"+fakeIssueID, linear.SourceKey(fakeIssueID))
}
