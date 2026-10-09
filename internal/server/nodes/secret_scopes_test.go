//go:build integration

package nodes_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server"
	"github.com/podium-ade/podium/internal/server/api"
	"github.com/podium-ade/podium/internal/server/auth"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

// TestSecretScopesOnTheRealAPI drives Set, List, Delete, and CreateTask through the
// running server. A session cookie is a signed-in person. The dev token and the conductor
// bearer are the other authenticated callers. A node has no CreateTask bearer on the local
// transport (it presents the dev token until Hello), so that refusal calls the same
// CreateTask handler with the KindNode identity the stream would carry.
func TestSecretScopesOnTheRealAPI(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, func(cfg *server.Config) {
		cfg.GoogleClientID = "test-client"
		cfg.GoogleClientSecret = "test-secret"
	})
	st, err := store.New(ctx, h.databaseURL)
	require.NoError(t, err)
	t.Cleanup(st.Close)

	require.NoError(t, mustUser(t, st, "owner@acme.com", store.RoleOwner))
	_, err = st.ClaimInstance(ctx, "owner@acme.com", "acme.com")
	require.NoError(t, err)
	require.NoError(t, mustUser(t, st, "admin@acme.com", store.RoleAdmin))
	require.NoError(t, mustUser(t, st, "ada@acme.com", store.RoleMember))
	require.NoError(t, mustUser(t, st, "bob@acme.com", store.RoleMember))

	owner := secretClient(t, h, sessionClient(t, st, "owner@acme.com"))
	admin := secretClient(t, h, sessionClient(t, st, "admin@acme.com"))
	ada := secretClient(t, h, sessionClient(t, st, "ada@acme.com"))
	bob := secretClient(t, h, sessionClient(t, st, "bob@acme.com"))
	dev := h.secrets
	agent := podiumv1connect.NewSecretServiceClient(h2cClient(agentToken), h.url)

	adaTasks := podiumv1connect.NewTaskServiceClient(sessionClient(t, st, "ada@acme.com"), h.url)
	bobTasks := podiumv1connect.NewTaskServiceClient(sessionClient(t, st, "bob@acme.com"), h.url)
	adminTasks := podiumv1connect.NewTaskServiceClient(sessionClient(t, st, "admin@acme.com"), h.url)
	ownerTasks := podiumv1connect.NewTaskServiceClient(sessionClient(t, st, "owner@acme.com"), h.url)

	setGlobal(t, dev, "COMPANY")
	setGlobal(t, agent, "CONDUCTOR_ONLY")
	setGlobal(t, admin, "FROM_ADMIN")
	setGlobal(t, owner, "FROM_OWNER")

	_, err = ada.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "FROM_ADMIN", Value: []byte("nope"), Scope: podiumv1.SecretScope_SECRET_SCOPE_GLOBAL,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = dev.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "ADA_NOTE", Value: []byte("dev-personal"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = agent.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "ADA_NOTE", Value: []byte("agent-personal"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	adaNote := setPersonal(t, ada, "ADA_NOTE", "ada-value")
	require.Equal(t, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, adaNote.GetScope())
	require.Equal(t, "ada@acme.com", adaNote.GetOwner())
	require.NotContains(t, adaNote.String(), "ada-value")
	setPersonal(t, bob, "BOB_NOTE", "bob-value")
	setPersonal(t, owner, "OWNER_NOTE", "owner-value")

	_, err = ada.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "COMPANY", Value: []byte("clash"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	_, err = admin.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "ADA_NOTE", Value: []byte("clash"), Scope: podiumv1.SecretScope_SECRET_SCOPE_GLOBAL,
	}))
	require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	_, err = ada.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "podium.agent.github_token", Value: []byte("nope"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ada.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "podium.agent", Value: []byte("nope"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	globals := []string{"COMPANY", "CONDUCTOR_ONLY", "FROM_ADMIN", "FROM_OWNER"}
	assertVisible(t, ada, append(append([]string{}, globals...), "ADA_NOTE")...)
	assertVisible(t, bob, append(append([]string{}, globals...), "BOB_NOTE")...)
	assertVisible(t, admin, globals...)
	assertVisible(t, owner, append(append([]string{}, globals...), "OWNER_NOTE")...)
	assertVisible(t, dev, globals...)
	assertVisible(t, agent, globals...)

	list, err := ada.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	for _, row := range list.Msg.GetSecrets() {
		require.NotContains(t, row.String(), "ada-value")
		require.NotContains(t, row.String(), "bob-value")
		require.Nil(t, row.ProtoReflect().Descriptor().Fields().ByName("value"))
	}

	_, err = admin.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{
		Name: "ADA_NOTE", Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assertVisible(t, ada, "COMPANY", "CONDUCTOR_ONLY", "FROM_ADMIN", "FROM_OWNER", "ADA_NOTE")

	_, err = ada.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{
		Name: "COMPANY", Scope: podiumv1.SecretScope_SECRET_SCOPE_GLOBAL,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = ada.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{
		Name: "ADA_NOTE", Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.NoError(t, err)
	_, err = dev.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{
		Name: "FROM_ADMIN", Scope: podiumv1.SecretScope_SECRET_SCOPE_GLOBAL,
	}))
	require.NoError(t, err)

	sets, err := st.ListAudit(ctx, store.ActionSecretSet, "ADA_NOTE", 10)
	require.NoError(t, err)
	require.NotEmpty(t, sets)
	require.Equal(t, "personal", sets[0].Details["scope"])
	require.Equal(t, "ada@acme.com", sets[0].Details["owner"])
	require.NotContains(t, sets[0].Details, "ada-value")

	before := taskCount(t, h.databaseURL)
	refused := secretSpec("COMPANY", "")
	for _, client := range []podiumv1connect.TaskServiceClient{h.tasks, adminTasks, adaTasks, ownerTasks} {
		_, err = client.CreateTask(ctx, connect.NewRequest(refused))
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "the conductor attaches that secret")
	}
	nodeTasks := api.NewTaskService(st, nil, nil, nil, nil)
	nodeCtx := transport.NewContext(ctx, transport.Identity{Kind: transport.KindNode, Login: "node-a"})
	_, err = nodeTasks.CreateTask(nodeCtx, connect.NewRequest(refused))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.ErrorContains(t, err, "the conductor attaches that secret")
	require.Equal(t, before, taskCount(t, h.databaseURL))

	_, err = adaTasks.CreateTask(ctx, connect.NewRequest(secretSpec("BOB_NOTE", "bob@acme.com")))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = h.tasks.CreateTask(ctx, connect.NewRequest(secretSpec("BOB_NOTE", "bob@acme.com")))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.ErrorContains(t, err, "is personal")
	require.Equal(t, before, taskCount(t, h.databaseURL))

	setPersonal(t, ada, "ADA_NOTE", "ada-value")
	accepted, err := adaTasks.CreateTask(ctx, connect.NewRequest(secretSpec("ADA_NOTE", "ada@acme.com")))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, accepted.Msg.GetTask().GetStatus())
	require.Equal(t, "ada@acme.com", accepted.Msg.GetTask().GetSpec().GetSecrets()[0].GetOwner())

	accepted, err = h.agentTasks().CreateTask(ctx, connect.NewRequest(secretSpec("ADA_NOTE", "ada@acme.com")))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, accepted.Msg.GetTask().GetStatus())

	accepted, err = h.agentTasks().CreateTask(ctx, connect.NewRequest(secretSpec("COMPANY", "")))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_QUEUED, accepted.Msg.GetTask().GetStatus())
	require.Empty(t, accepted.Msg.GetTask().GetSpec().GetSecrets()[0].GetOwner())

	beforeMissing := taskCount(t, h.databaseURL)
	_, err = h.agentTasks().CreateTask(ctx, connect.NewRequest(secretSpec("MISSING_PERSONAL", "ada@acme.com")))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.ErrorContains(t, err, "MISSING_PERSONAL")
	_, err = bobTasks.CreateTask(ctx, connect.NewRequest(secretSpec("MISSING_PERSONAL", "bob@acme.com")))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.ErrorContains(t, err, "MISSING_PERSONAL")
	require.Equal(t, beforeMissing, taskCount(t, h.databaseURL), "a missing personal secret creates no task")

	created, err := h.agentTasks().CreateTask(ctx, connect.NewRequest(secretSpec("MISSING_GLOBAL", "")))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_FAILED, created.Msg.GetTask().GetStatus())
	require.ErrorContains(t, errOrReason(created.Msg.GetTask().GetFailureReason()), "MISSING_GLOBAL")

	// The personal secret exists. The global one does not. The failed row is still the
	// record of the missing global, and the personal name does not turn that into NotFound.
	mixed, err := h.agentTasks().CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{Spec: &podiumv1.TaskSpec{
		Image:   "alpine:3",
		Command: []string{"true"},
		Secrets: []*podiumv1.SecretRef{
			{Name: "ADA_NOTE", Target: "env", Key: "ADA_NOTE", Owner: "ada@acme.com"},
			{Name: "MISSING_GLOBAL", Target: "env", Key: "MISSING_GLOBAL"},
		},
	}}))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_FAILED, mixed.Msg.GetTask().GetStatus())
	require.ErrorContains(t, errOrReason(mixed.Msg.GetTask().GetFailureReason()), "MISSING_GLOBAL")
	require.NotEqual(t, connect.CodeNotFound, connect.CodeOf(err))

	// A server with no master key reports ErrNoKey. That is not a missing personal secret,
	// so the task row exists and the response is not NotFound.
	disabled := api.NewTaskService(st, nil, nil, secrets.New(st, nil, nil), nil)
	agentCtx := transport.NewContext(ctx, transport.Identity{Kind: transport.KindAgent, Login: "agent"})
	nokey, err := disabled.CreateTask(agentCtx, connect.NewRequest(secretSpec("ADA_NOTE", "ada@acme.com")))
	require.NoError(t, err)
	require.Equal(t, podiumv1.TaskStatus_TASK_STATUS_FAILED, nokey.Msg.GetTask().GetStatus())
	require.ErrorContains(t, errOrReason(nokey.Msg.GetTask().GetFailureReason()), "no master key")

	// The conductor stores a person's MCP token on that person's list. It cannot write
	// any other personal name, and a list that names them returns the credential without
	// their other secrets. The dev token still cannot.
	_, err = agent.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "ADA_NOTE", Value: []byte("nope"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, Owner: "ada@acme.com",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	stored, err := agent.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "mcp.linear_token", Value: []byte("lin"), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, Owner: "ada@acme.com",
	}))
	require.NoError(t, err)
	require.Equal(t, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, stored.Msg.GetSecret().GetScope())
	require.Equal(t, "ada@acme.com", stored.Msg.GetSecret().GetOwner())

	adaList, err := ada.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	var sawMCP bool
	for _, row := range adaList.Msg.GetSecrets() {
		if row.GetName() == "mcp.linear_token" {
			sawMCP = true
			require.Equal(t, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, row.GetScope())
			require.Equal(t, "ada@acme.com", row.GetOwner())
		}
	}
	require.True(t, sawMCP)
	bobList, err := bob.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	for _, row := range bobList.Msg.GetSecrets() {
		require.NotEqual(t, "mcp.linear_token", row.GetName())
	}
	agentList, err := agent.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	for _, row := range agentList.Msg.GetSecrets() {
		require.NotEqual(t, "mcp.linear_token", row.GetName())
	}
	agentAda, err := agent.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{Owner: "ada@acme.com"}))
	require.NoError(t, err)
	var agentSawMCP, agentSawNote bool
	for _, row := range agentAda.Msg.GetSecrets() {
		if row.GetName() == "mcp.linear_token" {
			agentSawMCP = true
		}
		if row.GetName() == "ADA_NOTE" {
			agentSawNote = true
		}
	}
	require.True(t, agentSawMCP)
	require.False(t, agentSawNote)
	_, err = dev.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{Owner: "ada@acme.com"}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func errOrReason(reason string) error { return stringError(reason) }

