package conductor

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

func TestTheBylineReadsAsOneLine(t *testing.T) {
	assert.Equal(t, "Claude Opus 5.5 · Workspace API key",
		Byline{Model: "Claude Opus 5.5", Credential: "Workspace API key"}.String())
	assert.Equal(t, "Claude Opus 5.5", Byline{Model: "Claude Opus 5.5"}.String(),
		"a turn resumed after a restart knows its model and not whose key it spent")
	assert.Empty(t, Byline{}.String())
	assert.True(t, Byline{}.Empty())
}

// With no store, which is also what a conductor that cannot read its settings sees, every
// credential is a key and every person is named by their login's local part.
func TestTheBylineSaysWhoseCredentialPaid(t *testing.T) {
	c := &Conductor{logger: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	assert.Equal(t, Byline{Model: "Claude Opus 5.5", Credential: "Workspace API key"},
		c.bylineFor(ctx, profiles.ProviderAnthropic, "claude-opus-5-5", "", false))
	assert.Equal(t, Byline{Model: "Claude Opus 5.5", Credential: "Workspace Claude plan"},
		c.bylineFor(ctx, profiles.ProviderAnthropic, "claude-opus-5-5", "", true),
		"the global Claude sign-in a chat-host turn spent")
	assert.Equal(t, Byline{Model: "Claude Sonnet 5.5", Credential: "alvaro's Claude plan"},
		c.bylineFor(ctx, profiles.ProviderAnthropic, "claude-sonnet-5-5", "alvaro@podium.test", true))
	assert.Equal(t, Byline{Model: "GPT-6.1 Sol", Credential: "alvaro's API key"},
		c.bylineFor(ctx, profiles.ProviderOpenAI, "gpt-6.1-sol", "alvaro@podium.test", false))
}

func TestAModelOutsideTheCatalogueIsNamedByItsID(t *testing.T) {
	assert.Equal(t, "Claude Haiku 5.5", modelName("claude-haiku-5-5"))
	assert.Equal(t, "some-new-model", modelName("some-new-model"))
	assert.Equal(t, Byline{Model: "Grok 4.7"}, bylineOfBackend(store.Backend{Model: "grok-4.7"}))
}

func TestAnAnswerCarriesItsRunsByline(t *testing.T) {
	src := &saidSource{}
	s := newTestSink(src)
	s.byline = Byline{Model: "Claude Opus 5.5", Credential: "Workspace API key"}

	s.deliver(context.Background(), OutProgress, "looking", nil)
	s.deliver(context.Background(), OutFinal, "the answer", nil)

	require.NotEmpty(t, src.said)
	final := src.said[len(src.said)-1]
	assert.Equal(t, OutFinal, final.Type)
	assert.Equal(t, s.byline, final.Byline)
	for _, out := range src.said[:len(src.said)-1] {
		assert.True(t, out.Byline.Empty(), "only the answer says what answered: %+v", out)
	}
}

// An answer retried by the outbox after a restart still says what answered.
func TestTheBylineSurvivesTheOutbox(t *testing.T) {
	out := Outbound{Type: OutFinal, Text: "the answer", Byline: Byline{Model: "Claude Opus 5.5", Credential: "Workspace API key"}}
	raw, err := json.Marshal(payloadOf(out))
	require.NoError(t, err)
	var back store.OutboxPayload
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, out, outboundOf(back))
}
