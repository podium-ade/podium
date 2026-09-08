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
	"strings"
	"sync"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/config"
	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/skills"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Placeholder is the first thing a human sees, posted before any work starts. Progress
// edits replace it.
const Placeholder = "👀 working…"

// ProgressPrefix marks a progress edit as a work-in-progress line rather than the answer.
// A source that can style the difference itself strips it.
const ProgressPrefix = "⏳ "

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

// chatTitleArtifact is the file a first web-chat turn writes so the conversation can be
// named from the model. It matches chat.ChatTitleArtifact; this package cannot import chat
// (chat already imports conductor).
const chatTitleArtifact = "chat-title.txt"

// autoTitler is the optional half of Source the web chat implements: a turn can name the
// conversation. Slack and Linear have their own titles and ignore this.
type autoTitler interface {
	SetAutoTitle(ctx context.Context, ref, title string) error
}

// pullRequestLinker is the other optional half the web chat implements: a conversation can
// carry the pull requests its turns produced. Slack and Linear do not — GitHub already
// unfurls a link in a Slack thread and attaches one to a Linear issue, and neither has a
// list of them belonging to the conversation for Podium to keep.
type pullRequestLinker interface {
	LinkPullRequests(ctx context.Context, ref string, prs []PullRequest) error
}

// maxPullRequestsPerTurn bounds what one turn may link. A turn that opens a pull request
// names one; a turn asked to summarise every open pull request in a repository names
// dozens, and nothing in the text tells the two apart. The cap is where a list stops being
// "the work this chat produced", and a human can still attach what it left out.
const maxPullRequestsPerTurn = 20

// Options is what a Conductor needs. Everything is required except Memory, MemoryClient
// and Metrics.
type Options struct {
	Store  *store.Store
	Podium *podium.Client
	// Profiles is the profile in force. It is a live holder rather than a profile because
	// a playbook created in the web UI has to reach the next turn without a restart: every
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
	// SkillsDir is the directory on this host that Agent Skills are read from
	// (PODIUM_AGENT_SKILLS_DIR). Empty means this conductor delivers none, and a playbook
	// that names one fails its turns saying so.
	SkillsDir string
	// Host is the runtime this process runs a turn on itself. Nil means every turn is a
	// task, which is what a conductor whose host has no runtime must do; set, it is what
	// answers a conversation, and a container is what it delegates to. See host.go.
	Host *HostRuntime
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
	// skillsDir is where a turn's Agent Skills are read from.
	skillsDir string
	// host is the runtime for a turn this process runs itself, nil when it runs none.
	host *HostRuntime

	mu       sync.Mutex
	sessions map[string]*sessionState
	// hostRuns are the host turns in flight, by the ref of the message that started each,
	// so one can be cancelled without stopping the conductor.
	hostRuns map[string]func()
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
	if opts.Host != nil {
		if err := opts.Host.validate(); err != nil {
			return nil, err
		}
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
		skillsDir:  opts.SkillsDir,
		host:       opts.Host,
		sessions:   map[string]*sessionState{},
		hostRuns:   map[string]func(){},
	}, nil
}

// Run drains every source until ctx is cancelled, and resumes whatever was in flight when
// the process last died. It returns once every source channel is closed or ctx is done.
func (c *Conductor) Run(ctx context.Context) error {
	c.recover(ctx)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.watchExtractions(ctx)
	}()

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

