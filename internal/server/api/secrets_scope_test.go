package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

func TestTheConductorStoresAPersonalMCPCredentialForThatPerson(t *testing.T) {
	s := NewSecretService(nil, nil, nil)
	agent := transport.NewContext(context.Background(), transport.Identity{Kind: transport.KindAgent, Login: "agent"})

	scope, owner, err := s.authorizeWrite(agent, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "mcp.linear_token", "ada@acme.com")
	require.NoError(t, err)
	assert.Equal(t, store.SecretScopePersonal, scope)
	assert.Equal(t, "ada@acme.com", owner)

	_, _, err = s.authorizeWrite(agent, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "ADA_NOTE", "ada@acme.com")
	require.Error(t, err)
	_, _, err = s.authorizeWrite(agent, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "mcp.linear_token", "")
	require.Error(t, err)
	_, _, err = s.authorizeWrite(agent, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "podium.agent.mcp.linear_token", "ada@acme.com")
	require.Error(t, err)

	dev := transport.NewContext(context.Background(), transport.Identity{Kind: transport.KindLocalToken, Login: "local"})
	_, _, err = s.authorizeWrite(dev, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "mcp.linear_token", "ada@acme.com")
	require.Error(t, err)

	person := transport.NewContext(context.Background(), transport.Identity{Kind: transport.KindUser, Login: "ada@acme.com"})
	scope, owner, err = s.authorizeWrite(person, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "ADA_NOTE", "")
	require.NoError(t, err)
	assert.Equal(t, store.SecretScopePersonal, scope)
	assert.Equal(t, "ada@acme.com", owner)
	_, _, err = s.authorizeWrite(person, podiumv1.SecretScope_SECRET_SCOPE_PERSONAL, "ADA_NOTE", "bob@acme.com")
	require.Error(t, err)

	listed, mcpOnly, err := s.listOwner(agent, "ada@acme.com")
	require.NoError(t, err)
	assert.Equal(t, "ada@acme.com", listed)
	assert.True(t, mcpOnly)
	listed, mcpOnly, err = s.listOwner(agent, "")
	require.NoError(t, err)
	assert.Empty(t, listed)
	assert.False(t, mcpOnly)
	_, _, err = s.listOwner(dev, "ada@acme.com")
	require.Error(t, err)
	_, _, err = s.listOwner(person, "bob@acme.com")
	require.Error(t, err)
}
