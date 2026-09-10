package chat

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/store"
)

// linker is conductor's own optional half of Source, restated here because it is
// unexported there. If this stops compiling, the conductor has stopped calling the chat.
type linker interface {
	LinkPullRequests(ctx context.Context, ref string, prs []conductor.PullRequest) error
}

var _ linker = (*Source)(nil)

// pr is one pull request read out of a turn's answer, the way the conductor produces them.
func pr(owner, repo string, number int) conductor.PullRequest {
	url := "https://github.com/" + owner + "/" + repo + "/pull/" + strconv.Itoa(number)
	found := conductor.FindPullRequests("I opened " + url + " with the fix.")
	return found[0]
}

// nextPullRequests reads the next pull-request frame, or fails rather than hanging.
func nextPullRequests(t *testing.T, sub *Subscriber) []store.ChatPullRequest {
	t.Helper()
	for {
		select {
		case f := <-sub.Frames():
			if f.Kind != FramePullRequests {
				continue
			}
			return f.PullRequests
		case <-time.After(2 * time.Second):
			t.Fatal("no pull-request frame arrived")
			return nil
		}
	}
}

func TestATurnLinksThePullRequestItNamedAndSaysSo(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	require.NoError(t, src.LinkPullRequests(context.Background(), "chat_1",
		[]conductor.PullRequest{pr("acme", "api", 7)}))

	linked := nextPullRequests(t, sub)
	require.Len(t, linked, 1)
	assert.Equal(t, "https://github.com/acme/api/pull/7", linked[0].URL)
	assert.Equal(t, store.PullRequestFromTurn, linked[0].Source,
		"a link a turn found records that a turn found it")
}

func TestASecondTurnNamingTheSamePullRequestLinksNothing(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)

	ctx := context.Background()
	require.NoError(t, src.LinkPullRequests(ctx, "chat_1", []conductor.PullRequest{pr("acme", "api", 7)}))

	sub := src.Subscribe(ctx, "chat_1")
	defer sub.Close()
	require.NoError(t, src.LinkPullRequests(ctx, "chat_1", []conductor.PullRequest{pr("acme", "api", 7)}))

	select {
	case f := <-sub.Frames():
		t.Fatalf("nothing changed, so nothing should have been published: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
	prs, err := st.ListChatPullRequests(ctx, "chat_1")
	require.NoError(t, err)
	assert.Len(t, prs, 1, "one link, however many turns mention it")
}

func TestDetachingALinkKeepsTheNextTurnFromPuttingItBack(t *testing.T) {
	// The case this exists for: a human removes a link and then asks a follow-up in the
	// same chat. The next answer says "I updated the PR at …" and the link must stay gone,
	// because a person removing something means it.
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	require.NoError(t, src.LinkPullRequests(ctx, "chat_1", []conductor.PullRequest{pr("acme", "api", 7)}))
	left, err := src.DetachPullRequest(ctx, "chat_1", "alice", "https://github.com/acme/api/pull/7")
	require.NoError(t, err)
	assert.Empty(t, left)

	require.NoError(t, src.LinkPullRequests(ctx, "chat_1", []conductor.PullRequest{pr("acme", "api", 7)}))
	prs, err := st.ListChatPullRequests(ctx, "chat_1")
	require.NoError(t, err)
	assert.Empty(t, prs, "a turn does not undo a human's removal")

	// Attaching it by hand does, because that is the one case where the person is asking
	// for it back.
	back, err := src.AttachPullRequest(ctx, "chat_1", "alice", "https://github.com/acme/api/pull/7")
	require.NoError(t, err)
	require.Len(t, back, 1)
	assert.Equal(t, store.PullRequestFromHuman, back[0].Source)
}

func TestAttachingByHandTakesTheURLTheBrowserIsShowing(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	sub := src.Subscribe(context.Background(), "chat_1")
	defer sub.Close()

	prs, err := src.AttachPullRequest(context.Background(), "chat_1", "alice",
		"  https://github.com/acme/api/pull/7/files  ")
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, "https://github.com/acme/api/pull/7", prs[0].URL)
	assert.Equal(t, 7, prs[0].Number)
	assert.Equal(t, store.PullRequestFromHuman, prs[0].Source)
	assert.Len(t, nextPullRequests(t, sub), 1, "every browser watching the chat is told")
}

func TestWhatTheManualAttachRefuses(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)
	ctx := context.Background()

	for _, raw := range []string{
		"", "not a url", "https://github.com/acme/api/issues/7", "https://example.com/pull/7",
	} {
		_, err := src.AttachPullRequest(ctx, "chat_1", "alice", raw)
		assert.ErrorIs(t, err, ErrNotPullRequestURL, "should be refused: %q", raw)
	}

	_, err := src.AttachPullRequest(ctx, "chat_1", "bob", "https://github.com/acme/api/pull/7")
	assert.ErrorIs(t, err, store.ErrNotFound, "another login's chat is not found, not forbidden")

	_, err = src.DetachPullRequest(ctx, "chat_1", "bob", "https://github.com/acme/api/pull/7")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDetachingSomethingThatIsNotLinked(t *testing.T) {
	st := newFakeStore()
	st.add("chat_1", "alice")
	src := newSource(t, st)

	_, err := src.DetachPullRequest(context.Background(), "chat_1", "alice",
		"https://github.com/acme/api/pull/7")
	assert.ErrorIs(t, err, store.ErrNotFound)
}
