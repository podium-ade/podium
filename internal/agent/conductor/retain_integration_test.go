//go:build integration

package conductor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// memoryTaskURL is where a task container would reach the memory service. Nothing dials it
// in these tests; it is the value that has to appear in the brief.
const memoryTaskURL = "http://host.docker.internal:8888"

// memoryKey is an obvious fake.
const memoryKey = "memtoken"

// ---------------------------------------------------------------------------
// a fake Hindsight
// ---------------------------------------------------------------------------

// fakeHindsight records every retain and can be told to fail, which is how "Hindsight is
// down" is simulated without stopping anything.
type fakeHindsight struct {
	srv *httptest.Server

	mu      sync.Mutex
	retains []retainedItem
	auths   []string
	status  int
}

// retainedItem is one item as it arrived on the wire.
type retainedItem struct {
	Content    string            `json:"content"`
	Context    string            `json:"context"`
	Tags       []string          `json:"tags"`
	Metadata   map[string]string `json:"metadata"`
	DocumentID string            `json:"document_id"`
}

func newFakeHindsight(t *testing.T) *fakeHindsight {
	t.Helper()
	f := &fakeHindsight{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		status := f.status
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		if status == http.StatusOK && strings.HasSuffix(r.URL.Path, "/memories") {
			var body struct {
				Items []retainedItem `json:"items"`
				Async bool           `json:"async"`
			}
			if json.Unmarshal(raw, &body) == nil {
				f.retains = append(f.retains, body.Items...)
			}
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"success":true,"bank_id":"podium","items_count":1,"async":true}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHindsight) client(t *testing.T) memory.Client {
	t.Helper()
	c, err := memory.New(memory.Options{
		BaseURL:    f.srv.URL,
		Bank:       "podium",
		APIKey:     memoryKey,
		HTTPClient: f.srv.Client(),
	})
	require.NoError(t, err)
	return c
}

func (f *fakeHindsight) fail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeHindsight) Retained() []retainedItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]retainedItem(nil), f.retains...)
}

func (f *fakeHindsight) Auths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

// withMemory attaches a brief memory block and a client to a conductor.
func withMemory(client memory.Client) func(*conductor.Options) {
	return func(o *conductor.Options) {
		o.Memory = &conductor.BriefMemory{
			MCPURL:    memory.MCPURL(memoryTaskURL, "podium"),
			APIKeyEnv: profiles.MemoryKeyEnv,
		}
		o.MemoryClient = client
	}
}

// slackish is an inbound event that is NOT from the dev source, which is what a retainable
// turn needs. Everything else about it matches the dev events the other tests use.
func slackish(ref, text string) conductor.InboundEvent {
	channel, thread, _ := strings.Cut(ref, "/")
	return conductor.InboundEvent{
		SourceKind: conductor.SourceSlack,
		SourceKey:  conductor.SourceSlack + ":" + channel + ":" + thread,
		Ref:        ref,
		Channel:    channel,
		Author:     "alice",
		Text:       text,
		TS:         time.Now().UTC(),
		URL:        "https://example.slack.com/archives/" + channel + "/p11",
		BriefKind:  conductor.SourceSlack,
	}
}

// answering is a fake control plane whose one task says `text` as its final.
func answering(fake *fakePodium, text string) {
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, text)}
	}
}

// awaitRetains waits for the fake to have seen n retains, then holds still long enough to
// catch an n+1 that should not happen.
func awaitRetains(t *testing.T, f *fakeHindsight, n int) []retainedItem {
	t.Helper()
	waitFor(t, 30*time.Second, "the retain to reach the memory service", func() bool {
		return len(f.Retained()) >= n
	})
	return f.Retained()
}

// ---------------------------------------------------------------------------
// the brief and the secret
// ---------------------------------------------------------------------------

