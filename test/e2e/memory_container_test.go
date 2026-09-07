//go:build e2e && e2e_memory

// This file is behind its own extra build tag and is NOT part of `make e2e`. It brings up
// the real memory engine — a 5.9 GB third-party image — against the suite's Postgres, and
// asserts the contract internal/agent/memory depends on: the paths, the auth, the list
// envelope, the tombstone, and the MCP endpoint a turn connects to.
//
//	make e2e-memory
//
// What it deliberately does NOT assert is that a retain becomes a fact. Extraction is an LLM
// call, and the only honest ways to have one succeed are a real provider key (which no test
// may need) or a stub reproducing the engine's extraction prompt contract — which changes
// between releases and would fail for reasons that say nothing about Podium. So a retain is
// asserted to be ACCEPTED, and "the item arrives with this document_id" is asserted against
// a fake instead, in internal/agent/conductor/retain_integration_test.go and
// test/e2e/m9_agent_memory_test.go.
package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	moby "github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/alvaroibarguen/podium/internal/agent/memory"
)

// memoryImage is the version this build was written against, pinned by tag AND digest the
// way deploy/docker-compose.yml pins it. NOTE the tag has no `v`: `v0.9.2` does not resolve.
const memoryImage = "ghcr.io/vectorize-io/hindsight:0.9.2@sha256:" +
	"84ab276b8f501546deb6ea9c64a57291718b4e16a59dd9e02a02fdd5adfe9028"

// containerMemoryKey is an obvious fake.
const containerMemoryKey = "memtoken-container"

