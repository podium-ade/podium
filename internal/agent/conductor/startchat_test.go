package conductor

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// The chat host and the runtime offer start_chat in the same words, because the prompt talks
// about it one way. This reads the runtime's source rather than trusting a comment to keep
// them together.
func TestStartChatIsDescribedTheSameWayToBothHarnesses(t *testing.T) {
	raw, err := os.ReadFile("../../../agent/runtime/src/mcp.ts")
	require.NoError(t, err)
	// Join the TypeScript string concatenations and unescape its quotes.
	joined := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(string(raw), "")
	joined = strings.ReplaceAll(joined, `\"`, `"`)
	assert.Contains(t, joined, StartChatTool.Description)
}

func TestTheChatHostOffersStartChat(t *testing.T) {
	var names []string
	for _, tool := range chatDelegateTools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, "start_chat")
}

func TestStartChatArgsReadsAListOrACommaSeparatedString(t *testing.T) {
	req, err := StartChatArgs(map[string]any{
		"title": "Triage", "opening": "o", "access": "members", "role": "viewer",
		"share_with": []any{"dana@acme.com", "ben@acme.com"},
	})
	require.NoError(t, err)
	assert.Equal(t, StartChatRequest{
		Title: "Triage", Opening: "o", Access: "members", Role: "viewer",
		ShareWith: []string{"dana@acme.com", "ben@acme.com"},
	}, req)

	req, err = StartChatArgs(map[string]any{"share_with": "dana@acme.com, ben@acme.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"dana@acme.com", " ben@acme.com"}, req.ShareWith, "trimmed where they are resolved")

	_, err = StartChatArgs(map[string]any{"share_with": []any{"dana@acme.com", 3}})
	require.ErrorIs(t, err, ErrInvalidStartChat)
}

type ctxT = context.Context

type fixedURL string

func (u fixedURL) URL(id string) string {
	if u == "" {
		return ""
	}
	return string(u) + "/agent/chat/" + id
}
func (fixedURL) Multiplayer(_ ctxT, _ string, _ bool, _ string) {}
func (fixedURL) ChatChanged(_ ctxT, _ string)                   {}

func TestAChatLinkCarriesItsLinkTokenOnlyWhenSharedByLink(t *testing.T) {
	pub := fixedURL("https://podium.example")
	assert.Equal(t, "https://podium.example/agent/chat/chat_1",
		chatLink(pub, store.Chat{ID: "chat_1", GeneralAccess: store.AccessRestricted, LinkToken: "stale"}))
	assert.Equal(t, "https://podium.example/agent/chat/chat_1?k=t%2Bk",
		chatLink(pub, store.Chat{ID: "chat_1", GeneralAccess: store.AccessLink, LinkToken: "t+k"}))
	assert.Equal(t, "/agent/chat/chat_1", chatLink(fixedURL(""), store.Chat{ID: "chat_1"}),
		"a conductor with no UI URL still gives a path")
	assert.Equal(t, "/agent/chat/chat_1", chatLink(nil, store.Chat{ID: "chat_1"}))
}

func TestDescribeStartedChatTellsTheModelWhatToSay(t *testing.T) {
	out := DescribeStartedChat(StartedChat{
		Chat: store.Chat{ID: "chat_1", Title: "Triage", GeneralAccess: store.AccessMembers},
		URL:  "https://podium.example/agent/chat/chat_1", SharedWith: []string{"dana@acme.com"},
	})
	assert.Contains(t, out, "url: https://podium.example/agent/chat/chat_1")
	assert.Contains(t, out, "shared with: dana@acme.com")
	assert.Contains(t, out, "access: members")
	assert.Contains(t, out, "[click here to join](https://podium.example/agent/chat/chat_1)")
}