// TestATurnCarriesTheMemoryURLAndTheMemorySecret is the contract with step 16's runtime: a
// brief with a memory block whose api_key_env is unset is a failed turn, so the secret ref
// is not optional and no playbook file decides whether it is there.
func TestATurnCarriesTheMemoryURLAndTheMemorySecret(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	hs := newFakeHindsight(t)
	answering(fake, "Bob owns the scheduler.")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ev := slackish("C1/1.1", "who owns the scheduler?")
	startWith(t, st, fake, src, withMemory(hs.client(t)))
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })
	taskSpec := fake.Specs()[0]

	brief := decodeBrief(t, taskSpec)
	require.NotNil(t, brief.Memory)
	assert.Equal(t, "http://host.docker.internal:8888/mcp/podium/", brief.Memory.MCPURL,
		"the bank in the path is what puts the memory service in single-bank mode")
	assert.Equal(t, profiles.MemoryKeyEnv, brief.Memory.APIKeyEnv)

	var names []string
	for _, ref := range taskSpec.GetSecrets() {
		names = append(names, ref.GetName())
		if ref.GetName() == profiles.MemoryKeySecret {
			assert.Equal(t, profiles.MemoryKeyEnv, ref.GetKey(),
				"the secret has to land in the variable the brief promised")
			assert.Equal(t, spec.SecretTargetEnv, ref.GetTarget())
		}
	}
	assert.Contains(t, names, profiles.MemoryKeySecret)
	assert.Contains(t, names, profiles.AnthropicKeySecret)
}

// With no memory configured a brief carries no memory block and the spec carries no memory
// secret. This is the whole of "memory is optional".
func TestWithoutMemoryATurnCarriesNeither(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	answering(fake, "Bob owns the scheduler.")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), slackish("C1/1.1", "who owns it?")))

	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })
	taskSpec := fake.Specs()[0]

	assert.Nil(t, decodeBrief(t, taskSpec).Memory)
	for _, ref := range taskSpec.GetSecrets() {
		assert.NotEqual(t, profiles.MemoryKeySecret, ref.GetName())
	}
}

// ---------------------------------------------------------------------------
// the retain
// ---------------------------------------------------------------------------

// TestASucceededTurnIsRetained is the required round trip: one turn, one item, with the
// template, the tags, the provenance and document_id = turn_id.
func TestASucceededTurnIsRetained(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	hs := newFakeHindsight(t)
	answering(fake, "Bob owns the scheduler, and has since the 12.4 release.")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ev := slackish("C1/1.1", "who owns the scheduler?")
	startWith(t, st, fake, src, withMemory(hs.client(t)))
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to succeed", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	items := awaitRetains(t, hs, 1)
	require.Len(t, items, 1, "one turn, one item")

	turn := turnOf(t, st, ev.SourceKey)
	got := items[0]

	// The exchange, in the words it was said in.
	assert.Equal(t,
		"alice asked: who owns the scheduler?\n\n"+
			"Podium answered: Bob owns the scheduler, and has since the 12.4 release.",
		got.Content)
	assert.Equal(t, "podium agent, playbook general", got.Context)
	assert.Equal(t, []string{"source:slack", "playbook:general"}, got.Tags)

	// document_id = turn_id is the whole idempotency story: retaining the same turn again
	// replaces what was there rather than adding a duplicate.
	assert.Equal(t, turn.ID, got.DocumentID)

	// The provenance a human traces a suspicious memory back through.
	assert.Equal(t, turn.ID, got.Metadata["turn_id"])
	assert.Equal(t, turn.TaskID, got.Metadata["task_id"])
	assert.Equal(t, ev.Ref, got.Metadata["source_ref"])
	assert.Equal(t, ev.URL, got.Metadata["source_url"])
	assert.NotEmpty(t, got.Metadata["session_id"])

	for _, auth := range hs.Auths() {
		assert.Equal(t, "Bearer "+memoryKey, auth)
	}
}

// Nothing about a memory belongs in the conversation. A retain is the bot's filing system,
// and the human asked a question.
func TestARetainSaysNothingToTheConversation(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	hs := newFakeHindsight(t)
	answering(fake, "Bob owns the scheduler.")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ev := slackish("C1/1.1", "who owns it?")
	startWith(t, st, fake, src, withMemory(hs.client(t)))
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to succeed", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	awaitRetains(t, hs, 1)

	assert.Len(t, posts(src.Records(), conductor.OutFinal), 1)
	assert.Empty(t, posts(src.Records(), conductor.OutFailure))
	assert.Equal(t, []string{
		string(conductor.ReactionWorking), string(conductor.ReactionDone),
	}, reactions(src.Records()))
}

