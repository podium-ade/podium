//go:build integration

package nodes_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

func setSecret(t *testing.T, h *harness, name, value string) *podiumv1.Secret {
	t.Helper()
	res, err := h.secrets.SetSecret(context.Background(), connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: name, Value: []byte(value),
	}))
	require.NoError(t, err)
	return res.Msg.GetSecret()
}

func TestSecretServiceRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	first := setSecret(t, h, "GREETING", "hello from the secret store")
	assert.EqualValues(t, 1, first.GetVersion())
	assert.Equal(t, "local", first.GetCreatedBy(), "the actor is the transport identity")
	assert.NotEmpty(t, first.GetKeyId())
	assert.False(t, first.GetUpdatedAt().AsTime().IsZero())

	second := setSecret(t, h, "GREETING", "a replacement value")
	assert.EqualValues(t, 2, second.GetVersion())

	list, err := h.secrets.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetSecrets(), 1)
	assert.Equal(t, "GREETING", list.Msg.GetSecrets()[0].GetName())

	// There is no read endpoint at all: the only field that could carry a value does not
	// exist on podium.v1.Secret.
	assert.Nil(t, list.Msg.GetSecrets()[0].ProtoReflect().Descriptor().Fields().ByName("value"),
		"podium.v1.Secret must never gain a value field")

	_, err = h.secrets.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{Name: "GREETING"}))
	require.NoError(t, err)

	list, err = h.secrets.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetSecrets())
}

