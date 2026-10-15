//go:build integration

package secrets

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

// A provider's key is stored encrypted, listed without itself, kept when only the settings
// change, resolved for a node, survives a master-key rotation and is gone once deleted.
func TestSandboxProviderCredential(t *testing.T) {
	ctx := context.Background()
	st, dsn, svc, oldKey := newService(t)

	_, err := svc.SetSandboxProvider(ctx, "alice", "boat", map[string]string{"template": "ci-base"}, 0, nil)
	require.ErrorIs(t, err, ErrInvalidSecret, "a new provider needs its key")

	row, err := svc.SetSandboxProvider(ctx, "alice", "boat", map[string]string{"template": "ci-base"}, 0, []byte("boat_live_key"))
	require.NoError(t, err)
	assert.Empty(t, row.Ciphertext)
	assert.NotEmpty(t, row.KeyID)

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var raw []byte
	require.NoError(t, conn.QueryRow(ctx, "select ciphertext from sandbox_providers where name = 'boat'").Scan(&raw))
	assert.NotContains(t, string(raw), "boat_live_key", "the key is not in Postgres in the clear")

	_, err = svc.SetSandboxProvider(ctx, "bob", "boat", map[string]string{"template": "ci-v2", "default_type": "small"}, 4, nil)
	require.NoError(t, err, "changing settings keeps the stored key")
	list, err := svc.ListSandboxProviders(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, map[string]string{"template": "ci-v2", "default_type": "small"}, list[0].Settings)
	assert.Equal(t, "bob", list[0].CreatedBy)
	assert.Empty(t, list[0].Ciphertext)

	cfg, found, err := svc.ResolveSandboxProvider(ctx, "node_1", "boat")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "boat_live_key", string(cfg.Secret))
	assert.Equal(t, "ci-v2", cfg.Settings["template"])

	_, found, err = svc.ResolveSandboxProvider(ctx, "node_1", "modal")
	require.NoError(t, err)
	assert.False(t, found)

	newKey, err := GenerateKey()
	require.NoError(t, err)
	rotated, err := Rotate(ctx, st, oldKey, newKey)
	require.NoError(t, err)
	assert.Equal(t, 1, rotated)
	cfg, found, err = New(st, newKey, nil).ResolveSandboxProvider(ctx, "node_1", "boat")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "boat_live_key", string(cfg.Secret), "the key survives a rotation")

	for name, settings := range map[string]map[string]string{
		"bad name":          nil,
		"boat":              {"secret": "in the clear"},
		"boat-with-bad-key": {"Bad Key": "x"},
	} {
		_, err := svc.SetSandboxProvider(ctx, "alice", name, settings, 0, []byte("k"))
		require.ErrorIs(t, err, ErrInvalidSecret, name)
	}

	require.NoError(t, New(st, newKey, nil).DeleteSandboxProvider(ctx, "alice", "boat"))
	require.ErrorIs(t, svc.DeleteSandboxProvider(ctx, "alice", "boat"), store.ErrNotFound)
}
