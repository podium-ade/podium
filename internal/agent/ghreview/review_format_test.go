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