func TestSecretServiceMapsErrorsToConnectCodes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	_, err := h.secrets.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: "not a name", Value: []byte("value"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = h.secrets.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{Name: "EMPTY"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = h.secrets.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{Name: "ABSENT"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestSecretServiceNeedsAToken(t *testing.T) {
	h := newHarness(t)
	anonymous := podiumv1connect.NewSecretServiceClient(http.DefaultClient, h.url)

	_, err := anonymous.ListSecrets(context.Background(), connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// failureReason reads tasks.failure_reason, which has no wire representation: the column
// carries why a task could not run and the API deliberately does not.
func failureReason(t *testing.T, h *harness, taskID string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var reason *string
	require.NoError(t, conn.QueryRow(ctx, "select failure_reason from tasks where id = $1", taskID).Scan(&reason))
	require.NotNil(t, reason, "task %s has no failure_reason", taskID)
	return *reason
}

// The values reach the node inside the Assign, and nowhere else: they are not on the task's
// spec, so nothing that reads a task back can see them.
func TestResolvedSecretsTravelInTheAssign(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	setSecret(t, h, "GREETING", "hello from the secret store")
	setSecret(t, h, "DEPLOY_KEY", "-----BEGIN OPENSSH PRIVATE KEY-----")

	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	created, err := h.tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{
			Image:   "alpine:3",
			Command: []string{"true"},
			Secrets: []*podiumv1.SecretRef{
				{Name: "GREETING", Target: "env", Key: "GREETING"},
				{Name: "DEPLOY_KEY", Target: "file", Key: "/podium/secrets/deploy_key"},
			},
		},
	}))
	require.NoError(t, err)
	task := created.Msg.GetTask()

	assign := node.awaitAssign(assignTimeout)
	require.Equal(t, task.GetId(), assign.GetTaskId())
	require.Len(t, assign.GetResolvedSecrets(), 2)

	byName := map[string]*podiumv1.ResolvedSecret{}
	for _, s := range assign.GetResolvedSecrets() {
		byName[s.GetName()] = s
	}
	assert.Equal(t, "hello from the secret store", string(byName["GREETING"].GetValue()))
	assert.Equal(t, "env", byName["GREETING"].GetTarget())
	assert.Equal(t, "GREETING", byName["GREETING"].GetKey())
	assert.Equal(t, "-----BEGIN OPENSSH PRIVATE KEY-----", string(byName["DEPLOY_KEY"].GetValue()))
	assert.Equal(t, "/podium/secrets/deploy_key", byName["DEPLOY_KEY"].GetKey())

	// The spec that travels with the assignment, and the one every reader of the task
	// sees, carries names only.
	require.Len(t, assign.GetSpec().GetSecrets(), 2)
	assert.Equal(t, "GREETING", assign.GetSpec().GetSecrets()[0].GetName())

	readBack := h.getTask(task.GetId())
	require.Len(t, readBack.GetSpec().GetSecrets(), 2)
	assert.NotContains(t, readBack.String(), "hello from the secret store",
		"a task read back must never carry a secret value")

	// RedactForLog is what every log site uses, and it must strip exactly this.
	assert.Empty(t, podiumv1.RedactForLog(assign).GetResolvedSecrets())
}

// The acceptance item: a missing secret fails the task before any node sees it, and the
// failure reason names the secret.
func TestAMissingSecretFailsTheTaskBeforeAnyNodeSeesIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	setSecret(t, h, "PRESENT", "a value that exists")

	created, err := h.tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{
			Image:   "alpine:3",
			Command: []string{"true"},
			Secrets: []*podiumv1.SecretRef{
				{Name: "PRESENT", Target: "env", Key: "PRESENT"},
				{Name: "ABSENT", Target: "env", Key: "ABSENT"},
			},
		},
	}))
	require.NoError(t, err)
	taskID := created.Msg.GetTask().GetId()

	failed := h.awaitTaskStatus(taskID, podiumv1.TaskStatus_TASK_STATUS_FAILED, assignTimeout)
	assert.Empty(t, failed.GetNodeId(), "the task must never have been assigned to a node")
	assert.Nil(t, failed.ExitCode, "a task that never ran has no exit code")
	assert.EqualValues(t, 0, failed.GetAttempts(), "a missing secret does not burn an attempt")
	assert.NotNil(t, failed.GetFinishedAt())

	reason := failureReason(t, h, taskID)
	assert.Contains(t, reason, "ABSENT", "failure_reason must name the secret")
	assert.Contains(t, reason, "missing secret")

	// No node was ever told about it.
	node.expectNoAssign(2 * time.Second)

	// A task with a secret that does exist still runs on the same server.
	ok, err := h.tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{
			Image:   "alpine:3",
			Command: []string{"true"},
			Secrets: []*podiumv1.SecretRef{{Name: "PRESENT", Target: "env", Key: "PRESENT"}},
		},
	}))
	require.NoError(t, err)
	assign := node.awaitAssign(assignTimeout)
	assert.Equal(t, ok.Msg.GetTask().GetId(), assign.GetTaskId())
}

// A deleted secret is a missing secret for every task queued afterwards.
func TestDeletingASecretFailsLaterTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	node := enrollNode(t, h, "node-a", nil)
	node.open(ctx)
	defer node.disconnect()
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	setSecret(t, h, "TEMPORARY", "a value about to be deleted")
	_, err := h.secrets.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{Name: "TEMPORARY"}))
	require.NoError(t, err)

	created, err := h.tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{
			Image:   "alpine:3",
			Command: []string{"true"},
			Secrets: []*podiumv1.SecretRef{{Name: "TEMPORARY", Target: "env", Key: "TEMPORARY"}},
		},
	}))
	require.NoError(t, err)

	h.awaitTaskStatus(created.Msg.GetTask().GetId(), podiumv1.TaskStatus_TASK_STATUS_FAILED, assignTimeout)
	assert.Contains(t, failureReason(t, h, created.Msg.GetTask().GetId()), "TEMPORARY")
}

// CreateTask validates the refs, so a malformed one never reaches the queue.
func TestCreateTaskRejectsABadSecretRef(t *testing.T) {
	h := newHarness(t)

	_, err := h.tasks.CreateTask(context.Background(), connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: &podiumv1.TaskSpec{
			Image:   "alpine:3",
			Command: []string{"true"},
			Secrets: []*podiumv1.SecretRef{{Name: "A", Target: "vault", Key: "A"}},
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "is not a secret target")
}
