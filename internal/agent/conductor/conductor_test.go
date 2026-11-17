package conductor

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// A Slack thread is a CONVERSATION now: the assistant answers it on this host and delegates
// the work, which is what lets a mention reach a playbook that a mention could never route
// to. Before this it was a task running whichever playbook the channel mapped to, and the
// only reachable one was the default.
func TestAConversationIsAnsweredHereForChatAndSlack(t *testing.T) {
	assert.True(t, aConversation(SourceChat))
	assert.True(t, aConversation(SourceSlack))
	assert.True(t, aConversation(SourceGitHub), "a pull request is talked about; the review is a delegation")
	assert.True(t, aConversation(KindDev), "the dev source is how a conversation is tested")
	assert.False(t, aConversation("linear"), "a ticket is one piece of work, not a conversation")
}

// The runtime's schema allows four source kinds, and a DELEGATED task used to be told it
// came from the web chat whatever asked for it. Harmless while only chats could delegate;
// wrong the moment a Slack thread could, because the prompt names where the answer is going.
func TestBriefKindForNamesTheRealSource(t *testing.T) {
	assert.Equal(t, SourceSlack, briefKindFor(SourceSlack))
	assert.Equal(t, "linear", briefKindFor("linear"))
	assert.Equal(t, SourceChat, briefKindFor(SourceChat))
	assert.Equal(t, SourceGitHub, briefKindFor(SourceGitHub))
	assert.Equal(t, SourceChat, briefKindFor(KindDev), "the test-only source presents as chat")
}

// The cap is on this HOST, because a host turn is a process on it. Slack decides how many
// conversations there are, so the number of them running at once cannot.
func TestHostSlotsQueueBeyondTheCap(t *testing.T) {
	c := &Conductor{
		logger:    slog.New(slog.DiscardHandler),
		metrics:   NewMetrics(nil),
		hostSlots: make(chan struct{}, 2),
	}
	ctx := t.Context()

	first, ok := c.acquireHostSlot(ctx)
	require.True(t, ok)
	second, ok := c.acquireHostSlot(ctx)
	require.True(t, ok)

	// The third waits, and is let through the moment a slot comes back.
	got := make(chan struct{})
	go func() {
		third, ok := c.acquireHostSlot(ctx)
		if ok {
			third()
			close(got)
		}
	}()
	select {
	case <-got:
		t.Fatal("a third turn ran while both slots were held")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued turn never got the slot that was given back")
	}
	second()
}

// A conductor that is stopping does not leave a queued turn waiting forever, and hands back
// nothing to release.
func TestHostSlotsGiveUpWhenTheConductorStops(t *testing.T) {
	c := &Conductor{
		logger:    slog.New(slog.DiscardHandler),
		metrics:   NewMetrics(nil),
		hostSlots: make(chan struct{}, 1),
	}
	held, ok := c.acquireHostSlot(context.Background())
	require.True(t, ok)
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, ok := c.acquireHostSlot(ctx)
	assert.False(t, ok)
	assert.NotNil(t, release, "the caller may still defer it")
	release()
}

// Every way a turn can end, and the mark it leaves. ✅ only for a turn that worked; ❌ for
// every other ending, cancellation included.
func TestReactionForEveryEnding(t *testing.T) {
	for status, want := range map[string]Reaction{
		store.TurnSucceeded: ReactionDone,
		store.TurnFailed:    ReactionFailed,
		store.TurnLost:      ReactionFailed,
		store.TurnCancelled: ReactionFailed,
		store.TurnTimeout:   ReactionFailed,
	} {
		assert.Equal(t, want, reactionFor(status), status)
	}
}

