package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTitleFromQuery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"how many active accounts last month", "how many active accounts last month"},
		{"/analyst how many active accounts", "how many active accounts"},
		{"/analyst", ""},
		{"  \n  ", ""},
		{"New chat", ""},
		{"line one\nline two", "line one"},
		{"/shrug what now", "what now"},
		{strings.Repeat("word ", 20), "word word word word word word word word word"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, TitleFromQuery(tc.in), tc.in)
	}
}

func TestSanitizeTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"August numbers", "August numbers"},
		{"\"August numbers\"", "August numbers"},
		{"August numbers.", "August numbers"},
		{"  August\nnumbers  ", "August"},
		{"", ""},
		{"New chat", ""},
		{"title\x00with\tnul", "titlewithnul"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, SanitizeTitle(tc.in), tc.in)
	}
}
