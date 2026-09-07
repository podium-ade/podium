//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// preRename applies the schema as it stood before 0003 — skills, sessions.skill, and a
// settings row whose keys are named after the old word — and hands back the URL of a database
// holding it. It is the state every conductor that is already running is in.
func preRename(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	url := newDatabase(t)

	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	_, err = conn.Exec(ctx, `create table schema_migrations (
		version    text        primary key,
		applied_at timestamptz not null default now()
	)`)
	require.NoError(t, err)
	for _, name := range []string{"0001_init.sql", "0002_skills.sql"} {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		require.NoError(t, err)
		require.NoError(t, applyMigration(ctx, conn, name, string(body)))
	}
	return url
}

// The rename reaches the rows an operator already has: the table, the column, and the two
// keys of the profile overrides a browser wrote. The overrides matter most, because nothing
// downstream would complain about a `default_skill` — profiles.Overrides would ignore it and
// the conductor would quietly serve profile.yaml's default instead of the operator's.
func TestTheRenameMigrationCarriesWhatIsAlreadyThere(t *testing.T) {
	ctx := context.Background()
	url := preRename(t)

	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	_, err = conn.Exec(ctx,
		`insert into skills (name, definition, updated_at, updated_by) values ($1, $2, now(), $3)`,
		"analyst", []byte(`{"image":"alpine:3"}`), "alice")
	require.NoError(t, err)
	_, err = conn.Exec(ctx,
		`insert into sessions (id, source_kind, source_key, profile, skill, created_at)
		 values ($1, $2, $3, $4, $5, now())`,
		"sess_1", "slack", "C1/1.0", "podium", "analyst")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `insert into settings (key, value, updated_at) values ($1, $2, now())`,
		"profile.overrides",
		[]byte(`{"display_name":"Podium","default_skill":"general","chat_default_skill":"analyst"}`))
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	s, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.Migrate(ctx))

	rows, err := s.ListStoredPlaybooks(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "analyst", rows[0].Playbook.Name)

	sess, err := s.GetSession(ctx, "sess_1")
	require.NoError(t, err)
	require.Equal(t, "analyst", sess.Playbook, "one session, one playbook — the column moved, the value did not")

	var ov struct {
		DisplayName         string `json:"display_name"`
		DefaultPlaybook     string `json:"default_playbook"`
		ChatDefaultPlaybook string `json:"chat_default_playbook"`
		DefaultSkill        string `json:"default_skill"`
		ChatDefaultSkill    string `json:"chat_default_skill"`
	}
	require.NoError(t, s.GetSetting(ctx, "profile.overrides", &ov))
	require.Equal(t, "general", ov.DefaultPlaybook)
	require.Equal(t, "analyst", ov.ChatDefaultPlaybook)
	require.Empty(t, ov.DefaultSkill, "the old key is gone rather than kept beside the new one")
	require.Empty(t, ov.ChatDefaultSkill)
	require.Equal(t, "Podium", ov.DisplayName, "a key the rename does not touch is left alone")
}

// An override that never named a default is left exactly as it was: the rewrite must not add
// empty keys to a document that did not have them, because an empty override means "whatever
// the file says" and a present-but-empty one would read the same only by accident.
func TestTheRenameMigrationLeavesAnOverrideWithNoDefaultAlone(t *testing.T) {
	ctx := context.Background()
	url := preRename(t)

	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `insert into settings (key, value, updated_at) values ($1, $2, now())`,
		"profile.overrides", []byte(`{"display_name":"Podium"}`))
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	s, err := New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.Migrate(ctx))

	var raw map[string]any
	require.NoError(t, s.GetSetting(ctx, "profile.overrides", &raw))
	require.Equal(t, map[string]any{"display_name": "Podium"}, raw)
}