type stringError string

func (e stringError) Error() string { return string(e) }

func mustUser(t *testing.T, st *store.Store, login, role string) error {
	t.Helper()
	ctx := context.Background()
	if _, err := st.UpsertUser(ctx, login, login); err != nil {
		return err
	}
	_, err := st.SetUserRoles(ctx, login, []string{role})
	return err
}

func sessionClient(t *testing.T, st *store.Store, login string) *http.Client {
	t.Helper()
	token, err := st.CreateSession(context.Background(), login, 0)
	require.NoError(t, err)
	return cookieClient(token)
}

func cookieClient(token string) *http.Client {
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: cookieRoundTripper{token: token, rt: tr}}
}

type cookieRoundTripper struct {
	rt    http.RoundTripper
	token string
}

func (c cookieRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.token})
	return c.rt.RoundTrip(r)
}

func secretClient(t *testing.T, h *harness, httpClient *http.Client) podiumv1connect.SecretServiceClient {
	t.Helper()
	return podiumv1connect.NewSecretServiceClient(httpClient, h.url)
}

func setGlobal(t *testing.T, client podiumv1connect.SecretServiceClient, name string) {
	t.Helper()
	_, err := client.SetSecret(context.Background(), connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: name, Value: []byte("global-" + name), Scope: podiumv1.SecretScope_SECRET_SCOPE_GLOBAL,
	}))
	require.NoError(t, err)
}

