// Package conductor is the turn loop: an inbound message becomes one Podium task running
// the agent runtime image, what the task says is relayed back to where the message came
// from, and the turn is recorded. Everything a task says is content — the conductor posts
// it and interprets none of it.
package conductor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/config"
	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Placeholder is the first thing a human sees, posted before any work starts. Progress
// edits replace it.
const Placeholder = "👀 working…"

// progressPrefix marks a progress edit as a work-in-progress line rather than the answer.
const progressPrefix = "⏳ "

// progressThrottle is the shortest gap between two edits of one turn's placeholder. The
// newest text wins; a held edit is flushed when the final arrives, before the final is
// posted, so nothing is ever lost — only superseded.
const progressThrottle = 2 * time.Second

// terminalStatusBudget is how long the conductor keeps asking for a task's terminal status
// after its event stream ended.
const terminalStatusBudget = 60 * time.Second

// turnSummaryArtifact is the runtime's own accounting file. It is read for num_turns and
// total_cost_usd only, and only when the accounting message did not arrive: the object
// store is optional and this file is not kept without one.
const turnSummaryArtifact = "turn.json"

// Options is what a Conductor needs. Everything is required except Memory, MemoryClient
// and Metrics.
type Options struct {
	Store  *store.Store
	Podium *podium.Client
	// Profiles is the profile in force. It is a live holder rather than a profile because
	// a skill created in the web UI has to reach the next turn without a restart: every
	// read below takes one snapshot and works from it.
	Profiles *profiles.Live
	Sources  []Source
	Metrics  *Metrics
	Logger   *slog.Logger
	// Memory is copied into every brief, and its presence is what makes the conductor add
	// the memory secret to every task spec. Nil means this host has no shared memory:
	// briefs carry no memory block and nothing is retained.
	Memory *BriefMemory
	// MemoryClient is what the end-of-turn retain writes through. Nil disables retaining
	// while leaving the briefs alone, which is what a test that only cares about the brief
	// wants; in production the two are set together.
	MemoryClient memory.Client
	// XAIBaseURL is where a Grok turn's container sends the agent SDK's requests. Empty
	// means config.DefaultXAIBaseURL, so a hand-built Conductor still produces a usable
	// brief.
	XAIBaseURL string
}

// Conductor owns the turn loop. One instance drains every source.
type Conductor struct {
	store    *store.Store
	podium   *podium.Client
	profiles *profiles.Live
	sources  []Source
	metrics  *Metrics
	logger   *slog.Logger
	memory   *BriefMemory
	// memories is the shared-memory client. See retain.go.
	memories memory.Client
	// xaiBaseURL is the endpoint a Grok turn's brief names.
	xaiBaseURL string

	mu       sync.Mutex
	sessions map[string]*sessionState
	wg       sync.WaitGroup
}

// sessionState is the in-memory half of a session: whether a turn is in flight and whether
// somebody spoke while it was. It is deliberately not persisted — after a restart there is
// no in-flight turn this process owns, and the recovery pass rebuilds what matters.
type sessionState struct {
	running bool
	pending bool
	// next is the latest thing said while a turn was running. Only the latest becomes the
	// next turn's instruction; the earlier ones are in the transcript.
	next InboundEvent
}

