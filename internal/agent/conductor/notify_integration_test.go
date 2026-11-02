//go:build integration

package conductor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

type sentNotice struct{ login, subject, body string }

type fakeNotifier struct {
	mu   sync.Mutex
	sent []sentNotice
}

func (*fakeNotifier) Name() string { return "fake" }

func (f *fakeNotifier) Notify(_ context.Context, login, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentNotice{login, subject, body})
	return nil
}

func (f *fakeNotifier) all() []sentNotice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentNotice(nil), f.sent...)
}

// TestAPersonWhoLeftHearsTheirTaskEnded: a web chat's person who has said nothing for a while
// is told, outside the chat, that the task they asked for finished — with its first line and
// a link to the chat. One still in the conversation is not.
func TestAPersonWhoLeftHearsTheirTaskEnded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		awayAfter  time.Duration
		wantNotice bool
	}{{"gone", time.Millisecond, true}, {"still here", time.Hour, false}} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			fake := newFakePodium(t)
			fake.events = func(taskID string) []*podiumv1.TaskEvent {
				return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "opened #51 and the suite is green\nmore detail")}
			}
			src := fakesource.New(conductor.SourceChat)
			t.Cleanup(src.Close)
			notices := &fakeNotifier{}
			r := startWith(t, st, fake, src, func(o *conductor.Options) {
				o.Notifiers = []conductor.Notifier{notices}
				o.NotifyAwayAfter = tc.awayAfter
				o.PublicURL = "https://podium.acme.com"
			})

			ctx := context.Background()
			chat, err := st.CreateChat(ctx, "ada@acme.com", "Fix the build")
			require.NoError(t, err)
			sess, err := st.UpsertSession(ctx, store.Session{SourceKind: conductor.SourceChat,
				SourceKey: store.ChatSourceKey(chat.ID), Playbook: "general"})
			require.NoError(t, err)
			ref := chat.ID
			src.Heard(ref, conductor.BriefEntry{Role: conductor.RoleUser, Author: "ada@acme.com",
				TS: conductor.BriefTimestamp(time.Now()), Text: "fix the build please"})
			time.Sleep(5 * time.Millisecond)
			turn, err := st.CreateTurn(ctx, sess.ID, ref, store.Backend{})
			require.NoError(t, err)
			_, err = r.cond.DelegateForTest(ctx, src, sess.ID, turn.ID, ref, "dogfood", "fix it", conductor.DelegateOptions{})
			require.NoError(t, err)

			waitFor(t, 30*time.Second, "the delegation to finish", func() bool {
				dlgs, _ := st.FinishedDelegationsForSession(ctx, sess.ID, time.Time{}, 5)
				return len(dlgs) == 1
			})
			time.Sleep(200 * time.Millisecond)
			sent := notices.all()
			if !tc.wantNotice {
				assert.Empty(t, sent, "the person is still in the conversation")
				return
			}
			require.Len(t, sent, 1)
			assert.Equal(t, "ada@acme.com", sent[0].login)
			assert.Equal(t, "Your dogfood task finished", sent[0].subject)
			assert.Contains(t, sent[0].body, "“Fix the build”")
			assert.Contains(t, sent[0].body, "opened #51 and the suite is green")
			assert.NotContains(t, sent[0].body, "more detail", "the first line only")
			assert.Contains(t, sent[0].body, "https://podium.acme.com/agent/chat/"+chat.ID)
		})
	}
}
