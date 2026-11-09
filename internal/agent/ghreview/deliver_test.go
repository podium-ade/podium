package ghreview

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// recordingSlack is a SlackBridge that keeps what it was asked to post.
type recordingSlack struct {
	mu    sync.Mutex
	posts []string
}

func (r *recordingSlack) FetchTranscript(context.Context, string) ([]conductor.BriefEntry, error) {
	return nil, nil
}

func (r *recordingSlack) Post(_ context.Context, ref string, _ conductor.Outbound) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, ref)
	return "ts", nil
}

func (r *recordingSlack) Edit(context.Context, string, string, conductor.Outbound) error { return nil }
func (r *recordingSlack) Attach(context.Context, string, conductor.Attachment) error     { return nil }
func (r *recordingSlack) React(context.Context, string, conductor.Reaction) error        { return nil }

// recordingDeliverer is the conductor's outbox, as this package sees it.
type recordingDeliverer struct {
	mu     sync.Mutex
	handle bool
	refs   []string
}

func (d *recordingDeliverer) DeliverTo(_ context.Context, kind, ref string, _ conductor.Outbound) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if kind == conductor.SourceSlack {
		d.refs = append(d.refs, ref)
	}
	return d.handle
}

// The Slack copy of a pull request's answer goes through the conductor's outbox, so a copy
// that fails is retried like the answer itself. Without a deliverer that takes it (a source
// built without a conductor, or a post outside a turn), the copy is posted directly as before.
func TestASlackCopyOfAnAnswerIsHandedToTheOutbox(t *testing.T) {
	h := newHarness(t)
	slack := &recordingSlack{}
	h.src.slack = slack
	ref := Ref(Parsed{Owner: "acme", Repo: "repo", Number: 12, InstallID: 42, Kind: KindSlack, SlackChan: "C1", SlackThread: "1.1", SlackTS: "2.2"})
	ctx := context.Background()

	d := &recordingDeliverer{handle: true}
	h.src.SetDeliverer(d)
	_, err := h.src.Post(ctx, ref, conductor.Outbound{Type: conductor.OutFinal, Text: "it adds a retry"})
	require.NoError(t, err)
	assert.Equal(t, []string{slackCoord("C1", "1.1", "2.2")}, d.refs, "the copy was owed through the outbox")
	assert.Empty(t, slack.posts, "and not posted a second time directly")

	d.handle = false
	_, err = h.src.Post(ctx, ref, conductor.Outbound{Type: conductor.OutFinal, Text: "again"})
	require.NoError(t, err)
	assert.Len(t, slack.posts, 1, "a deliverer that declines leaves the copy to the source")

	h.src.SetDeliverer(nil)
	_, err = h.src.Post(ctx, ref, conductor.Outbound{Type: conductor.OutFinal, Text: "once more"})
	require.NoError(t, err)
	assert.Len(t, slack.posts, 2, "no deliverer at all is the old behaviour")
}
