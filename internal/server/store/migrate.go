package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockKey is the session-level advisory lock two servers starting at once contend
// on, so migrations are applied exactly once. The value is arbitrary but must never change.
const migrationLockKey int64 = 7148506201021931521

// Migrate applies every embedded migration that is not already recorded in schema_migrations.
// It is idempotent and safe to run concurrently from several processes: an advisory lock
// serialises the runs and each file is applied inside its own transaction.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, "select pg_advisory_unlock($1)", migrationLockKey); err != nil {
			slog.Warn("release migration lock", "error", err)
		}
	}()

	const createTable = `create table if not exists schema_migrations (
		version    text        primary key,
		applied_at timestamptz not null default now()
	)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		row := conn.QueryRow(ctx, "select exists (select 1 from schema_migrations where version = $1)", name)
		if err := row.Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, conn.Conn(), name, string(body)); err != nil {
			return err
		}
		slog.Info("applied migration", "version", name)
	}
	return nil
}

// applyMigration runs one file and records it, atomically. The statements go through the
// simple query protocol (pgx uses it when Exec has no arguments), which is what lets a single
// call carry several statements.
func applyMigration(ctx context.Context, conn *pgx.Conn, name, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "insert into schema_migrations (version) values ($1)", name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
