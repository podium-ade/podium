//go:build integration

package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postgresImage is pgvector's build of Postgres 16, which is where the whole repository is
// going: step 19 puts Hindsight on Podium's own database and it needs the vector extension.
// This package moves first because it has no existing fixtures to break.
const postgresImage = "pgvector/pgvector:pg16"

// One container for the whole package; every test gets its own database inside it.
var (
	adminURL string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("podium_agent"),
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
	name := fmt.Sprintf("podium_agent_test_%d", dbSeq.Add(1))

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
