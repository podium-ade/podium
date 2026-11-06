package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// kindStub is a Source that is only ever asked its kind; refs builds a GitHub ref from a key.
type kindStub struct {
	Source
	kind string
	refs func(string) (string, bool)
}

func (k kindStub) Kind() string { return k.kind }

func (k kindStub) RefForKey(key string) (string, bool) {
	if k.refs == nil {
		return "", false
	}
	return k.refs(key)
}

// A web message in a pull request's chat reads the PR's earlier history from GitHub, not from
// the chat source that carried it; a GitHub-started turn keeps its own ref.
func TestAPullRequestsEarlierHistoryIsReadFromGitHub(t *testing.T) {
	gh := kindStub{kind: SourceGitHub, refs: func(key string) (string, bool) {
		return "acme/repo/12/0/pull_request/0", key == "github:acme/repo#12"
	}}
	web := kindStub{kind: SourceChat}
	c := &Conductor{sources: []Source{web, gh}}

	src, ref, ok := c.githubFor(web, "chat_1", "github:acme/repo#12")
	assert.True(t, ok)
	assert.Equal(t, SourceGitHub, src.Kind())
	assert.Equal(t, "acme/repo/12/0/pull_request/0", ref, "a ref for the pull request, not the chat")

	src, ref, ok = c.githubFor(gh, "acme/repo/12/42/issue_comment/7", "github:acme/repo#12")
	assert.True(t, ok)
	assert.Equal(t, SourceGitHub, src.Kind())
	assert.Equal(t, "acme/repo/12/42/issue_comment/7", ref, "GitHub's own turn keeps its ref")

	_, _, ok = (&Conductor{sources: []Source{web}}).githubFor(web, "chat_1", "github:acme/repo#12")
	assert.False(t, ok, "no GitHub source configured: nothing to read")
}
