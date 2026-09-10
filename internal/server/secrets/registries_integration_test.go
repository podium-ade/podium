//go:build integration

package secrets

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

const saKey = `{"type":"service_account","project_id":"acme","private_key":"-----BEGIN PRIVATE KEY-----"}`

func rawRegistryColumn(t *testing.T, dsn, host string) []byte {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var ciphertext []byte
	require.NoError(t, conn.QueryRow(ctx, "select ciphertext from registries where host = $1", host).Scan(&ciphertext))
	return ciphertext
}

// A login is stored by normalised host, listed without its password, and resolved only for
// the registries a task's images actually name.
func TestRegistryLoginGoesOnlyToTheImagesThatNeedIt(t *testing.T) {
	ctx := context.Background()
	_, dsn, svc, _ := newService(t)

	row, err := svc.SetRegistry(ctx, "alice", "https://US-docker.pkg.dev/acme/images", "_json_key", []byte(saKey))
	require.NoError(t, err)
	assert.Equal(t, "us-docker.pkg.dev", row.Host, "the host is normalised the way an image reference spells it")
	_, err = svc.SetRegistry(ctx, "alice", "https://index.docker.io/v1/", "hubuser", []byte("hubpass"))
	require.NoError(t, err)
	assert.NotContains(t, string(rawRegistryColumn(t, dsn, "us-docker.pkg.dev")), "service_account",
		"the key file is not in Postgres in the clear")

	list, err := svc.ListRegistries(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, r := range list {
		assert.Empty(t, r.Ciphertext)
		assert.Empty(t, r.Nonce)
	}

	creds, err := svc.ResolveRegistries(ctx, "task_1", []string{
		"us-docker.pkg.dev/acme/images/app:1.2", "alpine:3", "ghcr.io/octocat/tool:1", "not a reference",
	})
	require.NoError(t, err)
	require.Len(t, creds, 2, "ghcr.io has no login and is left anonymous; a bad reference is not this package's problem")
	assert.Equal(t, "docker.io", creds[0].Host)
	assert.Equal(t, "hubuser", creds[0].Username)
	assert.Equal(t, "hubpass", string(creds[0].Password))
	assert.Equal(t, "us-docker.pkg.dev", creds[1].Host)
	assert.Equal(t, "_json_key", creds[1].Username)
	assert.Equal(t, saKey, string(creds[1].Password), "the key file comes back byte for byte")

	none, err := svc.ResolveRegistries(ctx, "task_2", []string{"ghcr.io/octocat/tool:1"})
	require.NoError(t, err)
	assert.Empty(t, none)

	require.NoError(t, svc.DeleteRegistry(ctx, "alice", "us-docker.pkg.dev"))
	require.ErrorIs(t, svc.DeleteRegistry(ctx, "alice", "us-docker.pkg.dev"), store.ErrNotFound)
	creds, err = svc.ResolveRegistries(ctx, "task_3", []string{"us-docker.pkg.dev/acme/images/app:1.2"})
	require.NoError(t, err)
	assert.Empty(t, creds, "a deleted login means an anonymous pull, not a failed task")
}

func TestSetRegistryRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _ := newService(t)
	for name, in := range map[string][3]string{
		"host with spaces": {"not a host", "u", "p"},
		"empty host":       {"", "u", "p"},
		"empty username":   {"ghcr.io", "", "p"},
		"empty password":   {"ghcr.io", "u", ""},
	} {
		_, err := svc.SetRegistry(ctx, "alice", in[0], in[1], []byte(in[2]))
		require.ErrorIs(t, err, ErrInvalidSecret, name)
	}
}

// A server without a master key holds no logins, so it resolves none rather than failing
// every task whose image happens to be on a private registry.
func TestRegistriesWithoutAMasterKey(t *testing.T) {
	ctx := context.Background()
	st, _, _, _ := newService(t)
	disabled := New(st, nil, nil)
	_, err := disabled.SetRegistry(ctx, "alice", "ghcr.io", "u", []byte("p"))
	require.ErrorIs(t, err, ErrNoKey)
	creds, err := disabled.ResolveRegistries(ctx, "task", []string{"ghcr.io/octocat/tool:1"})
	require.NoError(t, err)
	assert.Empty(t, creds)
}

// The wrong key is an error naming both key ids, never garbage handed to a node.
func TestTheWrongMasterKeyCannotResolveARegistry(t *testing.T) {
	ctx := context.Background()
	st, _, svc, _ := newService(t)
	_, err := svc.SetRegistry(ctx, "alice", "ghcr.io", "octocat", []byte("ghp_token"))
	require.NoError(t, err)
	other, err := GenerateKey()
	require.NoError(t, err)
	_, err = New(st, other, nil).ResolveRegistries(ctx, "task", []string{"ghcr.io/octocat/tool:1"})
	require.ErrorIs(t, err, ErrDecrypt)
	require.ErrorContains(t, err, other.ID())
}

// Rotation covers registries too: a rotated server that could read its secrets but not its
// registry logins would fail every private pull with a decrypt error.
func TestRotateReEncryptsRegistriesWithTheSecrets(t *testing.T) {
	ctx := context.Background()
	st, dsn, svc, oldKey := newService(t)
	_, err := svc.Set(ctx, "alice", "GREETING", []byte("hello there general"))
	require.NoError(t, err)
	_, err = svc.SetRegistry(ctx, "alice", "us-docker.pkg.dev", "_json_key", []byte(saKey))
	require.NoError(t, err)
	before := bytes.Clone(rawRegistryColumn(t, dsn, "us-docker.pkg.dev"))

	newKey, err := GenerateKey()
	require.NoError(t, err)
	rotated, err := Rotate(ctx, st, oldKey, newKey)
	require.NoError(t, err)
	assert.Equal(t, 2, rotated, "one secret and one registry")

	_, err = svc.ResolveRegistries(ctx, "task_old", []string{"us-docker.pkg.dev/acme/images/app"})
	require.ErrorIs(t, err, ErrDecrypt)

	creds, err := New(st, newKey, nil).ResolveRegistries(ctx, "task_new", []string{"us-docker.pkg.dev/acme/images/app"})
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, saKey, string(creds[0].Password))
	assert.NotEqual(t, before, rawRegistryColumn(t, dsn, "us-docker.pkg.dev"))
	list, err := New(st, newKey, nil).ListRegistries(ctx)
	require.NoError(t, err)
	assert.Equal(t, newKey.ID(), list[0].KeyID)
}
