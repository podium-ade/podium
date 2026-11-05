package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// A message deleted on its own surface keeps its row and its text in the store, and none of
// the text leaves the API: the UI draws a placeholder (decision 12).
func TestADeletedMessageSendsNoText(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	m := store.ChatMessage{
		ChatID: "c", Seq: 4, Role: store.RoleUser, Text: "secret", Author: "dana",
		Via: store.ViaSlack, ExternalURL: "https://x/p1", Addressed: false,
		Attachments: []store.ChatAttachment{{ArtifactID: "a1", Name: "f.txt"}},
		EditedAt:    &at, DeletedAt: &at,
	}
	out := chatMessageToProto(m)
	assert.True(t, out.GetDeleted())
	assert.Empty(t, out.GetText())
	assert.Empty(t, out.GetAttachments())
	assert.Equal(t, "slack", out.GetVia(), "where it was said is still shown")
	assert.Equal(t, "dana", out.GetAuthor())
}

func TestAMessageCarriesItsAttribution(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	out := chatMessageToProto(store.ChatMessage{
		ChatID: "c", Seq: 1, Role: store.RoleUser, Text: "lgtm", Author: "octocat",
		AuthorHandle: "octocat", AuthorAvatar: "https://a/o.png", Via: store.ViaGitHub,
		ExternalURL: "https://github.com/a/b/pull/1#issuecomment-7", EditedAt: &at,
	})
	assert.Equal(t, "lgtm", out.GetText())
	assert.Empty(t, out.GetAuthorLogin(), "not in Podium")
	assert.Equal(t, "octocat", out.GetAuthorHandle())
	assert.Equal(t, "https://a/o.png", out.GetAuthorAvatar())
	assert.Equal(t, "github", out.GetVia())
	assert.False(t, out.GetAddressed())
	require.NotNil(t, out.GetEditedAt())
	assert.False(t, out.GetDeleted())
}

func TestSurfacesComeFromTheSessionKey(t *testing.T) {
	gh := surfacesOf(store.Chat{SourceKey: "github:podium-ade/podium#247"})
	require.Len(t, gh, 1)
	assert.Equal(t, "github", gh[0].GetKind())
	assert.Equal(t, "podium-ade/podium#247", gh[0].GetLabel())
	assert.Equal(t, "https://github.com/podium-ade/podium/pull/247", gh[0].GetUrl())

	sl := surfacesOf(store.Chat{SourceKey: "slack:C1:1.1", Channel: "eng-review"})
	require.Len(t, sl, 1)
	assert.Equal(t, "#eng-review", sl[0].GetLabel())

	assert.Equal(t, "Slack thread", surfacesOf(store.Chat{SourceKey: "slack:C1:1.1"})[0].GetLabel())
	assert.Empty(t, sl[0].GetUrl(), "no link until the mirror recorded the thread's")
	linked := surfacesOf(store.Chat{SourceKey: "slack:C1:1.1", SourceURL: "https://acme.slack.com/archives/C1/p11"})
	assert.Equal(t, "https://acme.slack.com/archives/C1/p11", linked[0].GetUrl())
	assert.Empty(t, surfacesOf(store.Chat{SourceKey: "chat:chat_1"}), "a web chat lives nowhere else")
}
