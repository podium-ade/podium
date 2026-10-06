//go:build integration

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestAPreScopeRowStaysAGlobalTheConductorCanRead inserts a secret the way the table
// looked before scopes existed, applies 0012, and resolves it as a global. The migration
// adds columns; it must not rewrite ciphertext.
func TestAPreScopeRowStaysAGlobalTheConductorCanRead(t *testing.T) {
	ctx := context.Background()
	dsn := newDatabaseURL(t)
	st, err := store.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)

	key, err := GenerateKey()
	require.NoError(t, err)
	const name = "LEGACY"
	const plaintext = "still the same value"
	ciphertext, nonce, err := key.Encrypt(name, []byte(plaintext))
	require.NoError(t, err)

	applyMigrationsBeforeScope(t, dsn)
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `
		insert into secrets (name, ciphertext, nonce, version, key_id, created_by)
		values ($1, $2, $3, 1, $4, 'migration')`,
		name, ciphertext, nonce, key.ID())
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	require.NoError(t, st.Migrate(ctx))

	conn, err = pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	var scope, owner string
	var stored []byte
	require.NoError(t, conn.QueryRow(ctx,
		"select scope, owner, ciphertext from secrets where name = $1", name).Scan(&scope, &owner, &stored))
	require.NoError(t, conn.Close(ctx))
	require.Equal(t, store.SecretScopeGlobal, scope)
	require.Empty(t, owner)
	require.Equal(t, ciphertext, stored)

	svc := New(st, key, nil)
	got, err := svc.Resolve(ctx, "task", []spec.SecretRef{{Name: name, Target: spec.SecretTargetEnv, Key: name}})
	require.NoError(t, err)
	require.Equal(t, plaintext, string(got[0].Value))
}

func applyMigrationsBeforeScope(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Join(filepath.Dir(file), "..", "store", "migrations")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `create table if not exists schema_migrations (
		version text primary key,
		applied_at timestamptz not null default now()
	)`)
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		if name == "0012_secret_scope.sql" || entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, "insert into schema_migrations (version) values ($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record %s: %v", name, err)
		}
		require.NoError(t, tx.Commit(ctx))
	}
}