// New validates the options and returns a Conductor.
func New(opts Options) (*Conductor, error) {
	switch {
	case opts.Store == nil:
		return nil, errors.New("conductor: a store is required")
	case opts.Podium == nil:
		return nil, errors.New("conductor: a Podium API client is required")
	case opts.Profiles == nil || opts.Profiles.Current() == nil:
		return nil, errors.New("conductor: a profile is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = NewMetrics(nil)
	}
	return &Conductor{
		store:      opts.Store,
		podium:     opts.Podium,
		profiles:   opts.Profiles,
		sources:    opts.Sources,
		metrics:    metrics,
		logger:     logger,
		memory:     opts.Memory,
		memories:   opts.MemoryClient,
		xaiBaseURL: cmp.Or(opts.XAIBaseURL, config.DefaultXAIBaseURL),
		sessions:   map[string]*sessionState{},
	}, nil
}

// Run drains every source until ctx is cancelled, and resumes whatever was in flight when
// the process last died. It returns once every source channel is closed or ctx is done.
func (c *Conductor) Run(ctx context.Context) error {
	c.recover(ctx)

	for _, src := range c.sources {
		c.wg.Add(1)
		go func(src Source) {
			defer c.wg.Done()
			c.drain(ctx, src)
		}(src)
	}
	c.wg.Wait()
	return nil
}

// drain reads one source's events. Handling happens on its own goroutine so a long turn
// never stops the source from delivering the next message — which is what makes the
// "somebody spoke while a turn was running" path work at all.
func (c *Conductor) drain(ctx context.Context, src Source) {
	events := src.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			c.metrics.SourceEvents.WithLabelValues(src.Kind()).Inc()
			c.accept(ctx, src, ev)
		}
	}
}

// accept does the bookkeeping an event needs before any work starts: pick the skill, find
// or create the session, and either start a turn or remember the message for the turn that
// is already running.
func (c *Conductor) accept(ctx context.Context, src Source, ev InboundEvent) {
	// One snapshot for the whole of this event. A profile swapped in half way through must
	// not route the message against one set of skills and then start the turn against
	// another.
	profile := c.profiles.Current()
	sel := profile.Select(profiles.Routing{
		Skill:        ev.Skill,
		DefaultSkill: ev.DefaultSkill,
		Channel:      ev.Channel,
		Text:         ev.Text,
	})
	if sel.Skill.Name == "" {
		c.logger.ErrorContext(ctx, "no skill could be selected; the profile has no default",
			"source", src.Kind(), "source_key", ev.SourceKey)
		return
	}

	sess, err := c.store.UpsertSession(ctx, store.Session{
		SourceKind: src.Kind(),
		SourceKey:  ev.SourceKey,
		Profile:    profile.Name,
		Skill:      sel.Skill.Name,
	})
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the session failed", "source_key", ev.SourceKey, "error", err)
		return
	}

	// One session, one skill, fixed at creation. A later /other in the same thread is
	// refused rather than silently ignored: the human asked for something specific.
	if sel.Explicit && sess.Skill != sel.Skill.Name {
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread is running the `%s` skill and a thread keeps the skill it started with. "+
				"Start a new thread to use `%s`.", sess.Skill, sel.Skill.Name)})
		return
	}
	skill, ok := profile.Skills[sess.Skill]
	if !ok {
		c.logger.ErrorContext(ctx, "the session's skill is no longer loaded",
			"session_id", sess.ID, "skill", sess.Skill)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread ran the `%s` skill, which this bot no longer has. Start a new thread.", sess.Skill)})
		return
	}
	// Whatever the routing rules said, the instruction is the text minus a /skill prefix.
	ev.Text = sel.Instruction

	c.mu.Lock()
	st := c.sessions[sess.ID]
	if st == nil {
		st = &sessionState{}
		c.sessions[sess.ID] = st
	}
	if st.running {
		// Nothing is lost and nothing runs twice: the message is in the thread, so it will
		// be in the next turn's transcript, and the next turn starts from it.
		st.pending = true
		st.next = ev
		c.mu.Unlock()
		c.logger.InfoContext(ctx, "a turn is already running for this session; queued the message",
			"session_id", sess.ID)
		return
	}
	st.running = true
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.serve(ctx, src, sess, skill, ev)
	}()
}

// serve runs turns for one session until nothing is pending. It holds the session's
// "running" flag for its whole life, which is what serialises turns within a session while
// leaving different sessions free to run at once.
func (c *Conductor) serve(ctx context.Context, src Source, sess store.Session, skill profiles.Skill, ev InboundEvent) {
	for {
		c.runTurn(ctx, src, sess, skill, ev)

		c.mu.Lock()
		st := c.sessions[sess.ID]
		if st == nil || !st.pending || ctx.Err() != nil {
			if st != nil {
				st.running = false
			}
			c.mu.Unlock()
			return
		}
		st.pending = false
		ev = st.next
		c.mu.Unlock()
	}
}