// TestTheRealMemoryEngineSpeaksWhatTheClientExpects is the bring-up smoke and the contract
// check in one.
func TestTheRealMemoryEngineSpeaksWhatTheClientExpects(t *testing.T) {
	ctx := context.Background()

	// A database of its own, with the pgvector extension already in `public`. The engine
	// would create it itself given the privilege, and would DROP and recreate it if it
	// found it in another schema — deploy/postgres/init.sql does this for the same reason.
	name := fmt.Sprintf("podium_memory_e2e_%d", dbSeq.Add(1))
	memoryDB := newNamedDatabase(t, name)
	execSQL(t, memoryDB, "create extension if not exists vector")

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        memoryImage,
			ExposedPorts: []string{"8888/tcp"},
			Env: map[string]string{
				// The suite's Postgres is published on the host, so the engine reaches it
				// through the bridge gateway — the same route a task container takes to
				// reach the engine, and the reason this step adds the extra host at all.
				"HINDSIGHT_API_DATABASE_URL": throughTheHostGateway(t, memoryDB),
				// Without these two there is NO AUTHENTICATION AT ALL, for REST or for MCP.
				"HINDSIGHT_API_TENANT_EXTENSION": "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension",
				"HINDSIGHT_API_TENANT_API_KEY":   containerMemoryKey,
				// A key that cannot work. Nothing here needs extraction to succeed.
				"HINDSIGHT_API_LLM_PROVIDER":        "anthropic",
				"HINDSIGHT_API_LLM_API_KEY":         "sk-ant-not-a-real-key",
				"HINDSIGHT_API_LLM_MODEL":           "claude-opus-5",
				"HINDSIGHT_API_EMBEDDINGS_PROVIDER": "local",
				"HINDSIGHT_ENABLE_CP":               "false",
				"HINDSIGHT_API_WORKER_ID":           "hindsight-e2e",
			},
			// /health is open — no bearer — which is what makes it usable as a compose
			// healthcheck. GET / is a 404 on this service; do not probe it.
			WaitingFor: wait.ForHTTP("/health").WithPort("8888/tcp").WithStartupTimeout(3 * time.Minute),
			HostConfigModifier: func(hc *moby.HostConfig) {
				hc.ShmSize = 1 << 30
				hc.ExtraHosts = []string{"host.docker.internal:host-gateway"}
			},
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })

	endpoint, err := ctr.PortEndpoint(ctx, "8888/tcp", "http")
	require.NoError(t, err)
	t.Logf("memory engine at %s", endpoint)

	// --- what the conductor's own client sees ----------------------------------------
	client, err := memory.New(memory.Options{
		BaseURL: endpoint,
		Bank:    "podium",
		APIKey:  containerMemoryKey,
	})
	require.NoError(t, err)

	require.NoError(t, client.Ready(ctx), "Ready must probe /health, not /")

	// A bank nobody has written to is an empty page, not a 404 — which is why nothing in
	// Podium provisions a bank.
	items, next, err := client.List(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.Empty(t, next)

	// Recall of an empty bank is an empty result, not an error.
	found, err := client.Recall(ctx, "who owns the scheduler", 10)
	require.NoError(t, err)
	assert.Empty(t, found)

	// Retain is accepted. What it becomes is the engine's business; see the file comment.
	require.NoError(t, client.Retain(ctx, memory.Item{
		Content:    "Bob owns the Podium scheduler, and has since the 12.4 release.",
		Context:    "podium agent, playbook general",
		Tags:       []string{"source:slack", "playbook:general"},
		Metadata:   map[string]string{"turn_id": "turn_01e2e"},
		DocumentID: "turn_01e2e",
	}))

	// Forgetting something that is not there is a typed NotFound; a malformed id is a plain
	// error and NOT a NotFound, because the handler branches on the difference.
	require.ErrorIs(t, client.Forget(ctx, "00000000-0000-4000-8000-000000000000"), memory.ErrNotFound)
	err = client.Forget(ctx, "not-a-uuid")
	require.Error(t, err)
	assert.NotErrorIs(t, err, memory.ErrNotFound)

	// --- authentication, which is OFF unless the two variables above are set ----------
	wrong, err := memory.New(memory.Options{
		BaseURL: endpoint, Bank: "podium", APIKey: "not-the-key",
	})
	require.NoError(t, err)
	_, _, err = wrong.List(ctx, "", 10)
	require.ErrorIs(t, err, memory.ErrUnauthorized)

	status, _ := plainGet(t, endpoint+"/v1/default/banks/podium/memories/list")
	assert.Equal(t, http.StatusUnauthorized, status, "an unauthenticated REST call must be refused")

	// /health and /version stay open, which is what a compose healthcheck and an operator
	// need.
	status, _ = plainGet(t, endpoint+"/health")
	assert.Equal(t, http.StatusOK, status)

	// --- the MCP endpoint a turn connects to -----------------------------------------
	mcpURL := memory.MCPURL(endpoint, "podium")
	require.True(t, strings.HasSuffix(mcpURL, "/mcp/podium/"))

	// A POST with no bearer is refused. (A bare GET of the same path answers 200 by design
	// — it is the pre-POST probe an MCP client makes — so that is not asserted here.)
	status, _ = mcpInitialize(t, mcpURL, "")
	assert.Equal(t, http.StatusUnauthorized, status, "an unauthenticated MCP call must be refused")

	// With the bearer it initialises, which is the whole of what a turn needs to be true.
	status, body := mcpInitialize(t, mcpURL, containerMemoryKey)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "hindsight-mcp-server", "the MCP endpoint must answer initialize")
}

// execSQL runs one statement against a database in the suite's Postgres.
func execSQL(t *testing.T, databaseURL, statement string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, statement)
	require.NoError(t, err)
}

// throughTheHostGateway rewrites a host-published DSN into one a container can dial.
func throughTheHostGateway(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	require.NoError(t, err)
	u.Scheme = "postgresql"
	u.Host = "host.docker.internal:" + u.Port()
	return u.String()
}

func plainGet(t *testing.T, target string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	return res.StatusCode, string(raw)
}

func mcpInitialize(t *testing.T, target, bearer string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"podium-e2e","version":"1"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	// Streamable HTTP: the response may be a plain JSON body or a text/event-stream, and
	// an MCP client has to accept both.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, string(raw)
}
