//go:build integration

// Package secrets' integration suite proves the properties the step exists for: a
// plaintext never reaches Postgres, the wrong master key is an error rather than garbage,
// and rotating the key re-encrypts everything.
package secrets

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

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

func newDatabaseURL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_secrets_%d", dbSeq.Add(1))

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

// newService returns a migrated store, its URL and a service holding a fresh master key.
func newService(t *testing.T) (*store.Store, string, *Service, *Key) {
	t.Helper()
	ctx := context.Background()
	dsn := newDatabaseURL(t)
	st, err := store.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.NoError(t, st.Migrate(ctx))

	key, err := GenerateKey()
	require.NoError(t, err)
	return st, dsn, New(st, key, nil), key
}

// rawColumn reads one row's ciphertext straight out of Postgres, bypassing this package.
func rawColumn(t *testing.T, dsn, name string) []byte {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var ciphertext []byte
	require.NoError(t, conn.QueryRow(ctx, "select ciphertext from secrets where name = $1", name).Scan(&ciphertext))
	return ciphertext
}

func TestSetListDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, _, svc, key := newService(t)

	row, err := svc.Set(ctx, "alice", "GREETING", []byte("hello from the secret store"))
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Version)
	assert.Equal(t, key.ID(), row.KeyID)

	list, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "GREETING", list[0].Name)
	assert.Nil(t, list[0].Ciphertext, "List must not hand out ciphertext")
	assert.Nil(t, list[0].Nonce)

	require.NoError(t, svc.Delete(ctx, "alice", "GREETING"))
	list, err = svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSetRejectsBadNamesAndEmptyValues(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)

	_, err := svc.Set(ctx, "alice", "not a name", []byte("value"))
	require.ErrorIs(t, err, ErrInvalidSecret)

	_, err = svc.Set(ctx, "alice", "GREETING", nil)
	require.ErrorIs(t, err, ErrInvalidSecret)
}

func TestSetScrubsTheCallersPlaintext(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)

	value := []byte("hunter2-hunter2-hunter2")
	_, err := svc.Set(ctx, "alice", "GREETING", value)
	require.NoError(t, err)
	assert.Equal(t, make([]byte, len(value)), value, "Set zeroes the slice it was handed")
}

// The acceptance item: the stored ciphertext never contains the plaintext, checked over a
// hex dump exactly as an operator would with psql.
func TestCiphertextNeverContainsThePlaintext(t *testing.T) {
	ctx := context.Background()
	_, dsn, svc, _ := newService(t)

	const plaintext = "correct-horse-battery-staple"
	_, err := svc.Set(ctx, "alice", "GREETING", []byte(plaintext))
	require.NoError(t, err)

	ciphertext := rawColumn(t, dsn, "GREETING")
	require.NotEmpty(t, ciphertext)
	assert.NotContains(t, string(ciphertext), plaintext, "the plaintext is in the ciphertext column")
	assert.NotContains(t, hex.EncodeToString(ciphertext), hex.EncodeToString([]byte(plaintext)),
		"the plaintext is in the hex dump of the ciphertext column")
	// GCM adds a 16-byte tag and nothing else, so a leak would show as a length match.
	assert.Equal(t, len(plaintext)+16, len(ciphertext))
	assert.False(t, bytes.Contains(ciphertext, []byte("horse")))
}

func TestResolveReturnsTheValueAndAuditsTheNames(t *testing.T) {
	ctx := context.Background()
	st, _, svc, _ := newService(t)

	_, err := svc.Set(ctx, "alice", "GREETING", []byte("hello world hello"))
	require.NoError(t, err)
	_, err = svc.Set(ctx, "alice", "DB_PASSWORD", []byte("hunter2-hunter2"))
	require.NoError(t, err)

	got, err := svc.Resolve(ctx, "task_1", []spec.SecretRef{
		{Name: "GREETING", Target: "env", Key: "GREETING"},
		{Name: "DB_PASSWORD", Target: "file", Key: "/podium/secrets/db"},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []byte("hello world hello"), got[0].Value)
	assert.Equal(t, "env", got[0].Target)
	assert.Equal(t, []byte("hunter2-hunter2"), got[1].Value)
	assert.Equal(t, "/podium/secrets/db", got[1].Key)

	entries, err := st.ListAudit(ctx, store.ActionSecretResolve, "task_1", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, []any{"DB_PASSWORD", "GREETING"}, entries[0].Details["names"])
	assert.NotContains(t, fmt.Sprint(entries[0].Details), "hunter2", "an audit row never carries a value")
}

func TestResolveFailsTheWholeTaskOnOneMissingName(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)

	_, err := svc.Set(ctx, "alice", "PRESENT", []byte("a value that exists"))
	require.NoError(t, err)

	_, err = svc.Resolve(ctx, "task_1", []spec.SecretRef{
		{Name: "PRESENT", Target: "env", Key: "PRESENT"},
		{Name: "ABSENT", Target: "env", Key: "ABSENT"},
	})
	require.ErrorIs(t, err, ErrMissing)
	assert.ErrorContains(t, err, `"ABSENT"`, "the error names the secret so failure_reason is actionable")
}

