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
	c := &Conductor{tickets: func(_ context.Context, login, sourceKey, text string) ([]BriefTicket, int) {
		asked = append(asked, login+"|"+sourceKey+"|"+text)
		return []BriefTicket{{Identifier: "EME-1"}, {Identifier: "EME-2"}, {Identifier: "EME-3"}, {Identifier: "EME-4"}}, 2
	}}
	ctx := context.Background()
	host := job{onHost: true}

	got, withheld := c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{SourceKey: "chat:1", Text: "built around it", RecordText: "look at EME-1"})
	assert.Len(t, got, MaxBriefTickets, "at most three")
	assert.Equal(t, 2, withheld, "the count of those left out for the conversation's other readers")
	assert.Equal(t, []string{"ada@acme.com|chat:1|look at EME-1"}, asked, "the person's own words, not the instruction")

	asked = nil
	for name, call := range map[string]func() ([]BriefTicket, int){
		"a playbook's turn": func() ([]BriefTicket, int) {
			return c.briefTickets(ctx, job{}, "ada@acme.com", InboundEvent{Text: "EME-1"})
		},
		"nobody known": func() ([]BriefTicket, int) { return c.briefTickets(ctx, host, "", InboundEvent{Text: "EME-1"}) },
		"a routine's run": func() ([]BriefTicket, int) {
			return c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1", Routine: &RoutineRef{}})
		},
		"an automatic event": func() ([]BriefTicket, int) {
			return c.briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1", Automatic: true})
		},
		"no briefer": func() ([]BriefTicket, int) {
			return (&Conductor{}).briefTickets(ctx, host, "ada@acme.com", InboundEvent{Text: "EME-1"})
		},
	} {
		got, withheld := call()
		assert.Nil(t, got, name)
		assert.Zero(t, withheld, name)
	}
	assert.Empty(t, asked)
}

func TestABriefWithoutTicketsCarriesNoKey(t *testing.T) {
	b := &Brief{SessionID: "s", TurnID: "t", Source: BriefSource{Kind: SourceChat, Ref: "r"}}
	enc, err := b.encodeOnce()
	require.NoError(t, err)
	assert.NotContains(t, enc, "linear_tickets")

	b.LinearTicketsWithheld = 2
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"linear_tickets_withheld":2`)
	b.LinearTicketsWithheld = 0

	b.LinearTickets = []BriefTicket{{Identifier: "EME-1", Title: "t", Comments: []BriefTicketComment{{Author: "a", At: "x", Body: "b"}}}}
	raw, err = json.Marshal(b)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"linear_tickets":[{"identifier":"EME-1","title":"t","comments":[{"author":"a","at":"x","body":"b"}]}]`)
}
