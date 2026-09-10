package conductor

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

func TestATurnTokenIsOneTurnsAuthorityAndNoLonger(t *testing.T) {
	c := &Conductor{}
	grant := turnGrant{turnID: "turn_01", sessionID: "sess_01", ref: "chat_01", playbooks: []string{"podium"}}

	token, err := c.mintTurnToken(grant)
	require.NoError(t, err)
	// 32 bytes of crypto/rand, hex: a bearer for a conversation, not an id.
	assert.Len(t, token, 64)

	got, ok := c.grantFor(token)
	require.True(t, ok)
	assert.Equal(t, grant, got)

	c.revokeTurnToken(token)
	_, ok = c.grantFor(token)
	assert.False(t, ok, "a token must not outlive the turn it was minted for")
}

func TestEveryTurnTokenIsDifferent(t *testing.T) {
	c := &Conductor{}
	seen := map[string]bool{}
	for range 50 {
		token, err := c.mintTurnToken(turnGrant{turnID: "turn_01"})
		require.NoError(t, err)
		assert.False(t, seen[token], "a repeated token would let one turn act as another")
		seen[token] = true
	}
}

func TestAnUnknownTokenIsRefusedByEveryCall(t *testing.T) {
	c := &Conductor{}
	ctx := context.Background()

	_, err := c.Delegate(ctx, "not-a-token", "podium", "do it")
	assert.ErrorIs(t, err, ErrNoTurn)

	_, _, err = c.GetDelegation(ctx, "not-a-token", "dlg_01")
	assert.ErrorIs(t, err, ErrNoTurn)

	_, err = c.Delegations(ctx, "not-a-token")
	assert.ErrorIs(t, err, ErrNoTurn)

	_, err = c.CancelDelegation(ctx, "not-a-token", "dlg_01", "")
	assert.ErrorIs(t, err, ErrNoTurn)
}

func TestDelegateRefusesAPlaybookTheTurnWasNotOffered(t *testing.T) {
	c := &Conductor{}
	token, err := c.mintTurnToken(turnGrant{turnID: "turn_01", playbooks: []string{"podium"}})
	require.NoError(t, err)

	// The menu is the allow-list. A playbook that exists but was not offered is refused
	// exactly as one that does not exist: a turn may not reach a playbook by guessing.
	_, err = c.Delegate(context.Background(), token, "release", "ship it")
	assert.ErrorIs(t, err, ErrPlaybookNotOffered)
	assert.Contains(t, err.Error(), "release")
	assert.Contains(t, err.Error(), "podium", "the message says what this turn may ask for")
}

func TestDelegateNeedsAnInstruction(t *testing.T) {
	c := &Conductor{}
	token, err := c.mintTurnToken(turnGrant{turnID: "turn_01", playbooks: []string{"podium"}})
	require.NoError(t, err)

	_, err = c.Delegate(context.Background(), token, "podium", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instruction")
}

func TestTheDelegationMenuIsEveryPlaybookInTheProfile(t *testing.T) {
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{
		Name: "podium",
		Playbooks: map[string]profiles.Playbook{
			"general": {
				Name:         "general",
				SystemPrompt: "Answer questions about anything.\nMore detail follows.",
			},
			"podium": {
				Name:         "podium",
				SystemPrompt: "You work on Podium itself: the control plane and the node daemon.",
				Docker:       true,
				Browser:      true,
				Repos:        []profiles.Repo{{Name: "podium"}},
			},
		},
	})}

	menu := c.DelegablePlaybooks()
	require.Len(t, menu, 2, "every playbook is delegable, including the one the chat is running")
	assert.Equal(t, []string{"general", "podium"}, playbookNames(menu))

	// What a playbook can REACH, which is what a model choosing between them needs.
	podium := menu[1]
	assert.True(t, podium.Docker)
	assert.True(t, podium.Browser)
	assert.Equal(t, []string{"podium"}, podium.Repos)
	assert.Equal(t, "You work on Podium itself: the control plane and the node daemon.", podium.Summary)

	// A playbook with no repositories says so by carrying none rather than an empty list.
	assert.Nil(t, menu[0].Repos)
	assert.Equal(t, "Answer questions about anything.", menu[0].Summary,
		"a summary is the first line of the prompt, not the whole thing")
}

func TestAPlaybookSummaryIsBoundedAndSingleLine(t *testing.T) {
	long := profiles.Playbook{SystemPrompt: repeat("y", 400)}
	summary := playbookSummary(long)
	assert.LessOrEqual(t, len([]rune(summary)), maxSummaryRunes+1, "one line of a menu, not a prompt")
	assert.NotContains(t, summary, "\n")

	assert.Equal(t, "", playbookSummary(profiles.Playbook{}), "no prompt is no summary")
	assert.Equal(t, "First line.", playbookSummary(profiles.Playbook{SystemPrompt: "  First line.\n\nSecond."}))
}

func TestFirstLineIsBoundedAndTrimmed(t *testing.T) {
	assert.Equal(t, "hello", firstLine("  hello  \nworld", 100))
	assert.Equal(t, "", firstLine("", 100))
	assert.Equal(t, "abc…", firstLine("abcdef", 3))
	// A multi-byte character must not be cut in half.
	assert.Equal(t, "héllo…", firstLine("héllo wörld", 6))
}