// ---------------------------------------------------------------------------
// what is NOT retained
// ---------------------------------------------------------------------------

// TestAFailedTurnRetainsNothing covers the whole exclusion list at once: a turn that did not
// succeed, a turn from the test-only dev source (which is the only source that can put the
// runtime into dry run), and a turn that said nothing.
func TestATurnThatShouldNotBeRememberedIsNot(t *testing.T) {
	cases := map[string]struct {
		kind  string
		final string
		exit  int32
		fail  string
	}{
		"a failed turn":        {kind: conductor.SourceSlack, final: "I could not work it out.", exit: 1},
		"a dry run":            {kind: conductor.KindDev, final: "dry run: hello there", exit: 0},
		"a turn with no words": {kind: conductor.SourceSlack, final: "", exit: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			fake := newFakePodium(t)
			hs := newFakeHindsight(t)
			answering(fake, tc.final)
			if tc.exit != 0 {
				fake.exitCode = &tc.exit
				fake.terminal = podiumv1.TaskStatus_TASK_STATUS_FAILED
			}
			src := fakesource.New(tc.kind)
			t.Cleanup(src.Close)

			ev := slackish("C1/1.1", "who owns it?")
			ev.SourceKind = tc.kind
			ev.SourceKey = tc.kind + ":C1:1.1"
			startWith(t, st, fake, src, withMemory(hs.client(t)))
			require.NoError(t, src.Send(context.Background(), ev))

			waitFor(t, 30*time.Second, "the turn to finish", func() bool {
				s := turnStatus(st, ev.SourceKey)
				return s != "" && s != store.TurnRunning
			})
			// The turn is over. Anything the retain was going to do, it has done —
			// give it a moment anyway so this is not a race that passes for free.
			assert.Never(t, func() bool { return len(hs.Retained()) > 0 },
				2*time.Second, 100*time.Millisecond,
				"nothing about this turn belongs in shared memory")
		})
	}
}

// A redacted answer means the node caught a secret on its way out of the container. Whatever
// the sentence around it says, it must not become a fact every future turn reads.
func TestARedactedAnswerIsNotRetained(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	hs := newFakeHindsight(t)
	answering(fake, "the deploy key is [redacted:DEPLOY_KEY], which is why it worked")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ev := slackish("C1/1.1", "why did it work?")
	startWith(t, st, fake, src, withMemory(hs.client(t)))
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to succeed", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	// The answer still reached the human — that is step 15's known gap, not this step's
	// job — but not one byte of it reached the memory service.
	assert.Len(t, posts(src.Records(), conductor.OutFinal), 1)
	assert.Never(t, func() bool { return len(hs.Retained()) > 0 },
		2*time.Second, 100*time.Millisecond)
}

// TestAMemoryOutageDoesNotCostTheAnswer is the acceptance item: with the memory service
// returning errors the turn still runs, still answers, still succeeds and still gets its ✅.
func TestAMemoryOutageDoesNotCostTheAnswer(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	hs := newFakeHindsight(t)
	hs.fail(http.StatusInternalServerError)
	answering(fake, "Bob owns the scheduler.")
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ev := slackish("C1/1.1", "who owns it?")
	startWith(t, st, fake, src, withMemory(hs.client(t)))
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to succeed anyway", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	waitFor(t, 30*time.Second, "the retain to have been attempted", func() bool {
		return len(hs.Auths()) > 0
	})

	records := src.Records()
	require.Len(t, posts(records, conductor.OutFinal), 1)
	assert.Equal(t, "Bob owns the scheduler.", posts(records, conductor.OutFinal)[0].Text)
	// A memory outage is not something a human hears about.
	assert.Empty(t, posts(records, conductor.OutFailure))
	assert.Equal(t, []string{
		string(conductor.ReactionWorking), string(conductor.ReactionDone),
	}, reactions(records))
	assert.Empty(t, hs.Retained())
}