func setPersonal(t *testing.T, client podiumv1connect.SecretServiceClient, name, value string) *podiumv1.Secret {
	t.Helper()
	res, err := client.SetSecret(context.Background(), connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: name, Value: []byte(value), Scope: podiumv1.SecretScope_SECRET_SCOPE_PERSONAL,
	}))
	require.NoError(t, err)
	return res.Msg.GetSecret()
}

func assertVisible(t *testing.T, client podiumv1connect.SecretServiceClient, names ...string) {
	t.Helper()
	res, err := client.ListSecrets(context.Background(), connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	got := make([]string, 0, len(res.Msg.GetSecrets()))
	for _, row := range res.Msg.GetSecrets() {
		got = append(got, row.GetName())
		require.NotContains(t, row.String(), "global-")
		require.NotContains(t, row.String(), "-value")
	}
	require.Equal(t, names, got)
}

func secretSpec(name, owner string) *podiumv1.CreateTaskRequest {
	return &podiumv1.CreateTaskRequest{Spec: &podiumv1.TaskSpec{
		Image:   "alpine:3",
		Command: []string{"true"},
		Secrets: []*podiumv1.SecretRef{{Name: name, Target: "env", Key: "SECRET_KEY", Owner: owner}},
	}}
}

func taskCount(t *testing.T, dsn string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var n int
	require.NoError(t, conn.QueryRow(ctx, "select count(*) from tasks").Scan(&n))
	return n
}
