package linear

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindIssueRefsMatchesOnlyTheWorkspacesTeams(t *testing.T) {
	known := func(k string) bool { return k == "EME" || k == "ENG" || k == "A1" }
	ids := func(refs []IssueRef) []string {
		out := []string{}
		for _, r := range refs {
			out = append(out, r.Identifier)
		}
		return out
	}
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"a bare identifier", "can you look at EME-6?", []string{"EME-6"}},
		{"several, in order, each once", "ENG-12 blocks EME-6, and EME-6 again; also ENG-3.", []string{"ENG-12", "EME-6", "ENG-3"}},
		{"a team the workspace does not have", "the file is UTF-8 and the date ISO-8601", []string{}},
		{"lower case is not an identifier", "see eme-6", []string{}},
		{"a link counts as its identifier", "https://linear.app/emergere/issue/EME-7/fix-the-thing and EME-7", []string{"EME-7"}},
		{"a link with a lower-case identifier", "https://linear.app/acme/issue/eng-40", []string{"ENG-40"}},
		{"inside a word or a path", "xEME-6 foo/EME-6 EME-6x", []string{}},
		{"a version or a hyphenated tail", "EME-6.1 EME-6-beta", []string{}},
		{"punctuation around it", "(EME-6), \"ENG-1\": [A1-2]", []string{"EME-6", "ENG-1", "A1-2"}},
		{"a leading zero is no issue number", "EME-06", []string{}},
		{"at the end of a sentence", "Fixed in EME-9.", []string{"EME-9"}},
		{"a github link is not Linear's", "https://github.com/acme/repo/issues/EME-6", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ids(FindIssueRefs(tc.text, known, 0)))
		})
	}
}

func TestFindIssueRefsStopsAtMax(t *testing.T) {
	refs := FindIssueRefs("EME-1 EME-2 EME-3 EME-4", func(string) bool { return true }, 3)
	assert.Len(t, refs, 3)
	assert.Equal(t, "EME-3", refs[2].Identifier)
}

func TestFindIssueRefsWithoutKnownTeamsFindsNothing(t *testing.T) {
	assert.Empty(t, FindIssueRefs("EME-1", nil, 0))
}

func TestParseIssueRef(t *testing.T) {
	cases := map[string]IssueRef{
		"EME-6":                                 {Identifier: "EME-6"},
		" eme-6 ":                               {Identifier: "EME-6"},
		"https://linear.app/x/issue/EME-6/slug": {Identifier: "EME-6"},
		"9F3C2A10-0000-4000-8000-00000000ABCD":  {ID: "9f3c2a10-0000-4000-8000-00000000abcd"},
		"9f3c2a10-0000-4000-8000-00000000abcd":  {ID: "9f3c2a10-0000-4000-8000-00000000abcd"},
	}
	for in, want := range cases {
		got, ok := ParseIssueRef(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "EME", "EME-", "EME-6 and more", "https://example.com/issue/EME-6", "-6"} {
		_, ok := ParseIssueRef(in)
		assert.False(t, ok, in)
	}
	assert.Equal(t, "EME", IssueRef{Identifier: "EME-6"}.TeamKey())
	assert.Equal(t, "", IssueRef{ID: "x"}.TeamKey())
}