func TestResolveWithNoRefsIsANoOp(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)

	got, err := svc.Resolve(ctx, "task_1", nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// A server with no master key is loudly useless rather than quietly unsafe.
func TestDisabledServiceRefusesEverything(t *testing.T) {
	ctx := context.Background()
	st, _, _, _ := newService(t)
	disabled := New(st, nil, nil)

	assert.False(t, disabled.Enabled())
	assert.Empty(t, disabled.KeyID())

	_, err := disabled.Set(ctx, "alice", "GREETING", []byte("value"))
	require.ErrorIs(t, err, ErrNoKey)

	_, err = disabled.Resolve(ctx, "task_1", []spec.SecretRef{{Name: "GREETING", Target: "env", Key: "GREETING"}})
	require.ErrorIs(t, err, ErrNoKey)
}

// The acceptance item: the wrong master key gives a decrypt error, never garbage.
func TestTheWrongMasterKeyCannotResolve(t *testing.T) {
	ctx := context.Background()
	st, _, svc, _ := newService(t)

	_, err := svc.Set(ctx, "alice", "GREETING", []byte("correct-horse-battery"))
	require.NoError(t, err)

	other, err := GenerateKey()
	require.NoError(t, err)
	wrong := New(st, other, nil)

	got, err := wrong.Resolve(ctx, "task_1", []spec.SecretRef{{Name: "GREETING", Target: "env", Key: "GREETING"}})
	require.ErrorIs(t, err, ErrDecrypt)
	assert.Nil(t, got)
	assert.ErrorContains(t, err, "server holds "+other.ID(), "the error says which key the server has")
}

// The acceptance item: rotation re-encrypts every row, the old key stops working and the
// new one round-trips.
func TestRotateReEncryptsEveryRow(t *testing.T) {
	ctx := context.Background()
	st, dsn, svc, oldKey := newService(t)

	values := map[string]string{
		"GREETING":    "hello there general",
		"DB_PASSWORD": "hunter2-hunter2-hunter2",
		"DEPLOY_KEY":  "-----BEGIN OPENSSH PRIVATE KEY-----",
	}
	before := make(map[string][]byte, len(values))
	for name, v := range values {
		_, err := svc.Set(ctx, "alice", name, []byte(v))
		require.NoError(t, err)
		before[name] = bytes.Clone(rawColumn(t, dsn, name))
	}

	newKey, err := GenerateKey()
	require.NoError(t, err)

	require.ErrorContains(t, func() error { _, e := Rotate(ctx, st, oldKey, oldKey); return e }(),
		"the new master key is the old one")

	rotated, err := Rotate(ctx, st, oldKey, newKey)
	require.NoError(t, err)
	assert.Equal(t, len(values), rotated)

	refs := make([]spec.SecretRef, 0, len(values))
	for name := range values {
		refs = append(refs, spec.SecretRef{Name: name, Target: "env", Key: name})
	}

	// The old key can no longer decrypt anything.
	_, err = svc.Resolve(ctx, "task_old", refs)
	require.ErrorIs(t, err, ErrDecrypt)

	// The new key round-trips every value, and every row records it.
	rotatedSvc := New(st, newKey, nil)
	got, err := rotatedSvc.Resolve(ctx, "task_new", refs)
	require.NoError(t, err)
	require.Len(t, got, len(values))
	for _, r := range got {
		assert.Equal(t, values[r.Name], string(r.Value), "%s did not survive rotation", r.Name)
		assert.NotEqual(t, before[r.Name], rawColumn(t, dsn, r.Name), "%s was not re-encrypted", r.Name)
	}
	list, err := rotatedSvc.List(ctx)
	require.NoError(t, err)
	for _, row := range list {
		assert.Equal(t, newKey.ID(), row.KeyID)
	}
}

func TestBuiltinProviderResolvesOneName(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)

	_, err := svc.Set(ctx, "alice", "GREETING", []byte("a builtin value"))
	require.NoError(t, err)

	value, err := svc.Builtin().Resolve(ctx, "GREETING")
	require.NoError(t, err)
	assert.Equal(t, []byte("a builtin value"), value)

	_, err = svc.Builtin().Resolve(ctx, "ABSENT")
	require.ErrorIs(t, err, ErrMissing)
}
