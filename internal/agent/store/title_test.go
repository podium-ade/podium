package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanChatTitle(t *testing.T) {
	got, err := cleanChatTitle("  August   numbers \n ")
	require.NoError(t, err)
	assert.Equal(t, "August numbers", got)

	_, err = cleanChatTitle("   \n\t  ")
	assert.ErrorIs(t, err, ErrInvalidChatTitle)

	_, err = cleanChatTitle("ok\x00still")
	assert.ErrorIs(t, err, ErrInvalidChatTitle)

	ok := strings.Repeat("é", MaxChatTitleRunes)
	got, err = cleanChatTitle(ok)
	require.NoError(t, err)
	assert.Equal(t, ok, got)

	_, err = cleanChatTitle(ok + "x")
	assert.ErrorIs(t, err, ErrInvalidChatTitle)
}
