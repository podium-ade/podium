//go:build integration

package conductor_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// postgresImage is pgvector's build of Postgres 16 — where the whole repository is going in
// step 19, and already what internal/agent's own tests use.
const postgresImage = "pgvector/pgvector:pg16"

var (
	adminURL string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	// Before anything else, and before a container is started: with this set the process is
	// not a test run at all but the runtime of a host turn. See host_integration_test.go.
	if os.Getenv(hostFakeEnv) != "" {
		hostFakeRuntime()
		return
	}

	ctx := context.Background()
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("podium_agent"),
		postgres.WithUsername("podium"),
		postgres.WithPassword("podium"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		os.Exit(1)
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(ctr)
		os.Exit(1)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
	}
	os.Exit(code)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_agent_conductor_%d", dbSeq.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name

	st, err := store.New(ctx, u.String())
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.NoError(t, st.Migrate(ctx))
	return st
}

// testProfile has the shape examples/agent has, built in memory so these tests do not
// depend on the example's contents.
func testProfile(t *testing.T) *profiles.Profile {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/profile.yaml", []byte(`name: podium
display_name: Podium
system_prompt: be Podium
model: claude-opus-5
default_playbook: general
`), 0o600))
	require.NoError(t, os.MkdirAll(dir+"/playbooks", 0o750))
	require.NoError(t, os.WriteFile(dir+"/playbooks/general.yaml", []byte(`image: podium-agent-runtime:dev
system_prompt: answer the question
allowed_tools: [read, grep]
timeout: 15m
`), 0o600))
	require.NoError(t, os.WriteFile(dir+"/playbooks/coder.yaml", []byte(`image: podium-agent-runtime:dev
system_prompt: write the code
allowed_tools: [read, edit, bash]
`), 0o600))
	require.NoError(t, os.WriteFile(dir+"/playbooks/dogfood.yaml", []byte(`image: podium-agent-runtime-dev:dev
system_prompt: build podium
allowed_tools: [read, edit, bash]
docker: true
`), 0o600))
	require.NoError(t, os.WriteFile(dir+"/playbooks/looker.yaml", []byte(`image: podium-agent-runtime-dev:dev
system_prompt: look at the page
allowed_tools: [read, bash]
browser: true
`), 0o600))

	p, err := profiles.Load(dir)
	require.NoError(t, err)
	return p
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// running is one conductor started on a store, a fake control plane and a source.
type running struct {
	cond   *conductor.Conductor
	cancel context.CancelFunc
	done   chan struct{}
}

func start(t *testing.T, st *store.Store, fake *fakePodium, src *fakesource.Source) *running {
	return startWith(t, st, fake, src, nil)
}

// startWith is start with a hook on the options, which is how the memory tests attach a
// brief memory block and a recording client (see retain_integration_test.go).
func startWith(
	t *testing.T, st *store.Store, fake *fakePodium, src *fakesource.Source,
	tweak func(*conductor.Options),
) *running {
	t.Helper()
	opts := conductor.Options{
		Store:    st,
		Podium:   podium.New(fake.URL(), "devtoken"),
		Profiles: profiles.NewLive(testProfile(t)),
		Sources:  []conductor.Source{src},
		Logger:   quietLogger(),
	}
	if tweak != nil {
		tweak(&opts)
	}
	cond, err := conductor.New(opts)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cond: cond, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		requireNoError(t, cond.Run(ctx))
	}()
	t.Cleanup(r.stop)
	return r
}

// stop is the closest a test gets to killing the process: the context is cancelled and the
// turn loop unwinds without finishing whatever was in flight.
func (r *running) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(30 * time.Second):
	}
}

func inbound(ref, text string) conductor.InboundEvent {
	channel, thread, _ := strings.Cut(ref, "/")
	return conductor.InboundEvent{
		SourceKind: conductor.KindDev,
		SourceKey:  conductor.KindDev + ":" + channel + ":" + thread,
		Ref:        ref,
		Channel:    channel,
		Author:     "alice",
		Text:       text,
		TS:         time.Now().UTC(),
		BriefKind:  conductor.SourceChat,
		Env:        map[string]string{"PODIUM_AGENT_DRY_RUN": "1"},
	}
}

// threadInbound is inbound() for a source that is a THREAD rather than a chat window: a
// Slack mention. The difference is not cosmetic any more — a conversation may change its
// playbook per message and a thread may not (Conductor.aConversation) — so a test about
// thread behaviour has to use a thread's source kind.
func threadInbound(ref, text string) conductor.InboundEvent {
	ev := inbound(ref, text)
	channel, thread, _ := strings.Cut(ref, "/")
	ev.SourceKind = conductor.SourceSlack
	ev.SourceKey = conductor.SourceSlack + ":" + channel + ":" + thread
	ev.BriefKind = conductor.SourceSlack
	// Only the dev source may ask for task environment, and this one is not it.
	ev.Env = nil
	return ev
}

// decodeBrief reads back what the conductor put on a task spec, which is the only way to
// see the brief a turn actually ran with.
func decodeBrief(t *testing.T, s *podiumv1.TaskSpec) conductor.Brief {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s.GetEnv()[conductor.BriefEnv])
	require.NoError(t, err, "the spec must carry a base64 brief")
	var b conductor.Brief
	require.NoError(t, json.Unmarshal(raw, &b))
	return b
}

func posts(records []fakesource.Record, kind string) []fakesource.Record {
	var out []fakesource.Record
	for _, r := range records {
		if r.Action == fakesource.ActionPost && r.Type == kind {
			out = append(out, r)
		}
	}
	return out
}

func reactions(records []fakesource.Record) []string {
	var out []string
	for _, r := range records {
		if r.Action == fakesource.ActionReact {
			out = append(out, r.Reaction)
		}
	}
	return out
}

// turnStatus is turnOf for a polling condition: an absent session or turn is "not yet",
// not a failure.
func turnStatus(st *store.Store, sourceKey string) string {
	ctx := context.Background()
	sess, err := st.GetSessionByKey(ctx, sourceKey)
	if err != nil {
		return ""
	}
	turns, err := st.ListTurns(ctx, sess.ID, 1)
	if err != nil || len(turns) == 0 {
		return ""
	}
	return turns[0].Status
}

func turnOf(t *testing.T, st *store.Store, sourceKey string) store.Turn {
	t.Helper()
	ctx := context.Background()
	sess, err := st.GetSessionByKey(ctx, sourceKey)
	require.NoError(t, err)
	turns, err := st.ListTurns(ctx, sess.ID, 10)
	require.NoError(t, err)
	require.NotEmpty(t, turns)
	return turns[0]
}

// TestATurnRelaysEverythingAndRecordsIt is the whole loop against a fake control plane: the
// reaction, the placeholder, a progress edit, the answer, the attachment, the ✅, the turn
// row and the ledger.
func TestATurnRelaysEverythingAndRecordsIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			// A log chunk the conductor must ignore: it is not a log consumer.
			logEvent(taskID, 1, "podium-agent: turn starting\n"),
			messageEvent(taskID, 2, conductor.OutProgress, "reading the handler"),
			messageEvent(taskID, 3, conductor.OutFinal, "it dereferences the first label. See out.png.", "out.png"),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", "why does it 500?")
	release := fake.HoldTasks()
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })
	taskID := fake.TaskIDs()[0]
	fake.AddArtifact(taskID, "out.png", "image/png", "PNGDATA")
	fake.AddArtifact(taskID, "turn.json", "application/json",
		`{"session_id":"sess_x","num_turns":4,"total_cost_usd":0.0123,"exit_code":0}`)
	release()

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	records := src.Records()

	// The reactions are 👀 then ✅, on the triggering message, and nothing else.
	assert.Equal(t, []string{string(conductor.ReactionWorking), string(conductor.ReactionDone)}, reactions(records))

	// The placeholder goes up before anything else — it is the acknowledgement a human is
	// waiting for, and a source's write path is rate limited, so every call ahead of it is
	// a second of silence. The reaction follows, and the progress replaces the placeholder
	// by an edit rather than a second message.
	require.NotEmpty(t, records)
	assert.Equal(t, fakesource.ActionPost, records[0].Action, "the placeholder comes first")
	assert.Equal(t, conductor.Placeholder, records[0].Text)
	assert.Equal(t, fakesource.ActionReact, records[1].Action)

	var edits []fakesource.Record
	for _, rec := range records {
		if rec.Action == fakesource.ActionEdit {
			edits = append(edits, rec)
		}
	}
	require.Len(t, edits, 1, "one progress message is one edit")
	assert.Equal(t, records[0].MessageID, edits[0].MessageID, "the edit addresses the placeholder")
	assert.Equal(t, "⏳ reading the handler", edits[0].Text)

	// The answer is posted verbatim.
	finals := posts(records, conductor.OutFinal)
	require.Len(t, finals, 1)
	assert.Equal(t, "it dereferences the first label. See out.png.", finals[0].Text)

	// And the attachment it named was resolved against the task's artifacts and uploaded.
	var attached []fakesource.Record
	for _, rec := range records {
		if rec.Action == fakesource.ActionAttach {
			attached = append(attached, rec)
		}
	}
	require.Len(t, attached, 1)
	assert.Equal(t, "out.png", attached[0].Name)
	assert.Equal(t, int64(len("PNGDATA")), attached[0].Size)
	assert.Empty(t, posts(records, conductor.OutFailure), "a turn that worked apologises for nothing")

	// The turn row carries the runtime's own accounting, read out of turn.json.
	turn := turnOf(t, st, ev.SourceKey)
	assert.Equal(t, taskID, turn.TaskID)
	assert.Equal(t, ev.Ref, turn.TriggerRef)
	require.NotNil(t, turn.NumTurns)
	assert.Equal(t, 4, *turn.NumTurns)
	require.NotNil(t, turn.CostUSD)
	assert.InDelta(t, 0.0123, *turn.CostUSD, 1e-9)
	assert.Equal(t, "it dereferences the first label. See out.png.", turn.FinalText)

	// And what it ran on, recorded when it started rather than read back off the playbook —
	// which a per-turn override or a later edit could both have moved since.
	assert.NotEmpty(t, turn.Backend.Agent, "the turn records the backend it resolved to")
	assert.NotEmpty(t, turn.Backend.Model)
	assert.NotEmpty(t, turn.Backend.Provider, "and the provider that gets billed for it")

	// Two messages were said, so two seqs are claimed. The log chunk is not one of them.
	relayed, err := st.CountRelayed(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, 2, relayed, "only message events are relayed")
}

// TestATurnLinksThePullRequestsItsAnswerNamed is the automatic half of a chat's pull
// requests, driven through the whole turn loop.
//
// The turn says four things. A progress line names a pull request, and the answer names
// one twice — bare and as a markdown link — plus an issue in the same repository. Exactly
// one link comes out of that, and it is the one the ANSWER named: progress is coalesced,
// superseded and never stored, so reading it would make a link appear or not depending on
// how fast the runtime was talking.
func TestATurnLinksThePullRequestsItsAnswerNamed(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutProgress,
				"looking at https://github.com/acme/api/pull/1 for the pattern"),
			messageEvent(taskID, 2, conductor.OutFinal,
				"Opened https://github.com/acme/api/pull/41 with the fix."),
			messageEvent(taskID, 3, conductor.OutFinal,
				"Details in [#41](https://github.com/acme/api/pull/41); it closes "+
					"https://github.com/acme/api/issues/40."),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/2.1", "fix the nil dereference and open a PR")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	linked := src.PullRequests(ev.Ref)
	require.Len(t, linked, 1, "one pull request, named twice, in one answer")
	assert.Equal(t, conductor.PullRequest{
		URL: "https://github.com/acme/api/pull/41", Owner: "acme", Repo: "api", Number: 41,
	}, linked[0])

	// And what was linked is derivable from the row: final_text is the same joined answer
	// the scan read, so a reviewer can see where the link came from.
	turn := turnOf(t, st, ev.SourceKey)
	assert.Contains(t, turn.FinalText, "https://github.com/acme/api/pull/41")
	assert.NotContains(t, turn.FinalText, "https://github.com/acme/api/pull/1 ")
}

// TestATurnThatNamesNoPullRequestLinksNone is the ordinary turn: it answers a question, it
// opens nothing, and the conversation gains no links.
func TestATurnThatNamesNoPullRequestLinksNone(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutFinal,
				"It fails because the source table was empty. The repository is "+
					"https://github.com/acme/api and the run is at "+
					"https://github.com/acme/api/commit/aa519fc."),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/3.1", "why did the nightly ETL fail?")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	assert.Empty(t, src.PullRequests(ev.Ref),
		"a repository and a commit are not pull requests")
}

// TestATurnWithNoObjectStoreStillRecordsItsAccounting is the failing configuration: a host
// with PODIUM_S3_* unset, where the runtime's turn.json is written inside the container and
// then thrown away because artifacts are disabled. Nothing is registered as an artifact
// here, and the turn's accounting has to arrive anyway.
func TestATurnWithNoObjectStoreStillRecordsItsAccounting(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutProgress, "thinking"),
			messageEvent(taskID, 2, conductor.OutFinal, "pong"),
			accountingEvent(taskID, 3, 4, 0.0123),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	reg := prometheus.NewRegistry()
	ctx := context.Background()
	ev := inbound("C1/1.1", "ping")
	startWith(t, st, fake, src, func(o *conductor.Options) { o.Metrics = conductor.NewMetrics(reg) })
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	turn := turnOf(t, st, ev.SourceKey)
	require.NotNil(t, turn.NumTurns, "the accounting message is the route no configuration disables")
	assert.Equal(t, 4, *turn.NumTurns)
	require.NotNil(t, turn.CostUSD)
	assert.InDelta(t, 0.0123, *turn.CostUSD, 1e-9)
	assert.Equal(t, "pong", turn.FinalText)
	assert.Zero(t, counterValue(t, reg, turnsWithoutAccounting))

	// It is accounting, not conversation: nothing was said for it.
	records := src.Records()
	finals := posts(records, conductor.OutFinal)
	require.Len(t, finals, 1)
	assert.Equal(t, "pong", finals[0].Text)
	for _, rec := range records {
		assert.NotContains(t, rec.Text, "total_cost_usd", "the accounting is never said out loud")
	}

	// Its seq is deliberately left unclaimed, which is what lets a conductor resuming from
	// the last seq it relayed be sent the accounting again.
	relayed, err := st.CountRelayed(ctx, fake.TaskIDs()[0])
	require.NoError(t, err)
	assert.Equal(t, 2, relayed, "the accounting message claims no seq")
}

// TestATurnThatReportsNoAccountingSaysSo covers the remaining hole: neither route delivered.
// The columns are still null — there is nothing to put in them — but it is counted and named
// rather than passed over in silence.
func TestATurnThatReportsNoAccountingSaysSo(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "pong")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	reg := prometheus.NewRegistry()
	logs := &syncBuffer{}
	ev := inbound("C1/1.1", "ping")
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Metrics = conductor.NewMetrics(reg)
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	})
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	turn := turnOf(t, st, ev.SourceKey)
	assert.Nil(t, turn.NumTurns)
	assert.Nil(t, turn.CostUSD)
	assert.Equal(t, "pong", turn.FinalText, "the answer is recorded either way")

	assert.Equal(t, float64(1), counterValue(t, reg, turnsWithoutAccounting))
	assert.Contains(t, logs.String(), "a turn succeeded but reported no accounting")
	assert.Contains(t, logs.String(), turn.ID)
}

// TestTheAccountingMessageWinsOverTheArtifact pins the precedence: turn.json is the fallback,
// read only when the message did not arrive, so a host that does have an object store reads
// the same numbers over the cheaper route.
func TestTheAccountingMessageWinsOverTheArtifact(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutFinal, "pong"),
			accountingEvent(taskID, 2, 4, 0.0123),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ev := inbound("C1/1.1", "ping")
	release := fake.HoldTasks()
	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), ev))

	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })
	fake.AddArtifact(fake.TaskIDs()[0], "turn.json", "application/json",
		`{"session_id":"sess_x","num_turns":99,"total_cost_usd":9.99,"exit_code":0}`)
	release()

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	turn := turnOf(t, st, ev.SourceKey)
	require.NotNil(t, turn.NumTurns)
	assert.Equal(t, 4, *turn.NumTurns, "the message is what was read, not the artifact")
	require.NotNil(t, turn.CostUSD)
	assert.InDelta(t, 0.0123, *turn.CostUSD, 1e-9)
}

const turnsWithoutAccounting = "podium_agent_turns_without_accounting_total"

// counterValue reads one counter off a registry. Gather's return type is inferred rather
// than named, which is what keeps prometheus/client_model out of go.mod for one assertion.
func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		require.Len(t, mf.GetMetric(), 1)
		return mf.GetMetric()[0].GetCounter().GetValue()
	}
	t.Fatalf("%s is not registered", name)
	return 0
}

// syncBuffer is a log sink a test can read while the conductor is still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The spec a turn runs is the playbook's, plus exactly one secret the playbook did not name.
func TestTheTaskSpecIsThePlaybookPlusTheReservedSecret(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "dry run: hello")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), inbound("C1/1.1", "hello")))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	got := fake.Specs()[0]
	assert.Equal(t, "podium-agent-runtime:dev", got.GetImage())
	assert.Empty(t, got.GetCommand(), "the image's own entrypoint runs the turn")
	assert.Equal(t, int32(1), got.GetMaxAttempts())
	assert.False(t, got.GetRetryOnNodeLoss(),
		"a turn may already have posted an answer; running it twice would say it twice")
	assert.Equal(t, 15*time.Minute, got.GetTimeout().AsDuration())

	require.Len(t, got.GetSecrets(), 1)
	assert.Equal(t, profiles.AnthropicKeySecret, got.GetSecrets()[0].GetName())
	assert.Equal(t, spec.SecretTargetEnv, got.GetSecrets()[0].GetTarget())
	assert.Equal(t, profiles.AnthropicKeyEnv, got.GetSecrets()[0].GetKey())

	// The dev source's dry-run knob reached the spec, and so did the brief.
	assert.Equal(t, "1", got.GetEnv()["PODIUM_AGENT_DRY_RUN"])
	brief := decodeBrief(t, got)
	assert.Equal(t, conductor.BriefVersion, brief.Version)
	assert.Equal(t, conductor.SourceChat, brief.Source.Kind)
	assert.Equal(t, "C1/1.1", brief.Source.Ref)
	assert.Equal(t, "podium", brief.Profile.Name)
	assert.Equal(t, "claude-opus-5", brief.Profile.Model)
	assert.Equal(t, "general", brief.Playbook.Name)
	assert.Equal(t, []string{"read", "grep"}, brief.Playbook.AllowedTools)
	assert.Equal(t, profiles.DefaultMaxTurns, brief.Playbook.MaxTurns)
	assert.Equal(t, "hello", brief.Instruction)
	assert.False(t, brief.TranscriptTruncated)
	assert.Nil(t, brief.Memory, "step 17 sends no memory block")
	assert.Empty(t, brief.Repos)
}

// A playbook with `docker: true` gets a whole daemon it never had to describe. The shape is
// the conductor's, so this asserts every field of it: a half-configured daemon fails in
// ways that read as the agent's fault.
func TestADockerPlaybookGetsADaemonBesideIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "built")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), inbound("C1/1.1", "/dogfood build it")))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	got := fake.Specs()[0]
	sidecars := got.GetSidecars()
	require.Len(t, sidecars, 1, "one daemon, attached by the flag alone")
	dind, ok := sidecars["dind"]
	require.True(t, ok, "the sidecar is keyed by the name DOCKER_HOST resolves")

	assert.True(t, dind.GetPrivileged(), "dockerd cannot make its own cgroups without it")
	assert.True(t, dind.GetShareWorkspace(),
		"a bind source under /workspace must resolve inside the daemon too")
	assert.Equal(t, "", dind.GetEnv()["DOCKER_TLS_CERTDIR"], "empty is what turns TLS off")
	assert.Equal(t, int32(2375), dind.GetReadiness().GetTcpPort(),
		"the turn must not start before the daemon listens")
	assert.Contains(t, dind.GetImage(), "@sha256:", "the daemon is pinned by digest")

	assert.Equal(t, "tcp://dind:2375", got.GetEnv()["DOCKER_HOST"],
		"the agent runs plain `docker` and it reaches the sidecar")
}

// A `browser: true` playbook gets a headless Chrome beside it, and the address of that
// Chrome in its brief. The two must arrive together: tools with no browser, or a browser no
// tool can reach, is worse than neither.
func TestABrowserPlaybookGetsAHeadlessChromeBesideIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "looked")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), inbound("C1/1.1", "/looker look at it")))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	got := fake.Specs()[0]
	sidecars := got.GetSidecars()
	require.Len(t, sidecars, 1, "a browser, attached by the flag alone")
	chrome, ok := sidecars["chrome"]
	require.True(t, ok, "the sidecar is keyed by the name the CDP URL resolves")

	assert.False(t, chrome.GetPrivileged(),
		"a browser is an ordinary container: unlike dockerd it asks nothing of the kernel")
	assert.False(t, chrome.GetShareWorkspace(),
		"the browser has no business reading the workspace it is looking at a server for")
	// A command probe and not tcp_port, which a live turn proved is not optional: a
	// tcp_port probe runs inside the container, needs nc or wget, and this image has
	// neither — the sidecar never came ready and the turn died at provisioning with the
	// browser working fine. 9223 rather than 9222 because socat binds 9222 before the
	// browser behind it exists.
	assert.Zero(t, chrome.GetReadiness().GetTcpPort(),
		"tcp_port cannot be probed in an image with no nc")
	assert.Equal(t, []string{"bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/9223"},
		chrome.GetReadiness().GetCommand(),
		"the turn must not start before Chrome itself is listening")
	assert.Contains(t, chrome.GetImage(), "@sha256:", "the browser is pinned by digest")
	assert.Equal(t, []string{"--disable-dev-shm-usage"}, chrome.GetCommand(),
		"the one flag the image does not set: headless-shell already listens, and saying so "+
			"again kills it with `Address already in use`")

	b := decodeBrief(t, got)
	require.NotNil(t, b.Browser, "the brief carries the address, or the tools have nothing to drive")
	assert.Equal(t, "http://chrome:9222", b.Browser.CDPURL)
}

// The flag is the whole switch: a playbook without it is unchanged, and pays nothing.
func TestAPlaybookWithoutTheDockerFlagGetsNoSidecar(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "done")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	require.NoError(t, src.Send(context.Background(), inbound("C1/1.1", "hello")))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	got := fake.Specs()[0]
	assert.Empty(t, got.GetSidecars())
	assert.NotContains(t, got.GetEnv(), "DOCKER_HOST")
}

// A real source may not ask for task environment: the dry-run knobs exist for the dev
// source and for nothing else.
func TestOnlyTheDevSourceMayAskForTaskEnvironment(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "done")}
	}
	// A source calling itself something else — as slack and, later, linear and chat do.
	src := fakesource.New("slack")
	t.Cleanup(src.Close)

	start(t, st, fake, src)
	ev := inbound("C1/1.1", "hello")
	ev.SourceKind = "slack"
	ev.Env = map[string]string{"PODIUM_AGENT_DRY_RUN": "1", "ANTHROPIC_API_KEY": "stolen"}
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	env := fake.Specs()[0].GetEnv()
	assert.NotContains(t, env, "PODIUM_AGENT_DRY_RUN")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY")
	assert.Contains(t, env, conductor.BriefEnv, "the brief is the only thing the conductor puts there")
}

// TestASecondMessageWaitsForTheRunningTurn is the one-turn-per-session rule: the second
// message does not start a second task while the first is in flight, and when it does start
// one the brief has both human messages in it.
func TestASecondMessageWaitsForTheRunningTurn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answer to "+taskID)}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "first question")))
	waitFor(t, 30*time.Second, "the first task", func() bool { return len(fake.Specs()) == 1 })

	// The second message arrives while the first turn is still running.
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "actually, also this")))
	waitFor(t, 30*time.Second, "the second message to be queued", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) >= 1
	})
	assert.Len(t, fake.Specs(), 1, "a second turn must not start while one is running")

	// Let the first turn end. The second starts by itself.
	release()
	waitFor(t, 60*time.Second, "the second task", func() bool { return len(fake.Specs()) == 2 })
	waitFor(t, 60*time.Second, "both turns to finish", func() bool {
		sess, err := st.GetSessionByKey(ctx, conductor.KindDev+":C1:1.1")
		if err != nil {
			return false
		}
		turns, err := st.ListTurns(ctx, sess.ID, 10)
		if err != nil || len(turns) != 2 {
			return false
		}
		return turns[0].Status == store.TurnSucceeded && turns[1].Status == store.TurnSucceeded
	})

	first := decodeBrief(t, fake.Specs()[0])
	second := decodeBrief(t, fake.Specs()[1])
	assert.Equal(t, "first question", first.Instruction)
	assert.Equal(t, "actually, also this", second.Instruction,
		"the second turn starts from the latest human message")

	var texts []string
	for _, e := range second.Transcript {
		texts = append(texts, e.Text)
	}
	assert.Contains(t, texts, "first question")
	assert.Contains(t, texts, "actually, also this",
		"nothing said during a running turn is lost: it is in the next turn's transcript")

	// Two tasks, and they ran one after the other rather than at once.
	require.Len(t, fake.Specs(), 2)
	sess, err := st.GetSessionByKey(ctx, conductor.KindDev+":C1:1.1")
	require.NoError(t, err)
	turns, err := st.ListTurns(ctx, sess.ID, 10)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.NotNil(t, turns[1].FinishedAt)
	assert.False(t, turns[1].FinishedAt.After(turns[0].StartedAt.Add(time.Second)),
		"the second turn started after the first one finished")
}

// TestARestartResumesTheTurnAndPostsTheFinalOnce kills the conductor mid-turn and brings it
// back. The task is still running, the answer was already relayed, and the relayed ledger is
// what stops it being said twice — the fake deliberately replays every event.
func TestARestartResumesTheTurnAndPostsTheFinalOnce(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.replayAll = true
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutProgress, "still going"),
			messageEvent(taskID, 2, conductor.OutFinal, "here is the answer"),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	first := start(t, st, fake, src)
	ev := inbound("C1/1.1", "why does it 500?")
	require.NoError(t, src.Send(ctx, ev))

	waitFor(t, 30*time.Second, "the answer to be relayed", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) == 1
	})
	taskID := fake.TaskIDs()[0]

	// Kill it. The task is still running, so the turn stays running in the database.
	first.stop()
	turn := turnOf(t, st, ev.SourceKey)
	require.Equal(t, store.TurnRunning, turn.Status, "the turn was in flight when the process died")

	high, err := st.MaxRelayedSeq(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), high)

	// Bring it back. Its recovery pass resumes the follow from the last relayed seq.
	start(t, st, fake, src)
	release()
	waitFor(t, 60*time.Second, "the resumed turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	records := src.Records()
	assert.Len(t, posts(records, conductor.OutFinal), 1,
		"the answer must be posted exactly once across the restart")
	assert.Len(t, fake.Specs(), 1, "a resumed turn must not start a second task")

	relayed, err := st.CountRelayed(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, 2, relayed, "the ledger holds one row per thing said, and no more")
	assert.Contains(t, reactions(records), string(conductor.ReactionDone),
		"the resumed turn still shows its outcome")
}

// Two conductors following one task is what a botched rolling restart looks like. Both
// receive every event — the fake replays from the beginning — and the relayed ledger is the
// only thing that stops the human hearing the answer twice.
func TestTwoConductorsSayTheAnswerOnce(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.replayAll = true
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "one answer only")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	start(t, st, fake, src)
	ev := inbound("C1/1.1", "hello")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "a task and a turn", func() bool {
		if len(fake.Specs()) != 1 {
			return false
		}
		return turnOf(t, st, ev.SourceKey).TaskID != ""
	})

	// A second conductor recovers the same running turn and follows the same task.
	start(t, st, fake, src)
	release()
	waitFor(t, 60*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	// Give the loser a moment to try and fail to post.
	time.Sleep(500 * time.Millisecond)

	assert.Len(t, posts(src.Records(), conductor.OutFinal), 1, "exactly one conductor may say the answer")
	n, err := st.CountRelayed(ctx, fake.TaskIDs()[0])
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// A failed turn says something a human can act on and nothing a human cannot. The exit code
// is the runtime's "I ran out of turns"; the raw failure_reason goes to the log.
func TestAFailedTurnPostsPlainWordsAndNoRawError(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.terminal = podiumv1.TaskStatus_TASK_STATUS_FAILED
	fake.exitCode = exitCodeOf(3)
	fake.reason = "container exited 3 at /opt/podium-agent/dist/main.js:281"
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "I got most of the way")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", "do the big thing")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})

	records := src.Records()
	failures := posts(records, conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "ran out of turns")
	assert.Contains(t, failures[0].Text, fake.TaskIDs()[0], "the task id is how an operator digs in")
	for _, rec := range records {
		assert.NotContains(t, rec.Text, "main.js", "no raw error text may reach a human")
		assert.NotContains(t, rec.Text, fake.reason)
	}
	// A turn that already said something still says it.
	assert.Len(t, posts(records, conductor.OutFinal), 1)
	assert.Contains(t, reactions(records), string(conductor.ReactionFailed))
}

// One THREAD, one playbook. A later /other in the same Slack thread is refused politely and
// starts nothing. A chat window is the opposite and has its own test below.
func TestASecondPlaybookInOneThreadIsRefused(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered")}
	}
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := threadInbound("C1/1.1", "what is this")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the first turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})
	before := len(src.Records())

	require.NoError(t, src.Send(ctx, threadInbound("C1/1.1", "/coder fix it")))
	waitFor(t, 30*time.Second, "the refusal", func() bool { return len(src.Records()) > before })
	time.Sleep(300 * time.Millisecond)

	assert.Len(t, fake.Specs(), 1, "a refused playbook change starts no task")
	failures := posts(src.RecordsSince(int64(before)), conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "general")
	assert.Contains(t, failures[0].Text, "coder")
	assert.Contains(t, failures[0].Text, "Start a new thread")

	sess, err := st.GetSessionByKey(ctx, ev.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "general", sess.Playbook, "the session keeps the playbook it started with")
}

// A typed /playbook reaches the brief and is stripped from what the model is told, and an
// unknown /word is left alone. This is a TASK's routing — a Slack thread or a Linear ticket.
// A conversation has none of it: the assistant answers it and picks the playbook for each
// piece of work it delegates.
func TestATypedPlaybookReachesTheBriefWithoutItsPrefix(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	start(t, st, fake, src)

	// Nothing named a playbook, so profile.default_playbook runs.
	plain := inbound("C1/1.1", "hello")
	require.NoError(t, src.Send(ctx, plain))
	waitFor(t, 30*time.Second, "the first task", func() bool { return len(fake.Specs()) == 1 })
	assert.Equal(t, "general", decodeBrief(t, fake.Specs()[0]).Playbook.Name)

	typed := inbound("C1/2.2", "/coder fix it")
	require.NoError(t, src.Send(ctx, typed))
	waitFor(t, 30*time.Second, "the second task", func() bool { return len(fake.Specs()) == 2 })
	brief := decodeBrief(t, fake.Specs()[1])
	assert.Equal(t, "coder", brief.Playbook.Name)
	assert.Equal(t, "fix it", brief.Instruction)
	sess, err := st.GetSessionByKey(ctx, typed.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "coder", sess.Playbook, "the session records the playbook that ran")

	// Somebody typing /shrug must not break the bot, and must not have their text eaten.
	unknown := inbound("C1/3.3", "/shrug hi")
	require.NoError(t, src.Send(ctx, unknown))
	waitFor(t, 30*time.Second, "the third task", func() bool { return len(fake.Specs()) == 3 })
	brief = decodeBrief(t, fake.Specs()[2])
	assert.Equal(t, "general", brief.Playbook.Name)
	assert.Equal(t, "/shrug hi", brief.Instruction, "an unmatched prefix is left in the text")
}

// An attachment name that matches nothing is normal — the runtime names files it mentioned —
// and the thread says so rather than staying silent.
func TestAnAttachmentThatMatchesNothingSaysSo(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutFinal, "see before.png and after.png", "before.png", "after.png"),
		}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", "screenshot it")
	release := fake.HoldTasks()
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })
	fake.AddArtifact(fake.TaskIDs()[0], "before.png", "image/png", "ONE")
	release()

	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	records := src.Records()
	var attached []string
	var notes []string
	for _, rec := range records {
		switch rec.Action {
		case fakesource.ActionAttach:
			attached = append(attached, rec.Name)
		case fakesource.ActionPost:
			if strings.Contains(rec.Text, "no artifact named") {
				notes = append(notes, rec.Text)
			}
		}
	}
	assert.Equal(t, []string{"before.png"}, attached)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "after.png")
}

// A brief that cannot be made to fit fails the turn with something a human can act on,
// rather than being sent for the runtime to refuse.
func TestAnImpossibleBriefFailsTheTurnWithoutATask(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", strings.Repeat("x", conductor.MaxBriefBytes))
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to fail", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnFailed
	})

	assert.Empty(t, fake.Specs(), "no task may be created for a brief that cannot be sent")
	failures := posts(src.Records(), conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "too large")
	assert.Contains(t, reactions(src.Records()), string(conductor.ReactionFailed))
}

// A turn recorded but never bound to a task is what a crash between CreateTurn and
// SetTurnTask leaves behind. It is finished failed rather than followed forever.
func TestARecoveredTurnWithNoTaskIsFailed(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	sess, err := st.UpsertSession(ctx, store.Session{
		SourceKind: conductor.KindDev, SourceKey: conductor.KindDev + ":C1:1.1", Profile: "podium", Playbook: "general",
	})
	require.NoError(t, err)
	orphan, err := st.CreateTurn(ctx, sess.ID, "C1/1.1", store.Backend{})
	require.NoError(t, err)

	start(t, st, fake, src)
	waitFor(t, 30*time.Second, "the orphan to be failed", func() bool {
		got, err := st.GetTurn(ctx, orphan.ID)
		return err == nil && got.Status == store.TurnFailed
	})

	assert.Empty(t, fake.Specs(), "nothing ran, so nothing is resumed")
	failures := posts(src.Records(), conductor.OutFailure)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Text, "restarted")
}

func exitCodeOf(v int32) *int32 { return &v }

// A conversation that lives somewhere else is MIRRORED into chats/chat_messages so it can be
// read in the Podium UI: who asked, what they asked, and what came back. The mirror is never
// read into a brief — Slack stays the one authority on what was said — so this is about what
// a reader sees afterwards and nothing else.
func TestAMirroredConversationIsReadableAsAChat(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutProgress, "reading the tree"),
			messageEvent(taskID, 2, conductor.OutFinal, "it is a task runner."),
		}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", "what does this repo do?")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	chat, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.NoError(t, err, "the conversation should have opened a chat to be read in")
	assert.Empty(t, chat.Login, "a mirrored conversation has no owning login")
	assert.Equal(t, conductor.KindDev, chat.Origin)
	assert.Equal(t, "alice", chat.StartedBy, "the person who asked first is the attribution")
	assert.Equal(t, "what does this repo do?", chat.Title)

	msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "the question and the answer; the placeholder and the progress are noise: %+v", msgs)

	assert.Equal(t, store.RoleUser, msgs[0].Role)
	assert.Equal(t, "alice", msgs[0].Author)
	assert.Equal(t, "what does this repo do?", msgs[0].Text)

	assert.Equal(t, store.RoleAssistant, msgs[1].Role)
	assert.Equal(t, "Podium", msgs[1].Author, "the bot's own display name, so a reader can tell it apart")
	assert.Equal(t, "it is a task runner.", msgs[1].Text)

	// And it is in the list, for any login, because nobody owns it.
	chats, _, err := st.ListChats(ctx, "whoever", 0, "")
	require.NoError(t, err)
	var found bool
	for _, c := range chats {
		if c.ID == chat.ID {
			found = true
			assert.Equal(t, "alice", c.StartedBy)
		}
	}
	assert.True(t, found, "a mirrored conversation belongs to the workspace, so every login lists it")

	people, err := st.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alice"}, people, "the humans, and not the bot")
}

// A second person in the thread is a second participant, and the copy says who said what.
func TestAMirroredConversationRemembersEveryoneInIt(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{
			messageEvent(taskID, 1, conductor.OutFinal, "answered."),
		}
	}
	src := fakesource.New(conductor.KindDev)
	src.Mirrors()
	t.Cleanup(src.Close)

	ctx := context.Background()
	first := inbound("C1/1.1", "what does this repo do?")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, first))
	waitFor(t, 30*time.Second, "the first turn", func() bool {
		return turnStatus(st, first.SourceKey) == store.TurnSucceeded
	})

	second := inbound("C1/1.1", "and the node?")
	second.Author = "bob"
	require.NoError(t, src.Send(ctx, second))
	waitFor(t, 30*time.Second, "the second answer", func() bool {
		chat, err := st.ChatBySourceKey(ctx, second.SourceKey)
		if err != nil {
			return false
		}
		msgs, err := st.ListChatMessages(ctx, chat.ID, 0)
		return err == nil && len(msgs) >= 4
	})

	chat, err := st.ChatBySourceKey(ctx, second.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, "alice", chat.StartedBy, "the FIRST asker, not the latest one")

	people, err := st.ChatParticipants(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, people)
}

// The web chat must not be mirrored: it already stores its own messages, and a second
// writer would double every one of them.
func TestAConversationPodiumOwnsIsNotMirrored(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "answered.")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	ctx := context.Background()
	ev := inbound("C1/1.1", "what does this repo do?")
	start(t, st, fake, src)
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, ev.SourceKey) == store.TurnSucceeded
	})

	_, err := st.ChatBySourceKey(ctx, ev.SourceKey)
	require.ErrorIs(t, err, store.ErrNotFound)
}
