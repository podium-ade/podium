package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/store"
)

// Only a person's turn is answered on the issue: a routine's run, and a turn that ran as
// nobody or cannot be placed, say nothing there but what their tools say.
func TestOnlyAPersonsTurnIsAnsweredOnTheIssue(t *testing.T) {
	assert.True(t, relayable(&store.TurnRunner{Login: "alice@acme.com"}))
	assert.False(t, relayable(&store.TurnRunner{Login: "alice@acme.com", Routine: "Triage"}))
	assert.False(t, relayable(&store.TurnRunner{Routine: "Triage", RoutineSystem: true}))
	assert.False(t, relayable(&store.TurnRunner{}))
	assert.False(t, relayable(nil))
}

// The payload a relay is owed with keeps whose words they are across a retry.
func TestARelayKeepsItsAuthorThroughTheOutbox(t *testing.T) {
	out := Outbound{Type: OutRelay, Text: "hi", Author: "Ada Lovelace", AuthorAvatar: "https://example.com/ada.png"}
	back := outboundOf(payloadOf(out))
	assert.Equal(t, out.Author, back.Author)
	assert.Equal(t, out.AuthorAvatar, back.AuthorAvatar)
	assert.Equal(t, OutRelay, back.Type)
}
