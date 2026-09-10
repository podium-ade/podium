//go:build integration

package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/podium-ade/podium/pkg/spec"
)

// One Postgres 16 container for the whole package; every test gets its own database inside it.
var (
	adminURL string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg16",
		postgres.WithDatabase("podium"),
		postgres.WithUsername("podium"),
		postgres.WithPassword("podium"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		os.Exit(1)
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(ctr)
		os.Exit(1)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
	}
	os.Exit(code)
}

// newDatabase creates an empty database and returns its URL.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_test_%d", dbSeq.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// newStore returns a migrated store on a database of its own.
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), newDatabase(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.Migrate(context.Background()))
	return s
}

func testSpec() spec.TaskSpec {
	s := spec.TaskSpec{
		Image:   "alpine:3",
		Command: []string{"sh", "-c", "echo hi"},
		Env:     map[string]string{"FOO": "bar"},
		Labels:  []string{"linux/arm64"},
	}
	s.ApplyDefaults()
	return s
}

// mustCreateTask inserts a queued task with the given max_attempts (0 -> spec default).
func mustCreateTask(t *testing.T, s *Store, maxAttempts int32) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), NewTask{
		Spec:        testSpec(),
		RequestedBy: "dev",
		MaxAttempts: maxAttempts,
	})
	require.NoError(t, err)
	return task
}

// driveTo walks a freshly queued task along the happy path until it sits in target.
func driveTo(t *testing.T, s *Store, taskID string, target Status) Task {
	t.Helper()
	ctx := context.Background()
	path := []Status{StatusScheduled, StatusProvisioning, StatusRunning}
	cur, err := s.GetTask(ctx, taskID)
	require.NoError(t, err)
	if target == StatusQueued {
		return cur
	}
	for _, next := range path {
		cur, err = s.TransitionTask(ctx, taskID, nil, next, Patch{})
		require.NoError(t, err)
		if next == target {
			return cur
		}
	}
	// target is terminal: take the last hop from running.
	cur, err = s.TransitionTask(ctx, taskID, nil, target, Patch{})
	require.NoError(t, err)
	return cur
}

func mustCreateNode(t *testing.T, s *Store, name string, labels []string) Node {
	t.Helper()
	n, err := s.CreateNode(context.Background(), NewNode{
		Name:        name,
		Tags:        []string{"dev"},
		Labels:      labels,
		Capacity:    NodeCapacity{MaxTasks: 4, CPUCores: 8, MemoryMB: 16384},
		NodeKeyHash: HashToken("node-key-" + name),
		Status:      NodeOnline,
		Version:     "v0.0.1",
	})
	require.NoError(t, err)
	return n
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrInt32(v int32) *int32        { return &v }
func ptrString(v string) *string     { return &v }