// runTurn is one inbound message, end to end.
func (c *Conductor) runTurn(ctx context.Context, src Source, sess store.Session, skill profiles.Skill, ev InboundEvent) {
	started := time.Now()

	if err := src.React(ctx, ev.Ref, ReactionWorking); err != nil {
		c.logger.WarnContext(ctx, "reacting to the triggering message failed", "ref", ev.Ref, "error", err)
	}
	placeholder := c.post(ctx, src, ev.Ref, Outbound{Type: OutProgress, Text: Placeholder})

	entries, err := src.FetchTranscript(ctx, ev.Ref)
	if err != nil {
		// A turn with no history is a worse answer than one with it, and a much better
		// answer than none at all.
		c.logger.WarnContext(ctx, "reading the conversation failed; running the turn without history",
			"ref", ev.Ref, "error", err)
	}

	turn, err := c.store.CreateTurn(ctx, sess.ID, ev.Ref)
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the turn failed", "session_id", sess.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "Something went wrong on my side before I could start. Nothing ran."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return
	}

	brief := c.brief(sess, skill, turn.ID, ev, entries)
	encoded, err := brief.Encode()
	if err != nil {
		c.logger.WarnContext(ctx, "the turn brief does not fit", "turn_id", turn.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "This conversation is too large for me to take in at once. Start a new thread with just the question."})
		c.failTurn(ctx, src, sess, skill, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	taskSpec := c.taskSpec(src, skill, encoded, ev)
	task, err := c.podium.CreateTask(ctx, taskSpec)
	if err != nil {
		// Validation, a missing secret, a control plane that is down: all of them are
		// "I could not start", and none of the reason is a human's business.
		c.logger.WarnContext(ctx, "creating the turn's task failed",
			"turn_id", turn.ID, "image", skill.Image, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "Something went wrong on my side and the work never started. An operator should check the logs."})
		c.failTurn(ctx, src, sess, skill, turn, ev.Ref, started, store.TurnFailed)
		return
	}
	if err := c.store.SetTurnTask(ctx, turn.ID, task.GetId()); err != nil {
		c.logger.ErrorContext(ctx, "binding the turn to its task failed",
			"turn_id", turn.ID, "task_id", task.GetId(), "error", err)
	}
	turn.TaskID = task.GetId()
	c.logger.InfoContext(ctx, "turn started", "turn_id", turn.ID, "session_id", sess.ID,
		"skill", skill.Name, "task_id", task.GetId(), "source", src.Kind())

	(&turnRun{
		c:           c,
		src:         src,
		sess:        sess,
		skill:       skill,
		turn:        turn,
		ref:         ev.Ref,
		author:      ev.Author,
		instruction: ev.Text,
		url:         ev.URL,
		placeholder: placeholder,
		startedAt:   started,
	}).run(ctx)
}