// Anybody the GitHub triggers allow can comment on a pull request, so a turn there is
// offered the review playbook, and another playbook only when the speaker's role meets its
// github_requires. Every other source gets the whole profile.
func TestAPullRequestOffersPlaybooksByTheSpeakersRole(t *testing.T) {
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{
		Name: "podium", Model: "claude-opus-5",
		Playbooks: map[string]profiles.Playbook{
			"coder":  {Name: "coder", SystemPrompt: "reviews pull requests", GitHub: true},
			"deploy": {Name: "deploy", SystemPrompt: "ships to production"},
			"fixer":  {Name: "fixer", SystemPrompt: "fixes the review", GitHubRequires: "write"},
		},
	})}
	names := func(menu []DelegablePlaybook) []string { return playbookNames(menu) }
	assert.Equal(t, []string{"coder"}, names(c.menuFor(kindSource{SourceGitHub}, "github:acme/app#1", "")),
		"no role, as from Slack: the review alone")
	assert.Equal(t, []string{"coder"}, names(c.menuFor(kindSource{SourceGitHub}, "github:acme/app#1", "triage")))
	assert.Equal(t, []string{"coder", "fixer"}, names(c.menuFor(kindSource{SourceGitHub}, "github:acme/app#1", "write")))
	assert.Equal(t, []string{"coder", "fixer"}, names(c.menuFor(kindSource{SourceGitHub}, "github:acme/app#1", "admin")),
		"a playbook that names no role is never offered on a pull request")
	assert.True(t, c.menuFor(kindSource{SourceGitHub}, "github:acme/app#1", "")[0].GitHub)

	assert.Len(t, c.menuFor(kindSource{SourceSlack}, "", ""), 3)

	assert.Equal(t, Speaker{Playbook: "coder", SourcePlaybook: true}, c.speakerFor(kindSource{SourceGitHub}, "coder"))
	assert.Equal(t, Speaker{Playbook: "deploy"}, c.speakerFor(kindSource{SourceGitHub}, "deploy"))
	assert.Equal(t, Speaker{Playbook: "coder"}, c.speakerFor(kindSource{SourceSlack}, "coder"),
		"only a pull request has a playbook of its own")
	assert.Equal(t, Speaker{}, c.speakerFor(kindSource{SourceGitHub}, ""), "the assistant runs no playbook")
}

// A playbook naming a repository in github_repos reviews that repository's pull requests in
// place of the github: true one, which still reviews every other repository. A pull request's
// menu carries only its own repository's reviewer, and the github_requires playbooks that
// work in the same first repository.
func TestAPullRequestIsReviewedByItsRepositorysPlaybook(t *testing.T) {
	api := []profiles.Repo{{Name: "api", URL: "https://github.com/acme/api", DefaultBranch: "main"}}
	web := []profiles.Repo{{Name: "web", URL: "https://github.com/acme/web", DefaultBranch: "main"}}
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{
		Name: "podium", Model: "claude-opus-5",
		Playbooks: map[string]profiles.Playbook{
			"general":    {Name: "general", GitHub: true, Repos: web},
			"api-review": {Name: "api-review", GitHubRepos: []string{"Acme/API"}, Repos: api},
			"api-fixer":  {Name: "api-fixer", GitHubRequires: "write", Repos: api},
			"web-fixer":  {Name: "web-fixer", GitHubRequires: "write", Repos: web},
		},
	})}
	assert.Equal(t, []string{"api-fixer", "api-review"},
		playbookNames(c.menuFor(kindSource{SourceGitHub}, "github:acme/api#7", "write")))
	assert.Equal(t, []string{"general", "web-fixer"},
		playbookNames(c.menuFor(kindSource{SourceGitHub}, "github:acme/web#3", "write")))
	assert.Equal(t, []string{"general"},
		playbookNames(c.menuFor(kindSource{SourceGitHub}, "github:other/thing#1", "read")))

	assert.Equal(t, Speaker{Playbook: "api-review", SourcePlaybook: true},
		c.speakerFor(kindSource{SourceGitHub}, "api-review"))
	assert.Equal(t, Speaker{Playbook: "api-fixer"}, c.speakerFor(kindSource{SourceGitHub}, "api-fixer"))
}
