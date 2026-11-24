package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMentionsPodium(t *testing.T) {
	cases := []struct {
		text, personality string
		want              bool
	}{
		{"@Podium what changed?", "", true},
		{"what changed, @podium?", "", true},
		{"thanks @PODIUM.", "", true},
		{"ask @Podium\nplease", "", true},
		{"no mention here", "", false},
		{"mail alice@podium.dev", "", false},
		{"@podiums are not it", "", false},
		{"x@Podium", "", false},
		{"@Podium-bot", "", false},
		// A person chip whose display name happens to be Podium is a person, not the bot.
		{"@[Podium](mention:podium@example.com) hi", "", false},
		// A personal assistant answers to its own name, and Podium's still works.
		{"@Researcher dig into this", "Researcher", true},
		{"@research bot dig into this", "Research Bot", true},
		{"@Researcher dig into this", "", false},
		{"@Podium dig in", "Researcher", true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, MentionsPodium(c.text, c.personality), "%q / %q", c.text, c.personality)
	}
}

func TestMentionedLogins(t *testing.T) {
	text := "@[Alice](mention:Alice@Example.com) and @[Bob B](mention:bob@example.com), " +
		"again @[Alice](mention:alice@example.com); not @carol"
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, MentionedLogins(text))
	assert.Empty(t, MentionedLogins("@Podium only"))
}

func TestMentionsBrand(t *testing.T) {
	named := &Source{brand: func(context.Context) string { return "AI Buddy" }}
	assert.True(t, named.mentionsBrand(context.Background(), "@AI Buddy what changed?"))
	assert.True(t, named.mentionsBrand(context.Background(), "thanks, @ai buddy."))
	// The boundary rule is the same as for every other name.
	assert.False(t, named.mentionsBrand(context.Background(), "@AI Buddys are not it"))
	assert.False(t, named.mentionsBrand(context.Background(), "@[AI Buddy](mention:a@example.com) hi"))
	assert.False(t, named.mentionsBrand(context.Background(), "no mention"))

	// A deployment that was never named, or a Source built without a name to ask for, has no
	// brand mention; "@Podium" is still the house name and is handled by MentionsPodium.
	blank := &Source{brand: func(context.Context) string { return "" }}
	assert.False(t, blank.mentionsBrand(context.Background(), "@AI Buddy hi"))
	assert.False(t, (&Source{}).mentionsBrand(context.Background(), "@AI Buddy hi"))
}