// brief builds the turn brief. It never sets a field the runtime's schema does not have:
// the schema is strict at every level and an unknown key is a failed turn.
func (c *Conductor) brief(
	sess store.Session, skill profiles.Skill, turnID string, ev InboundEvent, entries []BriefEntry,
) *Brief {
	kind := ev.BriefKind
	if kind == "" {
		kind = ev.SourceKind
	}
	profile := c.profiles.Current()
	// One resolution for the whole brief: the override, then the skill, then the profile.
	// taskSpec resolves the same way for the credential, so the two cannot disagree about
	// which backend this turn is running on.
	choice := profile.Resolve(skill, ev.Override)
	b := &Brief{
		Version:   BriefVersion,
		SessionID: sess.ID,
		TurnID:    turnID,
		Source:    BriefSource{Kind: kind, Ref: ev.Ref, URL: ev.URL},
		Profile: BriefProfile{
			Name:         profile.Name,
			DisplayName:  profile.DisplayName,
			SystemPrompt: profile.SystemPrompt,
			Model:        choice.Model,
			Effort:       choice.Effort,
		},
		Skill: BriefSkill{
			Name:         skill.Name,
			SystemPrompt: skill.SystemPrompt,
			AllowedTools: append([]string{}, skill.AllowedTools...),
			MaxTurns:     skill.MaxTurns,
		},
		Transcript:  entries,
		Instruction: ev.Text,
		Memory:      c.memory,
	}
	b.Provider = c.providerFor(choice.Agent)
	for _, r := range skill.Repos {
		b.Repos = append(b.Repos, BriefRepo{Name: r.Name, URL: r.URL, DefaultBranch: r.DefaultBranch})
	}
	if b.Instruction == "" {
		// The schema requires a non-empty instruction, and a mention with no words is a
		// real thing a human does.
		b.Instruction = "(no message text)"
	}
	return b
}

// providerFor is which model API a backend's turns go to, and which environment variable
// holds the credential. Every turn has one — the harness is told `provider/model` and
// cannot be run without it.
//
// The base URL is only set where an install can point it somewhere else; leaving it empty
// means the harness's own default for that provider, which is the right answer for a
// provider Podium has no endpoint opinion about.
func (c *Conductor) providerFor(agent string) *BriefProvider {
	b, ok := profiles.FindBackend(agent)
	if !ok {
		b, _ = profiles.FindBackend(profiles.DefaultAgent)
	}
	out := &BriefProvider{ID: b.Provider, APIKeyEnv: profiles.KeyEnvFor(b.Provider)}
	if b.Provider == profiles.ProviderXAI {
		out.BaseURL = c.xaiBaseURL
	}
	return out
}

// taskSpec is the task one turn runs. The secrets are exactly the skill's, plus the
// reserved credential the conductor always adds: a skill only ever gets the credentials its
// own file names, and the one the backend it runs on needs.
func (c *Conductor) taskSpec(src Source, skill profiles.Skill, encodedBrief string, ev InboundEvent) *spec.TaskSpec {
	env := map[string]string{}
	for k, v := range skill.Env {
		env[k] = v
	}
	// TEST ONLY, and only for the dev source: the three dry-run knobs step 16 defined.
	// A real source can ask for nothing, which is why this is gated on the kind rather
	// than on the field being empty.
	if src.Kind() == KindDev {
		for k, v := range ev.Env {
			env[k] = v
		}
	}
	env[BriefEnv] = encodedBrief

	if skill.Docker {
		env[profiles.DockerHostEnv] = dockerSidecarHost
	}

	s := &spec.TaskSpec{
		Image:     skill.Image,
		Env:       env,
		Labels:    append([]string(nil), skill.Labels...),
		Resources: skill.Resources,
		Timeout:   skill.Timeout,
		Secrets: append(append([]spec.SecretRef(nil), skill.Secrets...),
			c.reservedSecrets(c.profiles.Current().Resolve(skill, ev.Override).Agent)...),
		MaxAttempts: 1,
		// A turn is not idempotent: it may already have posted a final. Running it twice
		// would say the same thing twice, so a lost node is surfaced to the human instead.
		RetryOnNodeLoss: false,
	}
	if skill.Docker {
		s.Sidecars = map[string]spec.Sidecar{dockerSidecarName: dockerSidecar()}
	}
	s.ApplyDefaults()
	return s
}

