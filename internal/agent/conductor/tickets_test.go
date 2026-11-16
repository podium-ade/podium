package conductor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBriefTicketsAreAPersonsOwnWordsToTheAssistant(t *testing.T) {
	var asked []string
	c := &Conductor{tickets: func(_ context.Context, login, sourceKey, text string) []BriefTicket {
		asked = append(asked, login+"|"+sourceKey+"|"+text)
		return []BriefTicket{{Identifier: "EME-1"}, {Identifier: "EME-2"}, {Identifier: "EME-3"}, {Identifier: "EME-4"}}
	}}
	ctx := context.Background()
	host := job{onHost: true}

	got := c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{SourceKey: "chat:1", Text: "built around it", RecordText: "look at EME-1"})
	assert.Len(t, got, MaxBriefTickets, "at most three")
	assert.Equal(t, []string{"ada@acme.com|chat:1|look at EME-1"}, asked, "the person's own words, not the instruction")

	asked = nil
	assert.Nil(t, c.briefTickets(ctx, job{}, "ada@acme.com", InboundEvent{Text: "EME-1"}), "a playbook's turn")
	assert.Nil(t, c.briefTickets(ctx, host, "", InboundEvent{Text: "EME-1"}), "nobody known")
	assert.Nil(t, c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1", Routine: &RoutineRef{}}), "a routine's run")
	assert.Nil(t, c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1", Automatic: true}), "an automatic event")
	assert.Empty(t, asked)

	none := &Conductor{}
	assert.Nil(t, none.briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1"}))
}

func TestABriefWithoutTicketsCarriesNoKey(t *testing.T) {
	b := &Brief{SessionID: "s", TurnID: "t", Source: BriefSource{Kind: SourceChat, Ref: "r"}}
	enc, err := b.encodeOnce()
	require.NoError(t, err)
	assert.NotContains(t, enc, "linear_tickets")

	b.LinearTickets = []BriefTicket{{Identifier: "EME-1", Title: "t", Comments: []BriefTicketComment{{Author: "a", At: "x", Body: "b"}}}}
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"linear_tickets":[{"identifier":"EME-1","title":"t","comments":[{"author":"a","at":"x","body":"b"}]}]`)
}
