package conductor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
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
