package ghreview

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitThreadNotesKeepsAMalformedFence(t *testing.T) {
	rest, notes := splitThreadNotes("done\n\n```reply\nno location here\n```\n\n```resolved\nx.go:4-6\nfixed\n```")
	require.Len(t, notes, 1)
	assert.Equal(t, threadNote{Path: "x.go", Line: 6, Body: "fixed", Resolve: true}, notes[0],
		"a range names the finding by its last line, as the review's own note does")
	assert.Contains(t, rest, "no location here", "a fence that names no finding stays in the answer")
}

func TestAThreadReplyKeepsTheCodeItQuotes(t *testing.T) {
	rest, notes := splitThreadNotes("Fixed.\n```resolved\na.go:3\nnow:\n```go\nok()\n```\n```\n")
	assert.Equal(t, "Fixed.", rest)
	require.Len(t, notes, 1)
	assert.True(t, notes[0].Resolve)
	assert.Equal(t, "now:\n```go\nok()\n```", notes[0].Body)
}
