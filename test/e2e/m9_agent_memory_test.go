//go:build e2e

package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/server"
)

// memoryAPIKey is the fake shared-memory key this file configures the conductor with. It is
// a test fixture, not a secret.
const memoryAPIKey = "memtoken-e2e"

// ---------------------------------------------------------------------------
// a fake memory service
// ---------------------------------------------------------------------------

// fakeMemoryService is Hindsight's REST surface, as much of it as this step's conductor
// uses: /health, retain, list, recall and the invalidate PATCH. Every response body is the
// shape hindsight 0.9.2 actually returns.
//
// It exists here rather than as a container because the real image is 5.9 GB and needs a
// provider key to extract a fact from anything. `make e2e-memory` runs the real one.
type fakeMemoryService struct {
	srv *httptest.Server

	mu       sync.Mutex
	retains  []map[string]any
	paths    []string
	auths    []string
	memories []map[string]any
	down     bool
}

func startFakeMemoryService(t *testing.T) *fakeMemoryService {
	t.Helper()
	f := &fakeMemoryService{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMemoryService) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	down := f.down
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	if r.URL.Path != "/health" {
		f.auths = append(f.auths, r.Header.Get("Authorization"))
	}
	f.mu.Unlock()

	if down {
		http.Error(w, `{"detail":"database unreachable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/health":
		_, _ = w.Write([]byte(`{"status":"ok"}`))

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories"):
		var body struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.retains = append(f.retains, body.Items...)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"bank_id":"podium","items_count":1,"async":true}`))

	case strings.HasSuffix(r.URL.Path, "/memories/list"):
		f.mu.Lock()
		items := append([]map[string]any(nil), f.memories...)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items, "total": len(items), "limit": 50, "offset": 0,
		})

	case strings.HasSuffix(r.URL.Path, "/memories/recall"):
		f.mu.Lock()
		items := append([]map[string]any(nil), f.memories...)
		f.mu.Unlock()
		results := make([]map[string]any, 0, len(items))
		for _, m := range items {
			// Recall reports the fact type as `type` and entities as an array, where the
			// list endpoint uses `fact_type` and a comma-joined string.
			results = append(results, map[string]any{
				"id": m["id"], "text": m["text"], "type": m["fact_type"],
				"tags": m["tags"], "metadata": m["metadata"], "entities": []string{"Bob"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})

	case r.Method == http.MethodPatch:
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		kept := f.memories[:0:0]
		for _, m := range f.memories {
			if m["id"] != id {
				kept = append(kept, m)
			}
		}
		f.memories = kept
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"` + id + `","state":"invalidated"}`))

	default:
		http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
	}
}

func (f *fakeMemoryService) seed(items ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memories = append(f.memories, items...)
}

func (f *fakeMemoryService) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeMemoryService) retained() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.retains...)
}

func (f *fakeMemoryService) seenPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeMemoryService) seenAuths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

// memoryEnv is what a conductor with shared memory is configured with.
func memoryEnv(base string) []string {
	return []string{
		"PODIUM_AGENT_MEMORY_URL=" + base,
		// A dry run never connects an MCP client, so nothing in this file dials it; the
		// value still has to reach the brief.
		"PODIUM_AGENT_MEMORY_TASK_URL=http://host.docker.internal:8888",
		"PODIUM_AGENT_MEMORY_API_KEY=" + memoryAPIKey,
	}
}

// startAgentWithMemory is startAgent plus the memory configuration.
func startAgentWithMemory(t *testing.T, h *harness, addr, memoryURL string) *agentProc {
	t.Helper()
	a := &agentProc{
		t:           t,
		h:           h,
		addr:        addr,
		databaseURL: newAgentDatabase(t),
		profileDir:  agentProfileDir(t),
		extraEnv:    memoryEnv(memoryURL),
	}
	a.start()
	t.Cleanup(a.stop)
	a.awaitReady()
	return a
}

// briefOfTask reads back the brief the conductor actually put on a task spec, out of the
// control plane's own row. It is the only way to see what a turn ran with.
func briefOfTask(t *testing.T, h *harness, taskID string) conductor.Brief {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var raw []byte
	require.NoError(t, conn.QueryRow(ctx, "select spec from tasks where id = $1", taskID).Scan(&raw))

	var spec struct {
		Env     map[string]string `json:"env"`
		Secrets []struct {
			Name   string `json:"name"`
			Target string `json:"target"`
			Key    string `json:"key"`
		} `json:"secrets"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))

	encoded := spec.Env[conductor.BriefEnv]
	require.NotEmpty(t, encoded, "the spec must carry a brief")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)

	var brief conductor.Brief
	require.NoError(t, json.Unmarshal(decoded, &brief))
	return brief
}