// accept does the bookkeeping an event needs before any work starts: pick the playbook, find
// or create the session, and either start a turn or remember the message for the turn that
// is already running.
func (c *Conductor) accept(ctx context.Context, src Source, ev InboundEvent) {
	// One snapshot for the whole of this event. A profile swapped in half way through must
	// not route the message against one set of playbooks and then start the turn against
	// another.
	profile := c.profiles.Current()
	sel := profile.Select(profiles.Routing{
		Playbook:        ev.Playbook,
		DefaultPlaybook: ev.DefaultPlaybook,
		Channel:         ev.Channel,
		Text:            ev.Text,
	})
	if sel.Playbook.Name == "" {
		c.logger.ErrorContext(ctx, "no playbook could be selected; the profile has no default",
			"source", src.Kind(), "source_key", ev.SourceKey)
		return
	}

	sess, err := c.store.UpsertSession(ctx, store.Session{
		SourceKind: src.Kind(),
		SourceKey:  ev.SourceKey,
		Profile:    profile.Name,
		Playbook:   sel.Playbook.Name,
	})
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the session failed", "source_key", ev.SourceKey, "error", err)
		return
	}

	// One session, one playbook, fixed at creation. A later /other in the same thread is
	// refused rather than silently ignored: the human asked for something specific.
	if sel.Explicit && sess.Playbook != sel.Playbook.Name {
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread is running the `%s` playbook and a thread keeps the playbook it started with. "+
				"Start a new thread to use `%s`.", sess.Playbook, sel.Playbook.Name)})
		return
	}
	playbook, ok := profile.Playbooks[sess.Playbook]
	if !ok {
		c.logger.ErrorContext(ctx, "the session's playbook is no longer loaded",
			"session_id", sess.ID, "playbook", sess.Playbook)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread ran the `%s` playbook, which this bot no longer has. Start a new thread.", sess.Playbook)})
		return
	}
	// Whatever the routing rules said, the instruction is the text minus a /playbook prefix.
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
		c.serve(ctx, src, sess, playbook, ev)
	}()
}

