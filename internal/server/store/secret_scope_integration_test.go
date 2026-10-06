//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretNamesStayDisjointAndReservedNamesStayGlobal(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	_, err := s.UpsertSecret(ctx, Secret{
		Scope: SecretScopeGlobal, Name: "SHARED", Ciphertext: []byte("g"), Nonce: []byte("n"),
		KeyID: "k", CreatedBy: "local",
	})
	require.NoError(t, err)

	_, err = s.UpsertSecret(ctx, Secret{
		Scope: SecretScopePersonal, Owner: "ada@acme.com", Name: "SHARED",
		Ciphertext: []byte("p"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "ada@acme.com",
	})
	require.ErrorIs(t, err, ErrSecretNameTaken)

	_, err = s.UpsertSecret(ctx, Secret{
		Scope: SecretScopePersonal, Owner: "ada@acme.com", Name: "podium.agent.github_token",
		Ciphertext: []byte("p"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "ada@acme.com",
	})
	require.ErrorIs(t, err, ErrReservedSecretName)

	_, err = s.UpsertSecret(ctx, Secret{
		Scope: SecretScopePersonal, Owner: "ada@acme.com", Name: "MINE",
		Ciphertext: []byte("p"), Nonce: []byte("n"), KeyID: "k", CreatedBy: "ada@acme.com",
	})
	require.NoError(t, err)

	visible, err := s.ListVisibleSecrets(ctx, "ada@acme.com")
	require.NoError(t, err)
	require.Equal(t, []string{"SHARED", "MINE"}, secretNames(visible))

	other, err := s.ListVisibleSecrets(ctx, "bob@acme.com")
	require.NoError(t, err)
	require.Equal(t, []string{"SHARED"}, secretNames(other))
}

func secretNames(rows []Secret) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Name)
	}
	return out
}
