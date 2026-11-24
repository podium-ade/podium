package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPreviewShowsAPersonChipAsTheName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "@Alice Ng can you look?", preview("@[Alice Ng](mention:alice@x.io) can you look?", 100))
	// Cut raw, the chip would end halfway through its own link and show as typed.
	long := "hello @[Bob Kowalski](mention:bob.k@example.com) " + strings.Repeat("word ", 30)
	got := preview(long, 40)
	assert.True(t, strings.HasPrefix(got, "hello @Bob Kowalski word word"), got)
	assert.NotContains(t, got, "mention:")
	assert.Equal(t, "mail alice@x.io [a](https://x.io)", preview("mail alice@x.io [a](https://x.io)", 100))
}
