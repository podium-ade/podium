package conductor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

type stubKeys struct {
	refused   map[string]bool
	err       error
	suspected []string
}

func (s *stubKeys) KeyRefused(_ context.Context, provider string) (bool, error) {
	return s.refused[provider], s.err
}

func (s *stubKeys) SuspectKey(provider string) { s.suspected = append(s.suspected, provider) }

func keyConductor(keys KeyHealth) *Conductor {
	return &Conductor{keys: keys, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestKeyRefusedStopsOnlyATurnSpendingTheRefusedGlobalKey(t *testing.T) {
	ctx := context.Background()
	keys := &stubKeys{refused: map[string]bool{profiles.ProviderAnthropic: true}}
	c := keyConductor(keys)

	text, refused := c.keyRefused(ctx, profiles.Choice{Agent: profiles.AgentClaude}, false)
	assert.True(t, refused)
	assert.Contains(t, text, "Anthropic is refusing this bot's API key")
	assert.NotContains(t, text, "x-api-key", "the provider's words are for the operator")

	_, refused = c.keyRefused(ctx, profiles.Choice{Agent: profiles.AgentGrok}, false)
	assert.False(t, refused, "another provider's key is not this one")

	_, refused = c.keyRefused(ctx, profiles.Choice{Agent: profiles.AgentClaude, CredentialOwner: "bob@acme.com"}, false)
	assert.False(t, refused, "a person's own key is not the global one")

	_, refused = c.keyRefused(ctx, profiles.Choice{Agent: profiles.AgentClaude}, true)
	assert.False(t, refused, "a chat-host turn may be spending a subscription instead")
}

func TestKeyRefusedStartsTheTurnWhenItCannotTell(t *testing.T) {
	c := keyConductor(&stubKeys{refused: map[string]bool{profiles.ProviderAnthropic: true}, err: errors.New("db down")})
	_, refused := c.keyRefused(context.Background(), profiles.Choice{Agent: profiles.AgentClaude}, false)
	assert.False(t, refused)

	_, refused = keyConductor(nil).keyRefused(context.Background(), profiles.Choice{Agent: profiles.AgentClaude}, false)
	assert.False(t, refused, "no key health configured checks nothing")
}

func TestNoteRefusalSuspectsOnlyARefusedCredential(t *testing.T) {
	ctx := context.Background()
	keys := &stubKeys{}
	c := keyConductor(keys)

	c.noteRefusal(ctx, profiles.ProviderAnthropic, "The turn failed before I could answer: invalid x-api-key", true)
	c.noteRefusal(ctx, profiles.ProviderXAI,
		"I could not answer: the model provider refused this bot's API key. An operator needs to replace it.", true)
	c.noteRefusal(ctx, profiles.ProviderAnthropic, "The turn failed before I could answer: Overloaded", true)
	c.noteRefusal(ctx, profiles.ProviderAnthropic, "invalid x-api-key is a header name", false)
	c.noteRefusal(ctx, "", "invalid x-api-key", true)

	assert.Equal(t, []string{profiles.ProviderAnthropic, profiles.ProviderXAI}, keys.suspected)
}