// serve runs turns for one session until nothing is pending. It holds the session's
// "running" flag for its whole life, which is what serialises turns within a session while
// leaving different sessions free to run at once.
func (c *Conductor) serve(ctx context.Context, src Source, sess store.Session, playbook profiles.Playbook, ev InboundEvent) {
	for {
		c.runTurn(ctx, src, sess, playbook, ev)

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
func (c *Conductor) runTurn(ctx context.Context, src Source, sess store.Session, playbook profiles.Playbook, ev InboundEvent) {
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

	// Resolved before the turn is recorded so the row says what actually ran, and handed to
	// the brief and the task spec so all three agree on one answer.
	choice := c.profiles.Current().Resolve(playbook, ev.Override)
	backend := store.Backend{
		Agent:    choice.Agent,
		Model:    choice.Model,
		Effort:   choice.Effort,
		Provider: c.providerFor(choice.Agent).ID,
	}

	turn, err := c.store.CreateTurn(ctx, sess.ID, ev.Ref, backend)
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the turn failed", "session_id", sess.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "Something went wrong on my side before I could start. Nothing ran."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return
	}

	// The playbook's Agent Skills, read off this host and packed. It happens per turn and
	// not at start-up so that a skill an operator has just edited is the one the next turn
	// gets, and so that a broken skill fails the playbook that names it rather than the
	// whole conductor.
	bundles, err := c.skillBundles(ctx, playbook)
	if err != nil {
		// The reason names a path, a database row or a cap on the conductor's own side, so it
		// goes to the log and not to a human, like every other "I could not start".
		c.logger.ErrorContext(ctx, "the playbook's agent skills could not be prepared",
			"turn_id", turn.ID, "playbook", playbook.Name, "skills_dir", c.skillsDir,
			"skills", playbook.Skills, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"The `%s` playbook asks for skills I could not prepare, so nothing ran. "+
				"An operator should check the logs.", playbook.Name)})
		c.failTurn(ctx, src, sess, playbook, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	brief := c.brief(sess, playbook, turn.ID, ev, entries, bundles, choice)
	if c.host != nil {
		c.fenceForHost(brief)
	}
	encoded, err := brief.Encode()
	if err != nil {
		c.logger.WarnContext(ctx, "the turn brief does not fit", "turn_id", turn.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "This conversation is too large for me to take in at once. Start a new thread with just the question."})
		c.failTurn(ctx, src, sess, playbook, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	run := &turnRun{
		c:           c,
		src:         src,
		sess:        sess,
		playbook:    playbook,
		turn:        turn,
		ref:         ev.Ref,
		author:      ev.Author,
		instruction: ev.Text,
		url:         ev.URL,
		placeholder: placeholder,
		startedAt:   started,
	}

	// The conversation runs here; a container is what it delegates to. The turn row keeps
	// its task_id empty, which is what the recovery pass reads to mean "this one died with
	// the process that was running it".
	//
	// A CONVERSATION, and not every turn: the web chat is a person waiting on an answer, and
	// a container per message is what makes that slow. A Slack mention and a Linear
	// assignment keep running as tasks, because their playbooks are the ones that want a
	// repository and a Docker daemon, and neither surface is anybody watching a cursor.
	if c.host != nil && hostCapable(src.Kind()) {
		host := &hostRun{r: run, encoded: encoded, bundles: bundles, provider: brief.Provider}
		if brief.Memory != nil {
			host.memoryKeyEnv = brief.Memory.APIKeyEnv
		}
		if src.Kind() == KindDev {
			// The same TEST-ONLY escape taskSpec allows, and only for the same source: the
			// dry-run knobs step 16 defined are how a test drives a turn with no model.
			host.devEnv = ev.Env
		}
		host.run(ctx)
		return
	}

	taskSpec := c.taskSpec(src, playbook, encoded, ev, bundles, choice)
	task, err := c.podium.CreateTask(ctx, taskSpec)
	if err != nil {
		// Validation, a missing secret, a control plane that is down: all of them are
		// "I could not start", and none of the reason is a human's business.
		c.logger.WarnContext(ctx, "creating the turn's task failed",
			"turn_id", turn.ID, "image", playbook.Image, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "Something went wrong on my side and the work never started. An operator should check the logs."})
		c.failTurn(ctx, src, sess, playbook, turn, ev.Ref, started, store.TurnFailed)
		return
	}
	if err := c.store.SetTurnTask(ctx, turn.ID, task.GetId()); err != nil {
		c.logger.ErrorContext(ctx, "binding the turn to its task failed",
			"turn_id", turn.ID, "task_id", task.GetId(), "error", err)
	}
	turn.TaskID = task.GetId()
	run.turn = turn
	c.logger.InfoContext(ctx, "turn started", "turn_id", turn.ID, "session_id", sess.ID,
		"playbook", playbook.Name, "task_id", task.GetId(), "source", src.Kind())

	run.run(ctx)
}

// hostCapable is which sources a host turn may answer. The dev source is in it because it
// is how the host path is tested (host_integration_test.go).
func hostCapable(kind string) bool {
	return kind == SourceChat || kind == KindDev
}

// brief builds the turn brief. It never sets a field the runtime's schema does not have:
// the schema is strict at every level and an unknown key is a failed turn.
func (c *Conductor) brief(
	sess store.Session, playbook profiles.Playbook, turnID string, ev InboundEvent,
	entries []BriefEntry, bundles []skills.Bundle, choice profiles.Choice,
) *Brief {
	kind := ev.BriefKind
	if kind == "" {
		kind = ev.SourceKind
	}
	profile := c.profiles.Current()
	// The choice is resolved once by the caller and handed to the brief, the task spec and
	// the turn row alike. It used to be resolved separately here and in taskSpec, which left
	// the credential and the model one profile reload apart from disagreeing.
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
		Playbook: BriefPlaybook{
			Name:         playbook.Name,
			SystemPrompt: playbook.SystemPrompt,
			AllowedTools: append([]string{}, playbook.AllowedTools...),
			MaxTurns:     playbook.MaxTurns,
		},
		Transcript:  entries,
		Instruction: ev.Text,
		Memory:      c.memory,
	}
	b.Provider = c.providerFor(choice.Agent)
	if playbook.Browser {
		// The same flag that put the sidecar on the task spec puts its address in the
		// brief. They cannot disagree: a turn with the tools and no browser, or a browser
		// no tool can reach, is worse than a turn with neither.
		b.Browser = &BriefBrowser{CDPURL: browserCDPURL}
	}
	for _, r := range playbook.Repos {
		b.Repos = append(b.Repos, BriefRepo{Name: r.Name, URL: r.URL, DefaultBranch: r.DefaultBranch})
	}
	for _, s := range bundles {
		b.Playbook.Skills = append(b.Playbook.Skills, BriefSkill{
			Name: s.Name, SHA256: s.SHA256, BundleEnv: s.Env,
		})
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
		// PODIUM_AGENT_XAI_BASE_URL names the host, not an API root: validateXAIKey builds
		// `<base>/v1/models` from the same value. The harness is handed a provider baseURL
		// and appends the endpoint to it directly, so it needs the version in the URL —
		// without it a turn dies on its first request with a 404 from
		// https://api.x.ai/responses.
		out.BaseURL = strings.TrimSuffix(c.xaiBaseURL, "/") + "/v1"
	}
	return out
}