// The Docker daemon a `docker: true` skill gets. It is the conductor's to build rather
// than the skill file's: a half-configured daemon — TLS still on, no workspace, no probe —
// fails in ways that read as the agent's fault, and there is exactly one shape that works.
const (
	// dockerSidecarName is also the DNS alias the daemon answers to on the task's own
	// private network, which is why the host below can be a constant.
	dockerSidecarName = "dind"

	// dockerSidecarHost is plaintext on purpose. The task network is per-task and carries
	// only this task and its sidecars, so TLS would protect the daemon from the one
	// container that is already entitled to drive it.
	dockerSidecarHost = "tcp://" + dockerSidecarName + ":2375"

	// Pinned by digest like every other base image Podium runs, and to the same 28.x the
	// -dev runtime image's CLI is built against. The digest is the multi-arch index, so it
	// resolves on amd64 and arm64 alike.
	dockerSidecarImage = "docker:28-dind@sha256:" +
		"2a232a42256f70d78e3cc5d2b5d6b3276710a0de0596c145f627ecfae90282ac"

	// dockerSidecarReady is generous because dockerd is slow to listen: ~17s on an
	// unloaded arm64 laptop, and a loaded node is slower than that by more than the
	// margin. A turn that waits two minutes for its daemon is still a turn; one that
	// starts without it fails on the agent's first docker command.
	dockerSidecarReady = 2 * time.Minute
)

func dockerSidecar() spec.Sidecar {
	return spec.Sidecar{
		Image: dockerSidecarImage,
		// Empty turns TLS off, which is what makes the daemon answer on plain 2375.
		Env:            map[string]string{"DOCKER_TLS_CERTDIR": ""},
		Privileged:     true,
		ShareWorkspace: true,
		Readiness:      spec.Readiness{TCPPort: 2375, Timeout: spec.Duration(dockerSidecarReady)},
	}
}

// reservedSecrets are the credentials the conductor attaches itself, whatever the skill
// file says.
//
// Exactly one model credential goes on a turn, and it is the one the turn's backend spends.
// A Grok turn is not handed the Anthropic key and a Claude turn is not handed the xAI one:
// a container gets the credential it needs and no other, which is the same rule the rest of
// Podium's secret handling follows. The refresh token of a subscription sign-in is on
// neither — it never leaves the conductor's host at all.
//
// The memory key is on every turn of a host that has memory, because a brief with a memory
// block whose api_key_env is unset is a failed turn (exit 2) — so that injection is not
// optional.
func (c *Conductor) reservedSecrets(agent string) []spec.SecretRef {
	model := spec.SecretRef{
		Name:   profiles.AnthropicKeySecret,
		Target: spec.SecretTargetEnv,
		Key:    profiles.AnthropicKeyEnv,
	}
	if agent == profiles.AgentGrok {
		model = spec.SecretRef{
			Name:   profiles.XAIKeySecret,
			Target: spec.SecretTargetEnv,
			Key:    profiles.XAIKeyEnv,
		}
	}
	refs := []spec.SecretRef{model}
	if c.memory != nil {
		refs = append(refs, spec.SecretRef{
			Name:   profiles.MemoryKeySecret,
			Target: spec.SecretTargetEnv,
			Key:    profiles.MemoryKeyEnv,
		})
	}
	return refs
}

// failTurn records a turn that never got as far as a task, or one whose brief was
// impossible, and shows the failure on the triggering message.
func (c *Conductor) failTurn(
	ctx context.Context, src Source, sess store.Session, skill profiles.Skill,
	turn store.Turn, ref string, started time.Time, status string,
) {
	if err := c.store.FinishTurn(ctx, turn.ID, status, nil, nil, ""); err != nil {
		c.logger.ErrorContext(ctx, "finishing a failed turn failed", "turn_id", turn.ID, "error", err)
	}
	c.finish(ctx, src, ref, ReactionFailed)
	c.metrics.Turns.WithLabelValues(sess.SourceKind, skill.Name, status).Inc()
	c.metrics.TurnDuration.WithLabelValues(skill.Name).Observe(time.Since(started).Seconds())
}

// post says one thing and returns the message id, or "" when it could not be said. A
// source that cannot post is logged and the turn carries on: the work is more use to a
// human than the placeholder was.
func (c *Conductor) post(ctx context.Context, src Source, ref string, out Outbound) string {
	id, err := src.Post(ctx, ref, out)
	if err != nil {
		c.logger.WarnContext(ctx, "posting to the conversation failed",
			"ref", ref, "type", out.Type, "error", err)
		return ""
	}
	return id
}

