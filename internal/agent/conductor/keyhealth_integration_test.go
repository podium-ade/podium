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
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// fakeKeys is the control plane's view of the stored model keys: which providers are
// refusing theirs, and which a turn asked to have checked.
type fakeKeys struct {
	mu        sync.Mutex
	refused   map[string]bool
	suspected []string
}

func (k *fakeKeys) KeyRefused(_ context.Context, provider string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.refused[provider], nil
}

func (k *fakeKeys) SuspectKey(provider string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.suspected = append(k.suspected, provider)
}

func (k *fakeKeys) Suspected() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.suspected...)
}

// A refused key stops the turn before a task exists: one plain sentence, no sandbox, and
// none of the provider's words on the conversation.
func TestATurnOnARefusedKeyDoesNotStart(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	keys := &fakeKeys{refused: map[string]bool{profiles.ProviderAnthropic: true}}
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Keys = keys })

	ev := threadInbound("C1/9.1", "review this")
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "the turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})

	assert.Empty(t, fake.Specs(), "no task is started on a key the provider refuses")
	failures := posts(src.Records(), conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "Anthropic is refusing this bot's API key")
	assert.Contains(t, failures[0].Text, "nothing ran")
}

// A turn the provider refused asks for the key to be checked now, so the next turn is
// stopped by the gate rather than failing the same way. Both runtimes are recognised: the
// one that says its own sentence and the one that relayed the provider's error.
func TestATurnRefusedItsKeyAsksForACheck(t *testing.T) {
	for name, answer := range map[string]string{
		"current runtime": "I could not answer: the model provider refused this bot's API key. An operator needs to replace it.",
		"older runtime":   "The turn failed before I could answer: invalid x-api-key",
	} {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			fake := newFakePodium(t)
			fake.terminal = podiumv1.TaskStatus_TASK_STATUS_FAILED
			exit := int32(4)
			fake.exitCode = &exit
			fake.events = func(taskID string) []*podiumv1.TaskEvent {
				return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, answer)}
			}
			src := fakesource.New(conductor.KindDev)
			t.Cleanup(src.Close)
			keys := &fakeKeys{refused: map[string]bool{}}
			startWith(t, st, fake, src, func(o *conductor.Options) { o.Keys = keys })

			ev := threadInbound("C1/9.2", "review this")
			require.NoError(t, src.Send(context.Background(), ev))
			waitFor(t, 30*time.Second, "the turn to fail", func() bool {
				return turnStatus(st, ev.SourceKey) == store.TurnFailed
			})
			assert.Equal(t, []string{profiles.ProviderAnthropic}, keys.Suspected())
		})
	}
}

// Any other failure is not the key's fault and asks for nothing.
func TestAnOrdinaryFailureDoesNotSuspectTheKey(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.terminal = podiumv1.TaskStatus_TASK_STATUS_FAILED
	exit := int32(4)
	fake.exitCode = &exit
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal,
			"The turn failed before I could answer: Overloaded")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	keys := &fakeKeys{refused: map[string]bool{}}
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Keys = keys })

	ev := threadInbound("C1/9.3", "review this")
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "the turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})
	assert.Empty(t, keys.Suspected())
}
