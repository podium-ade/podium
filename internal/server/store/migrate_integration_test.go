//go:build integration

package store

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var expectedTables = []string{
	"schema_migrations", "nodes", "enrollment_tokens", "tasks", "task_events", "task_log_chunks",
	// users arrives with 0002_tailnet.sql: the tailnet transport records a login the first
	// time it sees one.
	"users",
	// secrets and audit_log arrive with 0003_secrets.sql.
	"secrets", "audit_log",
	// artifacts was trimmed out of 0001_init.sql for MVP-0 and arrives with
	// 0005_artifacts.sql, kind column and all.
	"artifacts",
}

// migrationFiles is every migration this build carries, in the order Migrate applies them.
var migrationFiles = []string{
	"0001_init.sql", "0002_tailnet.sql", "0003_secrets.sql", "0004_scheduler.sql", "0005_artifacts.sql",
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var reg *string
	err := s.pool.QueryRow(context.Background(), "select to_regclass('public.'||$1)::text", name).Scan(&reg)
	require.NoError(t, err)
	return reg != nil
}

func TestMigrateFromEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, newDatabase(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	for _, name := range expectedTables {
		require.False(t, tableExists(t, s, name), "%s must not exist before Migrate", name)
	}

	require.NoError(t, s.Migrate(ctx))

	for _, name := range expectedTables {
		require.True(t, tableExists(t, s, name), "%s must exist after Migrate", name)
	}
	var versions []string
	rows, err := s.pool.Query(ctx, "select version from schema_migrations order by version")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		versions = append(versions, v)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, migrationFiles, versions)
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t) // already migrated once
	require.NoError(t, s.Migrate(ctx))
	require.NoError(t, s.Migrate(ctx))

	var n int
	require.NoError(t, s.pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&n))
	require.Equal(t, len(migrationFiles), n)
}

func TestMigrateIsConcurrencySafe(t *testing.T) {
	ctx := context.Background()
	url := newDatabase(t)

	const racers = 5
	stores := make([]*Store, racers)
	for i := range stores {
		s, err := New(ctx, url)
		require.NoError(t, err)
		t.Cleanup(s.Close)
		stores[i] = s
	}

	errs := make([]error, racers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range stores {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			errs[i] = stores[i].Migrate(ctx)
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		require.NoError(t, err, "racer %d", i)
	}
	var n int
	require.NoError(t, stores[0].pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&n))
	require.Equal(t, len(migrationFiles), n)
	for _, name := range expectedTables {
		require.True(t, tableExists(t, stores[0], name))
	}
}

func TestPingReportsReachability(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Ping(context.Background()))
}

// 0001_init.sql is already applied in real databases and the runner tracks applied files by
// name, so it must never be edited: a change there would silently never run. This asserts the
// set of migration files matches what the tests above expect, which is the cheapest way to make
// someone adding 0003 notice.
func TestEveryEmbeddedMigrationIsAccountedFor(t *testing.T) {
	names, err := migrationNames()
	require.NoError(t, err)
	require.Equal(t, migrationFiles, names)
}