// finish shows the turn's outcome on the triggering message.
func (c *Conductor) finish(ctx context.Context, src Source, ref string, kind Reaction) {
	if err := src.React(ctx, ref, kind); err != nil {
		c.logger.WarnContext(ctx, "reacting with the turn's outcome failed",
			"ref", ref, "reaction", kind, "error", err)
	}
}

// recover resumes what was in flight when this process last died. A running turn with a
// task is followed again from the highest seq that was already relayed, so the answer is
// posted exactly once even if the crash landed between the message arriving and it being
// said. A running turn with no task crashed between CreateTurn and SetTurnTask: nothing
// ran, and it is finished failed.
//
// The placeholder's message id is not persisted, so a resumed turn posts progress as new
// messages instead of editing. That is deliberate: an id that outlives the process is a
// thing to keep in sync, and progress is superseded by the final anyway.
func (c *Conductor) recover(ctx context.Context) {
	running, err := c.store.ListRunningTurns(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "reading the turns that were in flight failed", "error", err)
		return
	}
	for _, turn := range running {
		sess, err := c.store.GetSession(ctx, turn.SessionID)
		if err != nil {
			c.logger.ErrorContext(ctx, "a running turn has no session", "turn_id", turn.ID, "error", err)
			continue
		}
		src := c.sourceOf(sess.SourceKind)
		skill := c.profiles.Current().Skills[sess.Skill]

		if turn.TaskID == "" {
			c.logger.WarnContext(ctx, "a turn was recorded but its task never was; failing it",
				"turn_id", turn.ID)
			if err := c.store.FinishTurn(ctx, turn.ID, store.TurnFailed, nil, nil, ""); err != nil {
				c.logger.ErrorContext(ctx, "failing an orphaned turn failed", "turn_id", turn.ID, "error", err)
			}
			if src != nil {
				c.post(ctx, src, turn.TriggerRef, Outbound{Type: OutFailure, Text: "I was restarted before this got going and nothing ran. Ask me again."})
				c.finish(ctx, src, turn.TriggerRef, ReactionFailed)
			}
			continue
		}
		if src == nil {
			c.logger.WarnContext(ctx, "a running turn's source is not configured; leaving it alone",
				"turn_id", turn.ID, "source_kind", sess.SourceKind)
			continue
		}

		lastSeq, err := c.store.MaxRelayedSeq(ctx, turn.TaskID)
		if err != nil {
			c.logger.ErrorContext(ctx, "reading the relay high-water mark failed",
				"task_id", turn.TaskID, "error", err)
			continue
		}
		c.logger.InfoContext(ctx, "resuming a turn that was in flight",
			"turn_id", turn.ID, "task_id", turn.TaskID, "from_seq", lastSeq)

		c.mu.Lock()
		if c.sessions[sess.ID] == nil {
			c.sessions[sess.ID] = &sessionState{}
		}
		c.sessions[sess.ID].running = true
		c.mu.Unlock()

		run := &turnRun{
			c:         c,
			src:       src,
			sess:      sess,
			skill:     skill,
			turn:      turn,
			ref:       turn.TriggerRef,
			startedAt: turn.StartedAt,
			lastSeq:   lastSeq,
		}
		c.wg.Add(1)
		go func(run *turnRun, sessID string) {
			defer c.wg.Done()
			run.run(ctx)
			c.mu.Lock()
			if st := c.sessions[sessID]; st != nil {
				st.running = false
			}
			c.mu.Unlock()
		}(run, sess.ID)
	}
}

// sourceOf finds the source a session belongs to, or nil when it is not configured in this
// process (Slack tokens removed, say).
func (c *Conductor) sourceOf(kind string) Source {
	for _, src := range c.sources {
		if src.Kind() == kind {
			return src
		}
	}
	return nil
}
