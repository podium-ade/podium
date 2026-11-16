//go:build integration

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openQuestions is login's unread question items.
func openQuestions(t *testing.T, s *Store, login string) []Notification {
	t.Helper()
	rows, err := s.ListNotifications(context.Background(), login, true, 50)
	require.NoError(t, err)
	var out []Notification
	for _, n := range rows {
		if n.Kind == NotifyQuestion {
			out = append(out, n)
		}
	}
	return out
}

// A question asked in a ticket's conversation is one item in the asked person's inbox, with the
// ticket's identifier and title, replaced rather than repeated by the next question, left alone
// by "mark all read", and closed when the conversation hears from somebody.
func TestAQuestionInATicketsConversationIsOneInboxItemUntilAnswered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := "linear:iss-12"
	chat, _, err := s.EnsureLinearConversation(ctx, key, "EME-12 Export is slow")
	require.NoError(t, err)
	require.NoError(t, s.UpsertLinearIssue(ctx, LinearIssue{SourceKey: key, Identifier: "EME-12", Title: "Export is slow"}))
	require.NoError(t, s.UpsertIdentity(ctx, Identity{Login: "ada@acme.com", Provider: ProviderLinear, ExternalID: "org/ada"}))
	require.NoError(t, s.RecordLinearPeople(ctx, key, []LinearPerson{{LinearUser: "org/ada", Via: LinearViaDelegated}}, time.Now()))

	// Created.
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", "Which region is slow?"))
	got := openQuestions(t, s, "ada@acme.com")
	require.Len(t, got, 1)
	assert.Equal(t, chat.ID, got[0].ChatID)
	assert.Equal(t, "Which region is slow?", got[0].Detail)
	assert.Equal(t, "EME-12", got[0].LinearIdentifier)
	assert.Equal(t, "Export is slow", got[0].LinearTitle)
	first := got[0].ID

	// Deduped: the same question, or the next, is the same item with the newest text.
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", "Which region is slow?"))
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", "And since when?"))
	got = openQuestions(t, s, "ada@acme.com")
	require.Len(t, got, 1)
	assert.Equal(t, first, got[0].ID)
	assert.Equal(t, "And since when?", got[0].Detail)

	// Nobody else is told, and a question with no person or no text is nothing.
	assert.Empty(t, openQuestions(t, s, "bob@acme.com"))
	require.NoError(t, s.NotifyQuestion(ctx, "", chat.ID, 0, "", "x"))
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", "   "))
	assert.Len(t, openQuestions(t, s, "ada@acme.com"), 1)

	// A very long question is cut for the inbox.
	long := strings.Repeat("é", QuestionDetailChars+10)
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", long))
	got = openQuestions(t, s, "ada@acme.com")
	require.Len(t, got, 1)
	assert.Equal(t, QuestionDetailChars+1, len([]rune(got[0].Detail)))

	// "Mark all read" — and opening the chat, which marks the rest — leaves it: it waits for an
	// answer.
	_, err = s.MarkNotificationsRead(ctx, "ada@acme.com", nil, true)
	require.NoError(t, err)
	assert.Len(t, openQuestions(t, s, "ada@acme.com"), 1)

	// Closed by the conversation hearing from somebody.
	n, err := s.ResolveQuestions(ctx, key)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Empty(t, openQuestions(t, s, "ada@acme.com"))
	n, err = s.ResolveQuestions(ctx, key)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "nothing left to close")

	// The next question after an answer is a new item.
	require.NoError(t, s.NotifyQuestion(ctx, "ada@acme.com", chat.ID, 0, "", "Is it fixed?"))
	got = openQuestions(t, s, "ada@acme.com")
	require.Len(t, got, 1)
	assert.NotEqual(t, first, got[0].ID)

	// Dismissed by its own id.
	_, err = s.MarkNotificationsRead(ctx, "ada@acme.com", []int64{got[0].ID}, false)
	require.NoError(t, err)
	assert.Empty(t, openQuestions(t, s, "ada@acme.com"))
}