// secretRefsOfTask is the secret list on a task's stored spec.
func secretRefsOfTask(t *testing.T, h *harness, taskID string) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var raw []byte
	require.NoError(t, conn.QueryRow(ctx, "select spec from tasks where id = $1", taskID).Scan(&raw))

	var spec struct {
		Secrets []struct {
			Name   string `json:"name"`
			Target string `json:"target"`
			Key    string `json:"key"`
		} `json:"secrets"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))

	out := map[string]string{}
	for _, ref := range spec.Secrets {
		out[ref.Name] = ref.Target + ":" + ref.Key
	}
	return out
}

// ---------------------------------------------------------------------------
// TestAgentTurnCarriesMemoryAndRetainsNothingOnADryRun
// ---------------------------------------------------------------------------

// TestAgentTurnCarriesMemoryAndRetainsNothingOnADryRun is step 17's round trip with shared
// memory configured. It proves four things about a real task on a real node:
//
//   - the conductor put the memory API key into the control plane's own secret store at
//     startup, so an operator configures it in one place;
//   - the brief the container got names the MCP endpoint and the variable the key arrives in;
//   - the task spec carries the memory secret, which is not optional — a brief with a memory
//     block whose api_key_env is unset is exit 2;
//   - and NOTHING was retained, because a dry run's canned answer is not a fact.
func TestAgentTurnCarriesMemoryAndRetainsNothingOnADryRun(t *testing.T) {
	requireAgentRuntimeImage(t)

	mem := startFakeMemoryService(t)
	h := newHarness(t)
	startNode(t, h)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")

	agent := startAgentWithMemory(t, h, freeLoopbackAddr(t), mem.srv.URL)

	// The conductor wrote the memory key itself. `podium secret ls` is metadata only —
	// there is no endpoint that could return a value — so this is the whole assertion.
	secrets := h.podiumOK("secret", "ls")
	assert.Contains(t, secrets, profiles.MemoryKeySecret,
		"the conductor stores the memory key as a Podium secret at startup: %s", secrets)

	sourceKey := agent.send(`{"channel":"C1","thread":"1.1","author":"alice",
		"text":"who owns the scheduler?","dry_run":true}`)

	var taskID string
	waitFor(t, 2*time.Minute, "the conductor's task to appear", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		if len(rows) != 1 || rows[0].TaskID == "" {
			return false
		}
		taskID = rows[0].TaskID
		return strings.Contains(h.podiumOK("tasks"), taskID)
	}, func() string { return "agent log:\n" + agent.logs() })

	waitFor(t, 3*time.Minute, "the turn to succeed", func() bool {
		rows := turnRows(t, agent.databaseURL, sourceKey)
		return len(rows) == 1 && rows[0].Status == "succeeded"
	}, func() string {
		return "agent log:\n" + agent.logs() + "\npodium task:\n" + h.podiumOK("task", "get", taskID)
	})

	// The brief the container decoded.
	brief := briefOfTask(t, h, taskID)
	require.NotNil(t, brief.Memory, "a turn on a host with memory must carry a memory block")
	assert.Equal(t, "http://host.docker.internal:8888/mcp/podium/", brief.Memory.MCPURL,
		"the bank in the path is what puts the memory service in single-bank mode")
	assert.Equal(t, profiles.MemoryKeyEnv, brief.Memory.APIKeyEnv)

	// And the secret that makes that key exist in the container.
	refs := secretRefsOfTask(t, h, taskID)
	assert.Equal(t, "env:"+profiles.MemoryKeyEnv, refs[profiles.MemoryKeySecret],
		"the memory secret must land in the variable the brief promised: %v", refs)
	assert.Contains(t, refs, profiles.AnthropicKeySecret)

	// The answer came back and the human heard it.
	require.True(t, hasFinal(agent.outbound(), "dry run: who owns the scheduler?"),
		"outbound: %+v", agent.outbound())

	// Nothing was remembered. A dry run's answer is canned, and the dev source is the only
	// thing that can ask for one.
	assert.Empty(t, mem.retained(), "a dry run must retain nothing: %v", mem.retained())
	assert.NotContains(t, strings.Join(mem.seenPaths(), " "), "POST /v1/default/banks/podium/memories ")

	// The health probe is the only thing that touched the memory service, and readyz is
	// what did it.
	assert.Contains(t, mem.seenPaths(), "GET /health")

	requireNoPodiumResources(t)
}

// ---------------------------------------------------------------------------
// TestAgentReadyzReportsTheMemoryDown
// ---------------------------------------------------------------------------

// A conductor whose memory service is down is not ready — but a turn still runs and finishes
// through it, which is the acceptance item that matters. Nothing about a memory outage
// reaches the conversation.
func TestAgentReadyzReportsTheMemoryDown(t *testing.T) {
	mem := startFakeMemoryService(t)
	h := newHarness(t)
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	agent := startAgentWithMemory(t, h, freeLoopbackAddr(t), mem.srv.URL)

	code, body := agent.get("/readyz", "")
	require.Equal(t, http.StatusOK, code, body)

	mem.setDown(true)
	waitFor(t, 30*time.Second, "readyz to report the memory unreachable", func() bool {
		code, body := agent.get("/readyz", "")
		return code == http.StatusServiceUnavailable && strings.Contains(body, "memory unreachable")
	}, func() string { return "agent log:\n" + agent.logs() })

	// /healthz never lied about the process being up.
	code, _ = agent.get("/healthz", "")
	assert.Equal(t, http.StatusOK, code)

	mem.setDown(false)
	waitFor(t, 30*time.Second, "readyz to recover", func() bool {
		code, _ := agent.get("/readyz", "")
		return code == http.StatusOK
	})
}

// ---------------------------------------------------------------------------
// TestMemoryRPCsThroughTheServerProxy
// ---------------------------------------------------------------------------

// The three memory RPCs, reached the way a browser reaches them: through podium-server's
// reverse proxy, behind its identity middleware, with the conductor's bearer added on the
// way. No Docker and no node — nothing here runs a task.
func TestMemoryRPCsThroughTheServerProxy(t *testing.T) {
	mem := startFakeMemoryService(t)
	mem.seed(map[string]any{
		"id":           "ed1bd235-bd25-483c-beff-4d54ff776e52",
		"text":         "Bob owns the Podium scheduler.",
		"fact_type":    "world",
		"context":      "podium agent, playbook general",
		"document_id":  "turn_01probe",
		"mentioned_at": time.Now().UTC().Format(time.RFC3339Nano),
		"entities":     "Bob, scheduler",
		"tags":         []string{"source:slack", "playbook:general"},
		"metadata":     map[string]string{"turn_id": "turn_01probe", "task_id": "task_01probe"},
		"state":        "valid",
	})

	agentAddr := freeLoopbackAddr(t)
	h := newHarness(t, func(c *server.Config) {
		c.AgentURL = "http://" + agentAddr
		c.AgentToken = agentToken
	})
	setSecret(t, h, anthropicKeySecret, "sk-ant-not-a-real-key")
	startAgentWithMemory(t, h, agentAddr, mem.srv.URL)

	list := viaProxy(t, h, "ListMemories", `{}`)
	assert.Contains(t, list, "Bob owns the Podium scheduler.")
	assert.Contains(t, list, `"factType":"world"`)
	assert.Contains(t, list, `"documentId":"turn_01probe"`)
	// The provenance a human traces a suspicious memory back through.
	assert.Contains(t, list, `"task_id":"task_01probe"`)
	assert.Contains(t, list, "source:slack")

	search := viaProxy(t, h, "SearchMemories", `{"query":"who owns the scheduler"}`)
	assert.Contains(t, search, "Bob owns the Podium scheduler.")

	// Forgetting one takes it out of BOTH the list and the search: the memory engine's
	// tombstone excludes an invalidated memory from recall as well as from the table.
	del := viaProxy(t, h, "DeleteMemory",
		`{"id":"ed1bd235-bd25-483c-beff-4d54ff776e52"}`)
	assert.JSONEq(t, `{}`, del)

	assert.NotContains(t, viaProxy(t, h, "ListMemories", `{}`), "Bob owns")
	assert.NotContains(t, viaProxy(t, h, "SearchMemories", `{"query":"who owns the scheduler"}`), "Bob owns")

	// Every call carried the bearer the memory service demands. It has none of its own by
	// default, which is why this is asserted rather than assumed.
	auths := mem.seenAuths()
	require.NotEmpty(t, auths)
	for _, auth := range auths {
		assert.Equal(t, "Bearer "+memoryAPIKey, auth)
	}
}

// viaProxy posts one AgentService call through podium-server, the way the web UI does.
func viaProxy(t *testing.T, h *harness, method, body string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	target, err := url.JoinPath(h.url(), "/podium.agent.v1.AgentService/"+method)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+devToken)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	require.Equal(t, http.StatusOK, res.StatusCode, "%s: %s", method, raw)
	return string(raw)
}
