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

// migrationLockKey is the session-level advisory lock two conductors starting at once
// contend on, so migrations are applied exactly once. The value is arbitrary but must never
// change. It is deliberately not the control plane's key: the two schemas live in different
// databases, and an advisory lock is per-database anyway, but sharing a constant between two
// unrelated schemas is the kind of coincidence that stops being one.
const migrationLockKey int64 = 7148506201021931522

// Migrate applies every embedded migration that is not already recorded in
// schema_migrations. It is idempotent and safe to run concurrently from several processes.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire agent migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("take agent migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, "select pg_advisory_unlock($1)", migrationLockKey); err != nil {
			slog.Warn("release agent migration lock", "error", err)
		}
	}()

	const createTable = `create table if not exists schema_migrations (
		version    text        primary key,
		applied_at timestamptz not null default now()
	)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create agent schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		row := conn.QueryRow(ctx, "select exists (select 1 from schema_migrations where version = $1)", name)
		if err := row.Scan(&applied); err != nil {
			return fmt.Errorf("check agent migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read agent migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, conn.Conn(), name, string(body)); err != nil {
			return err
		}
		slog.Info("applied agent migration", "version", name)
	}
	return nil
}

// applyMigration runs one file and records it, atomically.
func applyMigration(ctx context.Context, conn *pgx.Conn, name, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agent migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply agent migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "insert into schema_migrations (version) values ($1)", name); err != nil {
		return fmt.Errorf("record agent migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit agent migration %s: %w", name, err)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded agent migrations: %w", err)
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
