package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/github"
)

func TestReplaceSource(t *testing.T) {
	slack, chat := &kindSource{SourceSlack}, &kindSource{SourceChat}
	c := &Conductor{sources: []Source{slack, chat}}

	next := &kindSource{SourceSlack}
	c.ReplaceSource(SourceSlack, next)
	assert.Same(t, next, c.sourceOf(SourceSlack), "a session's replies go to the new source")
	assert.Same(t, chat, c.sourceOf(SourceChat), "other kinds are left alone")
	assert.Len(t, c.sourceList(), 2)

	c.ReplaceSource(SourceSlack, nil)
	assert.Nil(t, c.sourceOf(SourceSlack), "nil turns the kind off")
	assert.Len(t, c.sourceList(), 1)

	gh := &kindSource{SourceGitHub}
	c.ReplaceSource(SourceGitHub, gh)
	assert.Same(t, gh, c.sourceOf(SourceGitHub), "a kind the conductor started without can be added")
}

func TestSetGitHub(t *testing.T) {
	app := &github.Client{}

	t.Run("refuses an App with nowhere to redeem a capability", func(t *testing.T) {
		c := &Conductor{}
		require.Error(t, c.SetGitHub(app))
		assert.Nil(t, c.githubApp())
	})

	t.Run("swaps the App and turns it off", func(t *testing.T) {
		c := &Conductor{mintSecret: "conductor-bearer", gitTaskURL: "http://conductor:8090"}
		require.NoError(t, c.SetGitHub(app))
		assert.Same(t, app, c.githubApp())

		require.NoError(t, c.SetGitHub(nil))
		assert.Nil(t, c.githubApp())
	})
}
