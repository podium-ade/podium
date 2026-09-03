//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertSecretVersionsAndKeepsTheOriginalAuthor(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	first, err := s.UpsertSecret(ctx, Secret{
		Name: "GREETING", Ciphertext: []byte("c1"), Nonce: []byte("n1"), KeyID: "key1", CreatedBy: "alice",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Version)
	assert.Equal(t, "alice", first.CreatedBy)

	second, err := s.UpsertSecret(ctx, Secret{
		Name: "GREETING", Ciphertext: []byte("c2"), Nonce: []byte("n2"), KeyID: "key2", CreatedBy: "bob",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Version, "a replacement bumps the version")
	assert.Equal(t, []byte("c2"), second.Ciphertext)
	assert.Equal(t, "key2", second.KeyID)
	assert.Equal(t, "alice", second.CreatedBy, "created_by records who introduced the name")
	assert.False(t, second.UpdatedAt.Before(first.UpdatedAt))
}

func TestUpsertSecretRejectsEmptyInput(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, err := s.UpsertSecret(ctx, Secret{Ciphertext: []byte("c"), Nonce: []byte("n")})
	require.ErrorContains(t, err, "name is required")

	_, err = s.UpsertSecret(ctx, Secret{Name: "A", Nonce: []byte("n")})
	require.ErrorContains(t, err, "ciphertext and nonce are required")
}

func TestGetAndDeleteSecret(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, err := s.GetSecret(ctx, "ABSENT")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteSecret(ctx, "ABSENT"), ErrNotFound)

	_, err = s.UpsertSecret(ctx, Secret{Name: "A", Ciphertext: []byte("c"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "dev"})
	require.NoError(t, err)

	got, err := s.GetSecret(ctx, "A")
	require.NoError(t, err)
	assert.Equal(t, []byte("c"), got.Ciphertext)

	require.NoError(t, s.DeleteSecret(ctx, "A"))
	_, err = s.GetSecret(ctx, "A")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGetSecretsReturnsOnlyWhatExists(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for _, name := range []string{"A", "B"} {
		_, err := s.UpsertSecret(ctx, Secret{Name: name, Ciphertext: []byte("c"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "dev"})
		require.NoError(t, err)
	}

	got, err := s.GetSecrets(ctx, []string{"A", "MISSING", "B"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Contains(t, got, "A")
	assert.Contains(t, got, "B")
	assert.NotContains(t, got, "MISSING", "a missing name is simply absent; failing is the caller's decision")

	empty, err := s.GetSecrets(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestListSecretsIsSortedByName(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for _, name := range []string{"ZED", "ALPHA", "MIKE"} {
		_, err := s.UpsertSecret(ctx, Secret{Name: name, Ciphertext: []byte("c"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "dev"})
		require.NoError(t, err)
	}
	rows, err := s.ListSecrets(ctx)
	require.NoError(t, err)
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"ALPHA", "MIKE", "ZED"}, names)
}

func TestRotateSecretsIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	for _, name := range []string{"A", "B", "C"} {
		_, err := s.UpsertSecret(ctx, Secret{
			Name: name, Ciphertext: []byte("old-" + name), Nonce: []byte("n"), KeyID: "old", CreatedBy: "dev",
		})
		require.NoError(t, err)
	}

	// A rotation that fails part way through must leave every row untouched.
	_, err := s.RotateSecrets(ctx, func(row Secret) ([]byte, []byte, string, error) {
		if row.Name == "B" {
			return nil, nil, "", assert.AnError
		}
		return []byte("new-" + row.Name), []byte("n2"), "new", nil
	})
	require.Error(t, err)
	for _, name := range []string{"A", "B", "C"} {
		got, err := s.GetSecret(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, "old", got.KeyID, "%s must not have been rotated", name)
		assert.Equal(t, []byte("old-"+name), got.Ciphertext)
	}

	rotated, err := s.RotateSecrets(ctx, func(row Secret) ([]byte, []byte, string, error) {
		return []byte("new-" + row.Name), []byte("n2"), "new", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, rotated)
	for _, name := range []string{"A", "B", "C"} {
		got, err := s.GetSecret(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, "new", got.KeyID)
		assert.Equal(t, []byte("new-"+name), got.Ciphertext)
		assert.EqualValues(t, 1, got.Version, "rotation re-encrypts a value; it does not change it")
	}
}

func TestAuditRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	require.NoError(t, s.Audit(ctx, "alice", ActionSecretSet, "GREETING", map[string]any{"version": 1}))
	require.NoError(t, s.Audit(ctx, "bob", ActionSecretDelete, "GREETING", nil))
	require.NoError(t, s.Audit(ctx, "scheduler", ActionSecretResolve, "task_1", map[string]any{
		"names": []string{"GREETING"},
	}))
	require.ErrorContains(t, s.Audit(ctx, "alice", "", "x", nil), "action is required")

	all, err := s.ListAudit(ctx, "", "", 0)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, ActionSecretResolve, all[0].Action, "newest first")

	sets, err := s.ListAudit(ctx, ActionSecretSet, "", 0)
	require.NoError(t, err)
	require.Len(t, sets, 1)
	assert.Equal(t, "alice", sets[0].Actor)
	assert.EqualValues(t, 1, sets[0].Details["version"])
	assert.False(t, sets[0].TS.IsZero())

	subject, err := s.ListAudit(ctx, "", "task_1", 0)
	require.NoError(t, err)
	require.Len(t, subject, 1)
	assert.Equal(t, []any{"GREETING"}, subject[0].Details["names"])

	deletes, err := s.ListAudit(ctx, ActionSecretDelete, "", 0)
	require.NoError(t, err)
	require.Len(t, deletes, 1)
	assert.Empty(t, deletes[0].Details, "nil details store as an empty object, not null")
}
