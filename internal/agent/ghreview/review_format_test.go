package ghreview

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewEventApprovesOnlyACleanVerdict(t *testing.T) {
	assert.Equal(t, eventApprove, reviewEvent("**Approve**\n\nLooks right."))
	assert.Equal(t, eventApprove, reviewEvent("**Nits**\n\nTwo names."))
	assert.Equal(t, eventApprove, reviewEvent("**Nits only**\n\n"))
	assert.Equal(t, eventComment, reviewEvent("**Changes needed**\n\nThe handler drops the error."))
	assert.Equal(t, eventComment, reviewEvent("looks good"))
	assert.Equal(t, eventComment, reviewEvent(""))
}

func TestSplitInlineKeepsNotesOffTheSummary(t *testing.T) {
	body := "**Nits**\n\nTwo names.\n\n```inline\ninternal/a.go:12\nRename this.\n```\n\n```inline\nweb/src/App.tsx:4-6\nThis wraps under the button.\n```\n"
	summary, notes := splitInline(body)
	assert.Equal(t, "**Nits**\n\nTwo names.", summary)
	require.Len(t, notes, 2)
	assert.Equal(t, inlineNote{Path: "internal/a.go", Line: 12, Body: "Rename this."}, notes[0])
	assert.Equal(t, inlineNote{Path: "web/src/App.tsx", Line: 6, StartLine: 4, Body: "This wraps under the button."}, notes[1])

	kept, none := splitInline("**Changes needed**\n\n```inline\nnot a location\nmissing a line\n```")
	assert.Contains(t, kept, "not a location")
	assert.Empty(t, none)
}

func TestSplitThreadNotesKeepsAMalformedFence(t *testing.T) {
	rest, notes := splitThreadNotes("done\n\n```reply\nno location here\n```\n\n```resolved\nx.go:4-6\nfixed\n```")
	require.Len(t, notes, 1)
	assert.Equal(t, threadNote{Path: "x.go", Line: 6, Body: "fixed", Resolve: true}, notes[0],
		"a range names the finding by its last line, as the review's own note does")
	assert.Contains(t, rest, "no location here", "a fence that names no finding stays in the answer")
}

// A note that quotes code keeps the code: the ```go block inside it does not end the note,
// and nothing of it is left behind in the summary.
func TestAnInlineNoteKeepsTheCodeItQuotes(t *testing.T) {
	body := "**Changes needed**\n\n### Summary\n- **Read:** the diff.\n\n" +
		"```inline\ninternal/agent/conductor/chathost.go:486\n" +
		"Chat-host turns fail. `chatRun.run` still checks:\n\n" +
		"```go\nif _, err := c.host.Credential(runCtx, h.provider.ID); err != nil {\n```\n" +
		"Because `h.provider.ID` is `\"anthropic\"`, it looks for the key.\n```\n"
	summary, notes := splitInline(body)
	assert.Equal(t, "**Changes needed**\n\n### Summary\n- **Read:** the diff.", summary)
	require.Len(t, notes, 1)
	assert.Equal(t, "internal/agent/conductor/chathost.go", notes[0].Path)
	assert.Equal(t, 486, notes[0].Line)
	assert.Contains(t, notes[0].Body, "```go\nif _, err := c.host.Credential(runCtx, h.provider.ID); err != nil {\n```")
	assert.Contains(t, notes[0].Body, "it looks for the key.")
}

// Two notes each quoting code, and a four-backtick note quoting a bare fence.
func TestNotesWithCodeAreSplitOneByOne(t *testing.T) {
	body := "verdict\n" +
		"```inline\na.go:1\nfirst\n```go\nx := 1\n```\n```\n" +
		"between\n" +
		"````inline\nb.go:2\nsecond\n```\nplain\n```\n````\n"
	summary, notes := splitInline(body)
	assert.Equal(t, "verdict\nbetween", summary)
	require.Len(t, notes, 2)
	assert.Equal(t, "first\n```go\nx := 1\n```", notes[0].Body)
	assert.Equal(t, "second\n```\nplain\n```", notes[1].Body)
}

// A fence with no close is left as the model wrote it.
func TestAnUnclosedNoteStaysInTheSummary(t *testing.T) {
	body := "verdict\n```inline\na.go:1\nnever closed\n"
	summary, notes := splitInline(body)
	assert.Empty(t, notes)
	assert.Equal(t, "verdict\n```inline\na.go:1\nnever closed", summary)
}

func TestAThreadReplyKeepsTheCodeItQuotes(t *testing.T) {
	rest, notes := splitThreadNotes("Fixed.\n```resolved\na.go:3\nnow:\n```go\nok()\n```\n```\n")
	assert.Equal(t, "Fixed.", rest)
	require.Len(t, notes, 1)
	assert.True(t, notes[0].Resolve)
	assert.Equal(t, "now:\n```go\nok()\n```", notes[0].Body)
}