func TestPlaybookNamesIsTheMenusNames(t *testing.T) {
	assert.Empty(t, playbookNames(nil))
	assert.Equal(t, []string{"a", "b"},
		playbookNames([]DelegablePlaybook{{Name: "a"}, {Name: "b"}}))
}

func TestADelegationRunRemembersTheLastThingItsTaskSaid(t *testing.T) {
	r := &delegationRun{}
	assert.Empty(t, r.progressText())
	r.setProgress("reading the handler")
	assert.Equal(t, "reading the handler", r.progressText())
	r.setProgress("running the tests")
	assert.Equal(t, "running the tests", r.progressText())
}

func TestProgressOfADelegationThisProcessIsNotFollowingIsEmpty(t *testing.T) {
	c := &Conductor{}
	assert.Empty(t, c.delegationProgress("dlg_nobody_is_following"))

	run := &delegationRun{}
	run.setProgress("still going")
	c.liveDelegations = map[string]*delegationRun{"dlg_01": run}
	assert.Equal(t, "still going", c.delegationProgress("dlg_01"))
}

func TestContainsString(t *testing.T) {
	assert.True(t, containsString([]string{"a", "b"}, "b"))
	assert.False(t, containsString([]string{"a", "b"}, "c"))
	assert.False(t, containsString(nil, ""))
}

// repeat is strings.Repeat without the import, kept local so this file's imports stay the
// ones the tests are about.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

// sayingSource is chatSource that remembers what was posted to it. fakesource would do the
// job and cannot be used: it imports this package, so an in-package test importing it back
// is a cycle — and these tests reach unexported state, so they have to be in-package.
type sayingSource struct {
	chatSource
	mu    sync.Mutex
	posts []Outbound
	refs  []string
}

func (s *sayingSource) Post(_ context.Context, ref string, out Outbound) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts = append(s.posts, out)
	s.refs = append(s.refs, ref)
	return "1", nil
}

func (s *sayingSource) said() []Outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Outbound(nil), s.posts...)
}

// announcingConductor is the smallest conductor the held-announcement paths need: a logger
// for the post failure path, and nothing else.
func announcingConductor() *Conductor {
	return &Conductor{logger: slog.New(slog.DiscardHandler)}
}

// TestATurnEndingSaysWhatItStarted is the half the integration test cannot pin: a delegated
// task that has not spoken yet is announced when the turn that started it ends, so the line
// follows the assistant's own words instead of pre-empting them.
func TestATurnEndingSaysWhatItStarted(t *testing.T) {
	src := &sayingSource{}
	c := announcingConductor()

	c.holdAnnouncement(announcement{
		dlgID: "dlg_1", turnID: "turn_1", src: src, ref: "C1/1.1", taskID: "task_01",
		text: "Working on this in a `dogfood` task: go",
	})
	c.sayAnnouncementsFor(context.Background(), "turn_1")

	posts := src.said()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0].Text, "Working on this in a `dogfood` task")
	assert.Equal(t, OutProgress, posts[0].Type, "an announcement is progress, not an answer")
	assert.Equal(t, "task_01", posts[0].TaskID, "and it names the task it introduces")

	// Exactly once: the task speaking afterwards must not repeat it.
	c.sayAnnouncement(context.Background(), "dlg_1")
	assert.Len(t, src.said(), 1)
}

// And the other order, which is what happens when a container is quick: the task's first
// word takes the line, and the turn ending afterwards has nothing left to say.
func TestATaskSpeakingFirstTakesTheAnnouncement(t *testing.T) {
	src := &sayingSource{}
	c := announcingConductor()

	c.holdAnnouncement(announcement{
		dlgID: "dlg_1", turnID: "turn_1", src: src, ref: "C1/1.1", taskID: "task_01", text: "go",
	})
	c.sayAnnouncement(context.Background(), "dlg_1")
	require.Len(t, src.said(), 1)

	c.sayAnnouncementsFor(context.Background(), "turn_1")
	assert.Len(t, src.said(), 1, "one delegation, one announcement, whichever path got there first")
}

// Two delegations from one turn are announced in the order they were started, because that
// is the order the person asked for them in.
func TestTwoDelegationsAreAnnouncedInOrder(t *testing.T) {
	src := &sayingSource{}
	c := announcingConductor()

	c.holdAnnouncement(announcement{dlgID: "dlg_1", turnID: "turn_1", src: src, ref: "C1/1.1", text: "first"})
	c.holdAnnouncement(announcement{dlgID: "dlg_2", turnID: "turn_1", src: src, ref: "C1/1.1", text: "second"})
	c.sayAnnouncementsFor(context.Background(), "turn_1")

	posts := src.said()
	require.Len(t, posts, 2)
	assert.Equal(t, "first", posts[0].Text)
	assert.Equal(t, "second", posts[1].Text)
}

// A held line belongs to ONE turn: another turn ending must not say it.
func TestAnotherTurnEndingSaysNothing(t *testing.T) {
	src := &sayingSource{}
	c := announcingConductor()

	c.holdAnnouncement(announcement{dlgID: "dlg_1", turnID: "turn_1", src: src, ref: "C1/1.1", text: "mine"})
	c.sayAnnouncementsFor(context.Background(), "turn_2")
	assert.Empty(t, src.said())

	c.sayAnnouncementsFor(context.Background(), "turn_1")
	assert.Len(t, src.said(), 1)
}