// taskSpec is the task one turn runs. The secrets are exactly the playbook's, plus the
// reserved credential the conductor always adds: a playbook only ever gets the credentials its
// own file names, and the one the backend it runs on needs.
func (c *Conductor) taskSpec(
	src Source, playbook profiles.Playbook, encodedBrief string, ev InboundEvent, bundles []skills.Bundle,
	choice profiles.Choice,
) *spec.TaskSpec {
	env := map[string]string{}
	for k, v := range playbook.Env {
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
	// One variable per skill bundle, written last so a playbook's own env: can never
	// shadow one. Playbook.validate has already refused the prefix, so this overwrites
	// nothing an operator wrote.
	for _, s := range bundles {
		env[s.Env] = s.Encoded
	}

	if playbook.Docker {
		env[profiles.DockerHostEnv] = dockerSidecarHost
	}

	s := &spec.TaskSpec{
		Image:     playbook.Image,
		Env:       env,
		Labels:    append([]string(nil), playbook.Labels...),
		Resources: playbook.Resources,
		Timeout:   playbook.Timeout,
		Secrets: append(append([]spec.SecretRef(nil), playbook.Secrets...),
			c.reservedSecrets(choice.Agent)...),
		MaxAttempts: 1,
		// A turn is not idempotent: it may already have posted a final. Running it twice
		// would say the same thing twice, so a lost node is surfaced to the human instead.
		RetryOnNodeLoss: false,
	}
	if playbook.Docker || playbook.Browser {
		s.Sidecars = map[string]spec.Sidecar{}
	}
	if playbook.Docker {
		s.Sidecars[dockerSidecarName] = dockerSidecar()
	}
	if playbook.Browser {
		s.Sidecars[browserSidecarName] = browserSidecar()
	}
	s.ApplyDefaults()
	return s
}

// skillBundles reads and packs the Agent Skills the playbook names. A playbook that names
// none reads nothing: neither source has to exist, or be configured, for a bot that does not
// use skills.
func (c *Conductor) skillBundles(ctx context.Context, playbook profiles.Playbook) ([]skills.Bundle, error) {
	if len(playbook.Skills) == 0 {
		return nil, nil
	}
	bundles, err := c.skills().Bundles(ctx, playbook.Skills)
	if err != nil {
		return nil, fmt.Errorf("playbook %q: %w", playbook.Name, err)
	}
	return bundles, nil
}

// skills is the library one turn resolves its names against: the directory on this host
// first, then the conductor's database. The directory wins — see skills.Library.
//
// The nil check is not decoration. A *store.Store assigned straight into an interface field
// gives a non-nil interface holding a nil pointer, and the library's "do I have a database"
// test would then be true on a Conductor built without one.
func (c *Conductor) skills() skills.Library {
	lib := skills.Library{Dir: c.skillsDir}
	if c.store != nil {
		lib.Store = c.store
	}
	return lib
}

// The Docker daemon a `docker: true` playbook gets. It is the conductor's to build rather
// than the playbook file's: a half-configured daemon — TLS still on, no workspace, no probe —
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

// The headless Chrome a `browser: true` playbook gets, on the same reasoning as the daemon
// above: the shape that works is one shape, and a playbook file guessing at it produces
// failures that read as the agent's.
const (
	// browserSidecarName is the DNS alias the browser answers to on the task's own private
	// network, which is why the URL below can be a constant.
	browserSidecarName = "chrome"

	// browserCDPURL is what the runtime hands its MCP server. Plaintext for the same
	// reason the daemon's port is: the network carries this task and its sidecars alone.
	browserCDPURL = "http://" + browserSidecarName + ":9222"

	// Pinned by digest, and the digest is the multi-arch index so it resolves on amd64 and
	// arm64 alike. headless-shell rather than a full Chrome image: no window server, no
	// extensions, no updater — the browser a turn actually needs is the rendering half.
	browserSidecarImage = "chromedp/headless-shell:151.0.7922.109@sha256:" +
		"2d349b544a1ea6b5b5fd7c0fe99215ff662339c57407ee2e8c0a11af93516b04"

	// browserSidecarReady is short because headless-shell listens in about a second. A
	// browser that has not opened its port in thirty is not slow, it is broken.
	browserSidecarReady = 30 * time.Second
)

func browserSidecar() spec.Sidecar {
	return spec.Sidecar{
		Image: browserSidecarImage,
		// Everything about listening is the image's own doing and must not be repeated
		// here: its run.sh already puts headless-shell on 9223 behind a socat listener on
		// 9222, with --no-sandbox and a software GL stack. Passing --remote-debugging-port
		// again lands on that listener and the browser dies with "Address already in use".
		//
		// What is left to say is the one thing a container changes: Chrome sizes its
		// shared memory from /dev/shm, which is 64 MB in a container and not enough for a
		// real page, and the tab dies rather than the process — so it reads as a page that
		// will not load.
		Command: []string{"--disable-dev-shm-usage"},
		// A command probe, not tcp_port, for two reasons — and the first one makes the
		// second moot anyway.
		//
		// A tcp_port probe is run from inside the container, because a node cannot route
		// to a task's own network. It needs `nc` or `wget` to do that, and this image has
		// neither: it carries a browser, socat and bash. The probe fails immediately with
		// "readiness probe not supported by this image", the sidecar never comes ready,
		// and the turn dies at provisioning while the browser sits there working.
		//
		// The second reason survives the first: 9222 is socat's listener, and socat binds
		// it before the browser behind it exists, so a port check there can report ready
		// while there is nothing to drive. 9223 is the browser's own port. Probing it is
		// the difference between "something is listening" and "Chrome is up".
		//
		// bash's /dev/tcp is a redirection, not a program, so it needs nothing installed.
		Readiness: spec.Readiness{
			Command: []string{"bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/9223"},
			Timeout: spec.Duration(browserSidecarReady),
		},
	}
}

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

// reservedSecrets are the credentials the conductor attaches itself, whatever the playbook
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
	ctx context.Context, src Source, sess store.Session, playbook profiles.Playbook,
	turn store.Turn, ref string, started time.Time, status string,
) {
	if err := c.store.FinishTurn(ctx, turn.ID, status, nil, nil, ""); err != nil {
		c.logger.ErrorContext(ctx, "finishing a failed turn failed", "turn_id", turn.ID, "error", err)
	}
	c.finish(ctx, src, ref, ReactionFailed)
	c.metrics.Turns.WithLabelValues(sess.SourceKind, playbook.Name, status).Inc()
	c.metrics.TurnDuration.WithLabelValues(playbook.Name).Observe(time.Since(started).Seconds())
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
		playbook := c.profiles.Current().Playbooks[sess.Playbook]

		if turn.TaskID == "" {
			// Either a turn whose task was never created, or a HOST turn — which runs as a
			// child of this process and therefore did not survive the restart. Neither can
			// be resumed and both are already over.
			c.logger.WarnContext(ctx, "a turn with no task was in flight; failing it",
				"turn_id", turn.ID)
			if err := c.store.FinishTurn(ctx, turn.ID, store.TurnFailed, nil, nil, ""); err != nil {
				c.logger.ErrorContext(ctx, "failing an orphaned turn failed", "turn_id", turn.ID, "error", err)
			}
			if src != nil {
				c.post(ctx, src, turn.TriggerRef, Outbound{Type: OutFailure, Text: "I was restarted while this was in flight and it did not survive. Nothing is still running. Ask me again."})
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
			playbook:  playbook,
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
