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
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/memory"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
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
	// MemorySandboxMCPURL is Memory's MCP URL as a cloud sandbox reaches it, for a turn
	// placed on a provider. Empty briefs such a turn with Memory as it is.
	MemorySandboxMCPURL string
	// MemoryClient is what the end-of-turn retain writes through. Nil disables retaining
	// while leaving the briefs alone, which is what a test that only cares about the brief
	// wants; in production the two are set together.
	MemoryClient memory.Client
	// XAIBaseURL is where a Grok turn's container sends the agent SDK's requests. Empty
	// means config.DefaultXAIBaseURL, so a hand-built Conductor still produces a usable
	// brief.
	XAIBaseURL string
	// OpenAIBaseURL is where an OpenAI turn that spent an API key sends the harness.
	// Empty means config.DefaultOpenAIBaseURL.
	OpenAIBaseURL string
	// OpenAICodexBaseURL is where an OpenAI turn that spent a ChatGPT subscription
	// token sends the harness. Empty means config.DefaultOpenAICodexBaseURL.
	OpenAICodexBaseURL string
	// GoogleBaseURL is where a Gemini turn sends the harness. Empty means
	// config.DefaultGoogleBaseURL.
	GoogleBaseURL string
	// SkillsDir is the directory on this host that Agent Skills are read from
	// (PODIUM_AGENT_SKILLS_DIR). Empty means this conductor delivers none, and a playbook
	// that names one fails its turns saying so.
	SkillsDir string
	// Host is the runtime this process runs a turn on itself. Nil means every turn is a
	// task, which is what a conductor whose host has no runtime must do; set, it is what
	// answers a conversation, and a container is what it delegates to. See host.go.
	Host *HostRuntime
	// Mirror is told about every row the mirror writes, so a web client watching a mirrored
	// thread sees it appear. Nil means nobody is watching; the rows are still written.
	Mirror MirrorWatcher
	// HostMaxTurns is how many host turns may run at once. Zero means
	// DefaultHostMaxTurns. It exists because a host turn is a process on this machine and
	// Slack decides how many conversations there are: see acquireHostSlot.
	HostMaxTurns int
	// GitHub mints the installation tokens turns clone and push with. Nil means this
	// conductor has no GitHub App, and a playbook naming its own podium.agent.github_token
	// works exactly as it did. See gitcred.go.
	GitHub *github.Client
	// GitTaskURL is this conductor's base URL as a TASK CONTAINER reaches it
	// (config.TaskURL). It is carried in the brief of every turn that gets a capability,
	// and it is only read when GitHub is set.
	GitTaskURL string
	// MintSecret is what a turn's minting capability is signed with. It is derived from
	// the conductor's own bearer rather than generated, so a capability outlives a restart
	// exactly as the turn holding it does. Empty with GitHub set is refused by New.
	MintSecret string
	// GitHubAPIURL is where a person's connected-account token is scoped and revoked, and
	// GitHubURL where it is refreshed. Empty means github.com. They exist for tests.
	GitHubAPIURL string
	GitHubURL    string
	// AllowedDomains are the Google Workspace domains a Slack speaker's email must be on to
	// be resolved to a Podium login (PODIUM_ALLOWED_DOMAINS). Empty skips that one check;
	// the email must still be a login that has signed in. See identity.go.
	AllowedDomains []string
	// TranscriptBudgetBytes and TranscriptKeepRecent bound the transcript a turn is handed
	// (transcript.go). Zero is DefaultTranscriptBudgetBytes / DefaultTranscriptKeepRecent.
	TranscriptBudgetBytes int
	TranscriptKeepRecent  int
	// OutboxRetryDelay is the delivery outbox's backoff base (outbox.go). Zero is the real
	// schedule (10s, doubling); negative retries a failed delivery at once, which is what a
	// test wants and nothing else does.
	OutboxRetryDelay time.Duration
	// ChatHost answers conversations on one long-lived opencode 2 service instead of a
	// runtime per turn (chathost.go). Nil runs every assistant turn as a host process. It
	// needs Host as well: the brief, the prompt and the credentials are a host turn's.
	ChatHost ChatHost
	// ChatHostURL is this conductor as the chat host reaches it: where its gateway and tools
	// are. Loopback, since the chat host runs on this machine.
	ChatHostURL string
	// ChatHostRender renders a turn's system prompt from its encoded brief — the runtime's
	// own prompt, in the form the chat host calls tools in.
	ChatHostRender func(ctx context.Context, encodedBrief string) (string, error)
	// Notifiers tell a person their delegated task ended when they have left its conversation
	// (notify.go), tried in order. None sends nothing.
	Notifiers []Notifier
	// NotifyAwayAfter is how long a person has said nothing before they count as away. Zero
	// means DefaultNotifyAwayAfter.
	NotifyAwayAfter time.Duration
	// PublicURL is this deployment's web UI, for the link in a notification. Empty sends none.
	PublicURL string
	// ChatHostClaude is true when the chat host can run Claude Code, so a person signed in
	// with their Claude subscription has their conversations answered by opencode's
	// claude-agent provider on it. Nowhere else spends a Claude sign-in.
	ChatHostClaude bool
	// AnthropicBaseURL is where the chat host's gateway sends Anthropic requests: the host,
	// without /v1. Empty is https://api.anthropic.com.
	AnthropicBaseURL string
	// ChatHostMaxPerUser bounds one person's chat-host turns running at once, across all of
	// their conversations. Zero means DefaultChatHostMaxPerUser.
	ChatHostMaxPerUser int
	// ChatHostMaxTurns bounds the chat-host turns running at once. Zero means
	// DefaultChatHostMaxTurns.
	ChatHostMaxTurns int
	// ReplicaID is this conductor's identity among the conductors sharing its database, and
	// what its leases are held under (lease.go). It must be different for each conductor and
	// the same across one conductor's restarts. Empty is a random id.
	ReplicaID string
	// LeaseTTL is how long a lease lasts unrenewed. Zero means DefaultLeaseTTL.
	LeaseTTL time.Duration
	// SandboxTaskURL is this conductor's turn surface as a cloud SANDBOX reaches it — a
	// playbook placed on a provider redeems its git capability there — which is a different
	// vantage point from GitTaskURL: a sandbox is not on this host's Docker network. Empty
	// means GitTaskURL. See turnSurfaceURL.
	SandboxTaskURL string
}

// Conductor owns the turn loop. One instance drains every source.
type Conductor struct {
	store    *store.Store
	podium   *podium.Client
	profiles *profiles.Live
	// sources is guarded by mu: ReplaceSource swaps Slack and GitHub while turns run.
	sources []Source
	metrics *Metrics
	logger  *slog.Logger
	memory  *BriefMemory
	// memorySandboxMCPURL is Options.MemorySandboxMCPURL.
	memorySandboxMCPURL string
	// memories is the shared-memory client. See retain.go.
	memories memory.Client
	// pending is the delegation announcements not said yet, oldest first. Guarded by mu;
	// see holdAnnouncement for why they wait at all.
	pending []announcement
	// lastExtractionCheck is when the extraction watcher last looked, and what makes a
	// failure NEWS rather than history. Touched only by that one goroutine, so it needs no
	// lock; see reconcile.go for why the window exists at all.
	lastExtractionCheck time.Time
	// xaiBaseURL is the endpoint a Grok turn's brief names.
	xaiBaseURL string
	// openaiBaseURL is the endpoint an OpenAI API-key turn's brief names, and
	// openaiCodexBaseURL the one a ChatGPT subscription turn's brief names.
	openaiBaseURL      string
	openaiCodexBaseURL string
	// googleBaseURL is the endpoint a Gemini turn's brief names.
	googleBaseURL string
	// skillsDir is where a turn's Agent Skills are read from.
	skillsDir string
	// host is the runtime for a turn this process runs itself, nil when it runs none.
	host   *HostRuntime
	mirror MirrorWatcher
	// hostSlots is the cap on concurrent host turns, one token per slot. Buffered and not
	// a mutex because a queued turn waits on ctx as well.
	hostSlots chan struct{}
	// github mints installation tokens; nil when no GitHub App is configured. It is guarded
	// by mu and read through githubApp, because SetGitHub swaps it while turns run. gitTaskURL
	// is where a task reaches this conductor to redeem a capability, and mintSecret is
	// what those capabilities are signed with. See gitcred.go.
	github     *github.Client
	gitTaskURL string
	mintSecret string
	// sandboxTaskURL is where a sandbox reaches the turn surface. See surface.go.
	sandboxTaskURL string
	// chatHost and the rest are the chat host, when there is one. See chathost.go.
	chatHost    ChatHost
	chatHostURL string
	chatRender  func(ctx context.Context, encodedBrief string) (string, error)
	chatSlots   chan struct{}
	chatPerUser int
	chat        chatState
	// anthropicBaseURL is AnthropicBaseURL.
	anthropicBaseURL string
	// chatHostClaude is ChatHostClaude.
	chatHostClaude bool
	// notifiers, notifyAwayAfter and publicURL are notify.go's.
	notifiers       []Notifier
	notifyAwayAfter time.Duration
	publicURL       string
	// replicaID, leaseTTL and heldLeases are this conductor's share of the work with the
	// others on its database. heldLeases is guarded by mu. See lease.go.
	replicaID string
	// runStarted is when Run began. The recovery pass at startup is about work from before
	// this process; anything recorded after it is this process's own, and in flight.
	runStarted time.Time
	leaseTTL   time.Duration
	heldLeases map[string]bool
	// githubAPIURL is where a person's token is scoped and revoked, and userTokens the
	// scoped tokens each running turn holds. See usertoken.go.
	githubAPIURL string
	githubURL    string
	// allowedDomains is Options.AllowedDomains.
	allowedDomains []string
	// outboxBase is Options.OutboxRetryDelay.
	outboxBase time.Duration
	// summarizer condenses the chatter a transcript budget leaves out. Nil is the
	// extractive default; a test replaces it. See transcript.go.
	summarizer Summarizer
	// transcriptBudget and transcriptKeep are Options.TranscriptBudgetBytes and
	// TranscriptKeepRecent with their defaults applied.
	transcriptBudget int
	transcriptKeep   int
	// idents caches who a Slack or GitHub account is, and rate-limits review commands from
	// outside Podium. See identity.go.
	idents identityCache
	// clock is time.Now, replaceable by a test.
	clock        func() time.Time
	userTokensMu sync.Mutex
	userTokens   map[string]*userTokenSet
	loginLocks   map[string]*sync.Mutex

	mu       sync.Mutex
	sessions map[string]*sessionState
	// hostRuns are the host turns in flight, by the ref of the message that started each,
	// so one can be cancelled without stopping the conductor.
	hostRuns map[string]func()
	// earlyStops are Stops that arrived for a turn already recorded as running but not yet
	// registered in hostRuns: the turn row is written a moment before the turn takes its
	// stop. registerHost honours one it finds, so Stop pressed in that gap is not lost.
	earlyStops map[string]time.Time
	// turnTokens is the authority a host turn holds while it runs: one token per turn,
	// scoped to that turn's conversation and its menu of playbooks, revoked when the turn
	// ends. Not persisted — a host turn does not survive this process, so neither should
	// what it may do. See delegate.go.
	turnTokens map[string]turnGrant
	// liveDelegations are the delegated tasks this process is following, by delegation id.
	// The durable half is the delegations table; this is only how a turn asking for
	// progress finds the last thing its task said.
	liveDelegations map[string]*delegationRun
	// routineTurns is the scheduled routine runs this conductor is running, by turn id
	// (routine.go). Guarded by mu.
	routineTurns map[string]*routineTurn
	// routineCreator is what create_routine creates routines with (routine.go).
	routineCreator RoutineCreator
	// workspaceMu makes claiming a pull request's workspace and recording the delegation
	// that claimed it one step. See delegate.
	workspaceMu sync.Mutex
	// baseCtx is the conductor's own lifetime, set by Run.
	//
	// It exists because following a delegated task is the conductor's work, not the work of
	// whatever asked for it. Delegate is an RPC, and an RPC's context is cancelled the
	// moment its response is written — a background run started on one would stop following
	// its task before the caller had finished reading the answer.
	baseCtx context.Context
	wg      sync.WaitGroup
}

// sessionState is the in-memory half of a session: whether a turn is in flight and whether
// somebody spoke while it was. It is deliberately not persisted — after a restart there is
// no in-flight turn this process owns, and the recovery pass rebuilds what matters.
type sessionState struct {
	running bool
	// turnLogin and turnKind are whose turn this session last ran, and on which surface. A
	// message is injected into a turn that is still up only when it is from the same person
	// on the same surface; anything else is a turn of its own (queue.go).
	turnLogin string
	turnKind  string
	// local is turns queued in this process because the database refused to queue them.
	// They are served after the durable queue.
	local []InboundEvent
	// awaitingTaskID is the running task that posted a question and is waiting for a
	// human reply. Empty means this session is not parked. It outlives the host turn
	// that started a delegated task, which is why a reply can land after the assistant
	// has already finished.
	awaitingTaskID string
	// warmTaskID is a playbook task that has answered and is still up, waiting for the
	// next instruction. A follow-up is injected into it. It is cleared when the task
	// exits, which is when the node stores the snapshot.
	warmTaskID string
	// warmHost is a host turn that has answered and is still up, waiting for the next
	// message, which is written into its inbox. See hostwarm.go.
	warmHost *warmHost
	// emptyPops is how many times in a row kick took the lease and found nothing it could
	// pop while the queue still counted rows (queue.go, retryQueue). Reset by a pop that works.
	emptyPops int
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
	// A capability nobody can verify is a turn that cannot push, discovered at its first
	// clone. Refuse the combination here instead.
	if opts.GitHub != nil {
		switch {
		case opts.MintSecret == "":
			return nil, errors.New("conductor: a GitHub App needs a mint secret to sign a turn's " +
				"capability with")
		case opts.GitTaskURL == "":
			return nil, errors.New("conductor: a GitHub App needs the URL a task container " +
				"reaches this conductor on, to redeem that capability")
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
	c := &Conductor{
		store:               opts.Store,
		podium:              opts.Podium,
		profiles:            opts.Profiles,
		sources:             opts.Sources,
		metrics:             metrics,
		logger:              logger,
		memory:              opts.Memory,
		memorySandboxMCPURL: opts.MemorySandboxMCPURL,
		memories:            opts.MemoryClient,
		xaiBaseURL:          cmp.Or(opts.XAIBaseURL, config.DefaultXAIBaseURL),
		openaiBaseURL:       cmp.Or(opts.OpenAIBaseURL, config.DefaultOpenAIBaseURL),
		openaiCodexBaseURL:  cmp.Or(opts.OpenAICodexBaseURL, config.DefaultOpenAICodexBaseURL),
		googleBaseURL:       cmp.Or(opts.GoogleBaseURL, config.DefaultGoogleBaseURL),
		skillsDir:           opts.SkillsDir,
		host:                opts.Host,
		mirror:              opts.Mirror,
		hostSlots:           make(chan struct{}, cmp.Or(opts.HostMaxTurns, DefaultHostMaxTurns)),
		github:              opts.GitHub,
		gitTaskURL:          opts.GitTaskURL,
		mintSecret:          opts.MintSecret,
		sandboxTaskURL:      opts.SandboxTaskURL,
		chatHost:            opts.ChatHost,
		chatHostURL:         opts.ChatHostURL,
		chatRender:          opts.ChatHostRender,
		anthropicBaseURL:    opts.AnthropicBaseURL,
		chatHostClaude:      opts.ChatHostClaude && opts.ChatHost != nil,
		notifiers:           opts.Notifiers,
		notifyAwayAfter:     cmp.Or(opts.NotifyAwayAfter, DefaultNotifyAwayAfter),
		publicURL:           opts.PublicURL,
		chatSlots:           make(chan struct{}, cmp.Or(opts.ChatHostMaxTurns, DefaultChatHostMaxTurns)),
		chatPerUser:         cmp.Or(opts.ChatHostMaxPerUser, DefaultChatHostMaxPerUser),
		replicaID:           cmp.Or(opts.ReplicaID, newReplicaID()),
		leaseTTL:            cmp.Or(opts.LeaseTTL, DefaultLeaseTTL),
		heldLeases:          map[string]bool{},
		githubAPIURL:        opts.GitHubAPIURL,
		githubURL:           opts.GitHubURL,
		allowedDomains:      opts.AllowedDomains,
		transcriptBudget:    cmp.Or(opts.TranscriptBudgetBytes, DefaultTranscriptBudgetBytes),
		transcriptKeep:      cmp.Or(opts.TranscriptKeepRecent, DefaultTranscriptKeepRecent),
		outboxBase:          opts.OutboxRetryDelay,
		sessions:            map[string]*sessionState{},
		hostRuns:            map[string]func(){},
		turnTokens:          map[string]turnGrant{},
		liveDelegations:     map[string]*delegationRun{},
		routineTurns:        map[string]*routineTurn{},
	}
	// A source that fans answers out to another (the GitHub source, to the Slack threads bound
	// to a pull request) hands each copy back here, so it is owed and retried like the
	// primary. See outbox.go.
	for _, src := range c.sources {
		if ds, ok := src.(deliverySetter); ok {
			ds.SetDeliverer(c)
		}
	}
	return c, nil
}

// Run drains every source until ctx is cancelled, and resumes whatever was in flight when
// the process last died. It returns once every source channel is closed or ctx is done.
func (c *Conductor) Run(ctx context.Context) error {
	c.mu.Lock()
	c.baseCtx = ctx
	c.runStarted = time.Now().UTC()
	c.mu.Unlock()
	c.recover(ctx)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.watchExtractions(ctx)
	}()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.renewLeases(ctx)
	}()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deliverOutbox(ctx)
	}()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.listenCancels(ctx)
	}()
	if c.chatHost != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.reapChats(ctx)
		}()
	}

	for _, src := range c.sourceList() {
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

// accept does the bookkeeping an event needs before any work starts: work out what the
// event runs, find or create the session, and either start a turn or remember the message
// for the turn that is already running.
func (c *Conductor) accept(ctx context.Context, src Source, ev InboundEvent) {
	c.rememberSlackChannel(ctx, ev)
	// Who said it.
	c.resolveSpeaker(ctx, src, &ev)
	// An edit, a delete, or chatter nobody put to Podium is recorded and is never a turn.
	switch {
	case ev.Change != "":
		c.mirrorChanged(ctx, src, ev)
		return
	case ev.RecordOnly:
		c.mirrorRecordOnly(ctx, src, ev)
		return
	}
	// Recorded before anything is decided about it, and whoever answers it: a conversation
	// whose first turn fails, or that the speaker may not start a turn in at all, is still a
	// conversation somebody can read. The recording is also the dedupe — a Slack retry, a
	// GitHub redelivery or a second conductor hearing the same event finds its message
	// already written and stops here, so one message is never two turns. A no-op that reports
	// fresh for the web chat, which stores its own messages, and for a source that is not
	// mirrored at all.
	if !c.mirrorHeard(ctx, src, &ev) {
		c.logger.InfoContext(ctx, "an inbound message was already recorded; not acting on it twice",
			"source", src.Kind(), "source_key", ev.SourceKey, "external_id", ev.ExternalID)
		return
	}
	// A message the web chat held back (ev.Held) is in nobody's record until a turn takes it.
	// Whatever path this event leaves by without one — refused, a failed start — the message
	// is recorded on the way out, so what a person said is never simply gone. A path that
	// queued it, or put it into a turn, has cleared ev.Held and this does nothing.
	defer c.recordHeld(ctx, src, &ev)
	// Whether they may start anything at all. A refused event is reacted to by admit.
	if !c.admit(ctx, src, &ev) {
		return
	}
	// One snapshot for the whole of this event. A profile swapped in half way through must
	// not decide the job against one document and then start the turn against another.
	profile := c.profiles.Current()

	// A pull request is a conversation: what people say on it is answered by the assistant,
	// which delegates the review to the playbook marked github: true. What nobody said — a
	// pull request opened, or marked ready — runs that playbook directly. So does every
	// GitHub event on a conductor with no host runtime, which has no assistant to ask.
	if src.Kind() == SourceGitHub && (ev.Automatic || c.host == nil) {
		name := profile.GitHubPlaybook()
		if name == "" {
			c.logger.ErrorContext(ctx, "no playbook reviews pull requests",
				"source_key", ev.SourceKey)
			c.post(ctx, src, ev.Ref, Outbound{
				Type: OutFailure,
				Text: "No playbook reviews pull requests. Mark one with github: true.",
			})
			c.finish(ctx, src, ev.Ref, ReactionFailed)
			return
		}
		ev.Playbook = name
		if c.host != nil {
			c.acceptAutomatic(ctx, src, profile, ev)
			return
		}
	}

	var j job
	var sess store.Session
	var ok bool
	// A conversation with a host runtime is answered here. A web chat without one is
	// refused rather than turned into a container task. The test-only KindDev source,
	// and Slack on a conductor that has no host, still run as tasks so the playbook
	// loop can be exercised without forking node.
	switch {
	case c.host != nil && aConversation(src.Kind()):
		j, sess, ok = c.acceptConversation(ctx, src, profile, &ev)
	case src.Kind() == SourceChat:
		c.logger.ErrorContext(ctx, "a conversation arrived with no host runtime",
			"source", src.Kind(), "source_key", ev.SourceKey)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "I couldn't start a turn: this conductor has no host runtime, so it cannot answer a conversation here."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return
	default:
		j, sess, ok = c.acceptTask(ctx, src, profile, &ev)
	}
	if !ok {
		return
	}

	// A turn that is still up takes the message when it is from the same person on the same
	// surface: a warm host turn, a running chat-host turn, a task that asked a question or is
	// waiting for a follow-up. Anybody else's message is a turn of its own (queue.go).
	//
	// Except a running turn, for a message the web chat held back (ev.Held): somebody who
	// sends from the web while Podium is answering is queueing it, the way other chat clients
	// queue, and each message gets an answer of its own. Steered into the turn in progress, a
	// second question was folded into the answer to the first.
	c.mu.Lock()
	st := c.state(sess.ID)
	same := c.sameAsker(st, src, ev)
	if wh := st.warmHost; wh != nil && st.awaitingTaskID == "" && same {
		// The host turn that answered this conversation is still up: the message goes into
		// it, and it is working again, so it is no longer a candidate to give its slot up.
		st.warmHost = nil
		c.mu.Unlock()
		c.recordHeld(ctx, src, &ev)
		if err := wh.inbox.inject(ev.Text); err == nil {
			c.metrics.WarmInjects.WithLabelValues(profiles.RunsOnHost).Inc()
			c.logger.InfoContext(ctx, "injected a message into a warm host turn", "session_id", sess.ID)
			placed(ev, 0)
			return
		}
		// Its inbox closed as it exited. The message becomes the next turn once the one
		// exiting has finished.
		c.mu.Lock()
		st = c.state(sess.ID)
	}
	if st.running && c.chatHost != nil && same && !ev.Held {
		// A chat-host turn is answering this conversation: the message goes into it, and is
		// answered as part of that turn rather than after it.
		c.mu.Unlock()
		if c.chatSteer(ctx, sess.ID, ev.Text) {
			c.logger.InfoContext(ctx, "steered a message into a running chat-host turn", "session_id", sess.ID)
			placed(ev, 0)
			return
		}
		c.mu.Lock()
		st = c.state(sess.ID)
	}
	injectID := st.awaitingTaskID
	if injectID == "" {
		injectID = st.warmTaskID
	}
	c.mu.Unlock()
	if injectID != "" && same {
		c.recordHeld(ctx, src, &ev)
		err := c.podium.InjectTask(ctx, injectID, ev.Text)
		if err == nil {
			c.logger.InfoContext(ctx, "injected a message into a running task",
				"session_id", sess.ID, "task_id", injectID)
			placed(ev, 0)
			return
		}
		c.logger.WarnContext(ctx, "injecting a reply into a running turn failed; queueing it as the next turn",
			"session_id", sess.ID, "task_id", injectID, "error", err)
	}
	// Another conductor may be serving this conversation, with a warm task answering this
	// same person: then the message goes straight into it.
	if c.injectIntoOwner(ctx, src, sess.ID, &ev) {
		placed(ev, 0)
		return
	}

	// Otherwise it is a turn: queued first, then served by whoever serves the session. Nothing
	// is lost and nothing runs twice — the row is the turn, and only one conductor takes it.
	c.enqueue(ctx, src, sess, ev)
	// The queued copy is still held and is recorded when its turn starts; this one is done.
	ev.Held = false
	c.kick(ctx, src, sess, j)
}

// runningDelegations is what this conversation already has in flight, for the brief. A
// failure is logged and the turn runs without the list: a thinner brief is worse than a
// complete one and better than no turn.
//
// The conversation is the session, not the ref. A Slack thread's ref names the message that
// asked, so scoping by it showed a follow-up in the same thread nothing running, and the
// assistant started a second task beside the one it should have injected into.
func (c *Conductor) runningDelegations(ctx context.Context, sessionID string) []BriefRunningDelegation {
	if c.store == nil || sessionID == "" {
		return nil
	}
	rows, err := c.store.RunningDelegationsForSession(ctx, sessionID)
	if err != nil {
		c.logger.WarnContext(ctx, "listing the conversation's running delegations failed",
			"session_id", sessionID, "error", err)
		return nil
	}
	out := make([]BriefRunningDelegation, 0, len(rows))
	for _, d := range rows {
		out = append(out, BriefRunningDelegation{
			ID:          d.ID,
			Playbook:    d.Playbook,
			Instruction: headOf(d.Instruction, 160),
			StartedAt:   BriefTimestamp(d.CreatedAt),
		})
	}
	return out
}

// headOf is the first line of s, cut to n runes. A delegation's instruction is a paragraph
// and the brief only needs enough of it to tell two tasks apart.
func headOf(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// setAwaiting records that this session's running task asked a question and is
// waiting. The next inbound is injected into that task instead of starting a turn.
func (c *Conductor) setAwaiting(sessionID, taskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil {
		st = &sessionState{}
		c.sessions[sessionID] = st
	}
	st.awaitingTaskID = taskID
	go func() { c.noteWarm(c.background(), sessionID, taskID) }()
}

// setWarm records that this session's task has answered and is waiting for a follow-up.
func (c *Conductor) setWarm(sessionID, taskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil {
		st = &sessionState{}
		c.sessions[sessionID] = st
	}
	st.warmTaskID = taskID
	go func() { c.noteWarm(c.background(), sessionID, taskID) }()
}

// clearWarm drops the warm task once it has exited.
func (c *Conductor) clearWarm(sessionID, taskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil || st.warmTaskID != taskID {
		return
	}
	st.warmTaskID = ""
	go func() { c.noteWarm(c.background(), sessionID, "") }()
}

// clearAwaiting drops a parked question once the task speaks again, or ends.
// It reports whether this session was waiting on that task, so the caller can
// tell the source the turn is working again.
func (c *Conductor) clearAwaiting(sessionID, taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil || st.awaitingTaskID != taskID {
		return false
	}
	st.awaitingTaskID = ""
	go func() { c.noteWarm(c.background(), sessionID, "") }()
	return true
}

// acceptConversation is a chat window's message. It runs the ASSISTANT — this process, the
// short tool list, the delegation menu — so there is no playbook to route to, nothing for a
// human to pick and nothing for the session to be pinned to. The playbooks are what the turn
// hands work to, chosen per piece of work by the turn itself.
func (c *Conductor) acceptConversation(
	ctx context.Context, src Source, profile *profiles.Profile, ev *InboundEvent,
) (job, store.Session, bool) {
	sess, err := c.store.UpsertSession(ctx, store.Session{
		SourceKind: src.Kind(),
		SourceKey:  ev.SourceKey,
		Profile:    sessionProfileName(profile.Name, ev.PersonalityName),
	})
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the session failed", "source_key", ev.SourceKey, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "I couldn't start a turn."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return job{}, store.Session{}, false
	}
	// A conversation this bot answered before host turns existed has a playbook on its row,
	// left there by a chip that no longer exists. Clearing it is what stops the sessions
	// screen crediting this turn to a playbook it will not run.
	if sess.Playbook != "" {
		if err := c.store.SetSessionPlaybook(ctx, sess.ID, ""); err != nil {
			c.logger.WarnContext(ctx, "clearing a conversation's old playbook failed",
				"session_id", sess.ID, "playbook", sess.Playbook, "error", err)
		}
		sess.Playbook = ""
	}
	// No prefix is stripped: with no playbook to select, a leading /word is just something
	// the person typed, and eating it would lose a word out of their question.
	ev.Text = strings.TrimSpace(ev.Text)
	return assistantJob(profile.Assistant()), sess, true
}

// acceptAutomatic runs the source's own playbook, ev.Playbook, on something nobody said: a
// pull request opened, or marked ready. There is nothing for the assistant to decide, so the
// conductor delegates it itself. It is still a delegation of the conversation, so the review
// is posted the way one the assistant asked for would be, and the next comment's turn sees it
// running and can add to it.
//
// A delegation hangs off a turn, so the event gets one. It records that the event was handled
// here, ran no model and cost nothing, and it is finished once the task has started.
func (c *Conductor) acceptAutomatic(ctx context.Context, src Source, profile *profiles.Profile, ev InboundEvent) {
	_, sess, ok := c.acceptConversation(ctx, src, profile, &ev)
	if !ok {
		return
	}
	turn, err := c.store.CreateTurnFor(ctx, sess.ID, ev.Ref, c.turnStartedBy(ctx, src, sess, ev), store.Backend{})
	if err == nil {
		// A turn with no task, possibly for hours while it queues behind a writer: the lease
		// is what tells another conductor's restart that it is alive.
		c.acquire(ctx, turnLease(turn.ID))
		defer c.release(ctx, turnLease(turn.ID))
	}
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the turn failed", "session_id", sess.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Speaker: c.speakerFor(src, ev.Playbook),
			Text: "Something went wrong on my side before I could start. Nothing ran."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return
	}
	status := store.TurnSucceeded
	_, err = c.delegate(ctx, turnGrant{
		turnID:    turn.ID,
		sessionID: sess.ID,
		ref:       ev.Ref,
		src:       src,
		playbooks: []string{ev.Playbook},
	}, ev.Playbook, ev.Text, DelegateOptions{Wait: true})
	if err != nil {
		status = store.TurnFailed
		c.logger.WarnContext(ctx, "starting the source's own playbook failed",
			"session_id", sess.ID, "playbook", ev.Playbook, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Speaker: c.speakerFor(src, ev.Playbook),
			Text: fmt.Sprintf("I could not start the `%s` playbook.", ev.Playbook)})
	}
	none, free := 0, 0.0
	if err := c.store.FinishTurn(ctx, turn.ID, status, &none, &free, ""); err != nil {
		c.logger.ErrorContext(ctx, "recording how the turn ended failed",
			"turn_id", turn.ID, "status", status, "error", err)
	}
	// The task may not have spoken yet. Its announcement is what says it started.
	c.sayAnnouncementsFor(ctx, turn.ID)
	c.metrics.Turns.WithLabelValues(sess.SourceKind, ev.Playbook, status).Inc()
}

// acceptTask is a Linear ticket, or a conversation on a conductor with no host runtime
// (the test-only KindDev source, and Slack before host turns). It runs one playbook as a
// task on a node. A web chat without a host runtime is refused instead.
func (c *Conductor) acceptTask(
	ctx context.Context, src Source, profile *profiles.Profile, ev *InboundEvent,
) (job, store.Session, bool) {
	sel := profile.Select(profiles.Routing{
		Playbook: ev.Playbook,
		Channel:  ev.Channel,
		Text:     ev.Text,
	})
	if sel.Playbook.Name == "" {
		c.logger.ErrorContext(ctx, "no playbook could be selected",
			"source", src.Kind(), "source_key", ev.SourceKey)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "I couldn't start a turn: no playbook matches this. Register one, or name it with /playbook."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return job{}, store.Session{}, false
	}

	sess, err := c.store.UpsertSession(ctx, store.Session{
		SourceKind: src.Kind(),
		SourceKey:  ev.SourceKey,
		Profile:    profile.Name,
		Playbook:   sel.Playbook.Name,
	})
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the session failed", "source_key", ev.SourceKey, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "I couldn't start a turn."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return job{}, store.Session{}, false
	}

	// A THREAD keeps the playbook it started with: a Slack thread and a Linear issue are one
	// piece of work, and a later /other in the middle of one is refused rather than silently
	// ignored, because the human asked for something specific.
	if sel.Explicit && sess.Playbook != sel.Playbook.Name {
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread is running the `%s` playbook and a thread keeps the playbook it started with. "+
				"Start a new thread to use `%s`.", sess.Playbook, sel.Playbook.Name)})
		return job{}, store.Session{}, false
	}
	playbook, ok := profile.Playbooks[sess.Playbook]
	if !ok {
		c.logger.ErrorContext(ctx, "the session's playbook is no longer loaded",
			"session_id", sess.ID, "playbook", sess.Playbook)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"This thread ran the `%s` playbook, which this bot no longer has. Start a new thread.", sess.Playbook)})
		return job{}, store.Session{}, false
	}
	// Whatever the routing rules said, the instruction is the text minus a /playbook prefix.
	ev.Text = sel.Instruction
	return playbookJob(playbook), sess, true
}

// serve runs turns for one session until nothing is queued. It holds the session's
// "running" flag for its whole life, which is what serialises turns within a session while
// leaving different sessions free to run at once. Each queued turn answers through its own
// source: a web message in a mirrored conversation answers in the web chat.
func (c *Conductor) serve(ctx context.Context, src Source, sess store.Session, j job, ev InboundEvent) {
	key := store.SessionLease(sess.ID)
	for {
		c.setTurnAsker(sess.ID, src, ev)
		c.runTurn(ctx, src, sess, j, ev)

		if ctx.Err() != nil {
			// Shutting down: the lease is left to this replica's next start, or to expire,
			// and the queue to whoever serves the session then.
			c.mu.Lock()
			c.state(sess.ID).running = false
			c.mu.Unlock()
			return
		}
		next, nsrc, nj, ok := c.popNext(ctx, src, sess, j)
		if !ok {
			c.letGo(ctx, sess.ID, key)
			return
		}
		ev, src, j = next, nsrc, nj
	}
}

// runTurn is one inbound message, end to end.
func (c *Conductor) runTurn(ctx context.Context, src Source, sess store.Session, j job, ev InboundEvent) {
	started := time.Now()
	// A message that waited out of the record goes into it now, ahead of the placeholder: it
	// is read after the answer it waited for, and before its own.
	c.recordHeld(ctx, src, &ev)

	// The placeholder goes first and the reaction second, because the placeholder is the
	// acknowledgement a human is actually waiting for and a source's write path is rate
	// limited: every call ahead of it is a second of silence after somebody spoke.
	placeholder := c.post(ctx, src, ev.Ref, Outbound{
		Type: OutProgress, Text: Placeholder, Speaker: c.speakerFor(src, j.playbook.Name),
	})
	if err := src.React(ctx, ev.Ref, ReactionWorking); err != nil {
		c.logger.WarnContext(ctx, "reacting to the triggering message failed", "ref", ev.Ref, "error", err)
	}

	// A host turn is a process on THIS machine, so the number of them running at once is
	// bounded here rather than by how many people are talking. The slot is taken after the
	// placeholder and before anything expensive: a queued conversation shows 👀 working…
	// for longer, which is the honest thing for it to show, and spends no Slack reads and
	// no model tokens while it waits.
	//
	// A chat-host turn is not a process, and is bounded by the chat host's own slots
	// (chathost.go) rather than these.
	if j.onHost && c.chatHost == nil {
		release, ok := c.acquireHostSlot(ctx)
		if !ok {
			c.logger.InfoContext(ctx, "the conductor stopped while a turn was queued",
				"session_id", sess.ID)
			return
		}
		defer release()
	}

	// The record, not the source: see transcript.go. The summary and the budget are applied
	// inside, because the budget has to know which lines were put to Podium.
	entries, err := c.turnTranscript(ctx, src, ev.Ref, sess)
	if err != nil {
		// A turn with no history is a worse answer than one with it, and a much better
		// answer than none at all.
		c.logger.WarnContext(ctx, "reading the conversation failed; running the turn without history",
			"ref", ev.Ref, "error", err)
	}

	// Resolved before the turn is recorded so the row says what actually ran, and handed to
	// the brief and the task spec so all three agree on one answer.
	choice := j.choose(c.profiles.Current(), ev.Override)
	backend := store.Backend{
		Agent:    choice.Agent,
		Model:    choice.Model,
		Effort:   choice.Effort,
		Provider: c.providerFor(choice.Agent, "").ID,
	}

	turn, err := c.store.CreateTurnFor(ctx, sess.ID, ev.Ref, c.turnStartedBy(ctx, src, sess, ev), backend)
	if err != nil {
		c.logger.ErrorContext(ctx, "recording the turn failed", "session_id", sess.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "Something went wrong on my side before I could start. Nothing ran."})
		c.finish(ctx, src, ev.Ref, ReactionFailed)
		return
	}
	// A scheduled routine run: the run row learns its turn, and the turn its run.
	c.noteRoutineTurn(ctx, turn.ID, ev)

	// The playbook's Agent Skills, read off this host and packed. It happens per turn and
	// not at start-up so that a skill an operator has just edited is the one the next turn
	// gets, and so that a broken skill fails the playbook that names it rather than the
	// whole conductor.
	bundles, err := c.skillBundles(ctx, j)
	if err != nil {
		// The reason names a path, a database row or a cap on the conductor's own side, so it
		// goes to the log and not to a human, like every other "I could not start".
		c.logger.ErrorContext(ctx, "the turn's agent skills could not be prepared",
			"turn_id", turn.ID, "job", j.name, "skills_dir", c.skillsDir,
			"skills", j.skills, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(
			"`%s` asks for skills I could not prepare, so nothing ran. "+
				"An operator should check the logs.", j.name)})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	// The job's MCP servers, resolved against the registry. Per turn for the same reason the
	// skills are: a server an operator has just disabled must not be handed to the next
	// turn, and a name that no longer resolves fails the job that names it rather than the
	// whole conductor.
	//
	// login is who personal secrets attach to: the person who started this turn, on whatever
	// surface they spoke (turnLogin). Slack and GitHub speakers are only ever resolved to a
	// login that has proven the account is theirs, so their secrets follow them here the
	// way a web chat's always have. speaker is the same person, kept as its own name for
	// the places that only want somebody to count or to pick MCP servers for.
	login := c.turnLogin(ctx, src, sess, ev)
	speaker := login
	mcpOwner := login
	if mcpOwner == "" {
		mcpOwner = speaker
	}
	servers, unavailable, err := c.mcpServers(ctx, j, mcpOwner)
	if err != nil {
		// A playbook naming a server this turn cannot have, a registry that cannot be read,
		// or a saved config that no longer parses. The assistant's missing servers are not
		// here: they are in unavailable, and its turn runs and says so itself.
		c.logger.ErrorContext(ctx, "the turn's mcp servers could not be resolved",
			"turn_id", turn.ID, "job", j.name, "mcp_servers", j.mcpServers, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, TaskID: "", Text: fmt.Sprintf(
			"`%s` asks for an MCP server I do not have, so nothing ran: %v", j.name, err)})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}
	c.logUnavailableMCP(ctx, turn.ID, j.name, unavailable)
	choice.CredentialOwner = c.turnCredentialOwner(ctx, sess, login, choice.Agent, j.onHost && c.chatHost != nil)
	persona, err := c.githubPersona(ctx, login, j.playbook)
	if err == nil {
		_, err = userSecretRefs(login, j.playbook.UserSecrets)
	}
	if err != nil {
		c.logger.ErrorContext(ctx, "the turn's personal secrets cannot be attached",
			"turn_id", turn.ID, "job", j.name, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: err.Error()})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	brief := c.brief(ctx, sess, j, turn.ID, ev, entries, bundles, servers, choice)
	brief.Playbook.UnavailableMCPServers = unavailable
	brief.Routine = c.briefRoutine(ctx, src, ev)
	if persona != nil {
		brief.Git = persona
	}
	c.withRequestedBy(ctx, brief, login, persona)
	// The menu is computed once and used twice: the brief shows it to the model and the
	// turn's token accepts exactly it, so the two cannot disagree.
	var menu []DelegablePlaybook
	if j.onHost {
		menu = c.routineMenu(c.menuFor(src, ev.Role), ev)
		c.hostBrief(brief, menu, c.runningDelegations(ctx, sess.ID), c.livePreviews(ctx, sess.ID))
	}
	encoded, err := brief.Encode()
	if err != nil {
		c.logger.WarnContext(ctx, "the turn brief does not fit", "turn_id", turn.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "This conversation is too large for me to take in at once. Start a new thread with just the question."})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	run := &turnRun{
		sink:        &sink{c: c, src: src, ref: ev.Ref, placeholder: placeholder, speaker: c.speakerFor(src, j.playbook.Name)},
		sess:        sess,
		job:         j,
		turn:        turn,
		author:      ev.Author,
		instruction: ev.Text,
		url:         ev.URL,
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
	if j.onHost {
		host := &hostRun{
			r: run, encoded: encoded, bundles: bundles, servers: servers,
			provider: brief.Provider, menu: menu, credOwner: choice.CredentialOwner,
		}
		if brief.Memory != nil {
			host.memoryKeyEnv = brief.Memory.APIKeyEnv
		}
		if src.Kind() == KindDev {
			// The same TEST-ONLY escape taskSpec allows, and only for the same source: the
			// dry-run knobs step 16 defined are how a test drives a turn with no model.
			host.devEnv = ev.Env
		}
		if brief.Delegation != nil {
			token, err := c.mintTurnToken(turnGrant{
				turnID:    turn.ID,
				sessionID: sess.ID,
				ref:       ev.Ref,
				src:       src,
				playbooks: playbookNames(menu),
				login:     speaker,
			})
			if err != nil {
				// A turn that cannot delegate is still a turn that can answer, and this is
				// a failure of crypto/rand rather than of anything the person asked for.
				c.logger.ErrorContext(ctx, "minting a host turn's delegation token failed; "+
					"it will run without being able to delegate", "turn_id", turn.ID, "error", err)
				brief.Delegation = nil
				if encoded, err = brief.Encode(); err != nil {
					c.logger.ErrorContext(ctx, "re-encoding a brief without delegation failed",
						"turn_id", turn.ID, "error", err)
				}
				host.encoded = encoded
			} else {
				host.token = token
			}
		}
		if c.chatHost != nil {
			host.model, host.effort, host.instruction = brief.Profile.Model, brief.Profile.Effort, brief.Instruction
			// Who the per-person limit counts this turn against: the person when there is
			// one, and the conversation itself when nobody signed in.
			host.who = cmp.Or(speaker, login, "session:"+sess.ID)
			host.login = cmp.Or(login, speaker)
			if brief.Memory != nil {
				host.memoryURL = brief.Memory.MCPURL
			}
			for _, s := range brief.Playbook.MCPServers {
				host.pullRequest = host.pullRequest || s.Name == PullRequestMCPName
				host.githubRead = host.githubRead || s.Name == GitHubReadMCPName
			}
			chatRun{h: host}.run(ctx)
			return
		}
		host.run(ctx)
		return
	}

	// The capability, before the task: admission refuses a spec naming a secret the control
	// plane does not have, so the secret has to exist by the time CreateTask is called.
	capabilitySecret, err := c.provisionGitCapability(ctx, turn.ID, j.playbook, login)
	if err != nil {
		c.logger.WarnContext(ctx, "preparing the turn's github credential failed",
			"turn_id", turn.ID, "playbook", j.name, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "I could not prepare a GitHub credential for this work. An operator should check the logs."})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}

	taskSpec := c.taskSpec(src, c.placed(ctx, j.playbook), encoded, ev, bundles, servers, choice, capabilitySecret, login)
	applyWorkspace(taskSpec, sess, j.playbook)
	task, err := c.podium.CreateTask(ctx, taskSpec, int32(j.playbook.Priority))
	if err != nil {
		// The turn never started, so nothing will reach finish to clean this up.
		if capabilitySecret != "" {
			c.dropGitCapability(ctx, turn.ID, j.playbook)
		}
		// Validation, a missing secret, a control plane that is down: all of them are
		// "I could not start", and none of the reason is a human's business.
		c.logger.WarnContext(ctx, "creating the turn's task failed",
			"turn_id", turn.ID, "image", j.playbook.Image, "error", err)
		text := "Something went wrong on my side and the work never started. An operator should check the logs."
		if len(j.playbook.UserSecrets) > 0 {
			text = err.Error()
		}
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: text})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, started, store.TurnFailed)
		return
	}
	if err := c.store.SetTurnTask(ctx, turn.ID, task.GetId()); err != nil {
		c.logger.ErrorContext(ctx, "binding the turn to its task failed",
			"turn_id", turn.ID, "task_id", task.GetId(), "error", err)
	}
	turn.TaskID = task.GetId()
	run.turn = turn
	run.taskID = task.GetId()
	run.warm = taskSpec.WorkspaceWarm > 0
	c.logger.InfoContext(ctx, "turn started", "turn_id", turn.ID, "session_id", sess.ID,
		"playbook", j.name, "task_id", task.GetId(), "source", src.Kind())

	run.run(ctx)
	c.clearWarm(sess.ID, task.GetId())
}

// playbookNames is the menu as the names a token's grant is checked against.
func playbookNames(menu []DelegablePlaybook) []string {
	out := make([]string, 0, len(menu))
	for _, p := range menu {
		out = append(out, p.Name)
	}
	return out
}

// aConversation is a source that is a chat window rather than a thread. A turn of one is
// answered by the assistant, in this process, and delegates whatever needs a machine. A
// Linear issue runs one playbook as a task: nobody is watching a cursor, and one session
// is one piece of work. A pull request is a conversation: people ask about the code and
// the review, and the assistant answers or delegates — the review to the playbook marked
// github: true, other work to whatever the menu offers. The dev source is in here because
// it is how a conversation is tested.
func aConversation(kind string) bool {
	return kind == SourceChat || kind == SourceSlack || kind == SourceGitHub || kind == KindDev
}

// speakerFor is who a run's output is from: the playbook, and whether it is the one src
// runs on its own. The assistant passes "" and is the zero Speaker.
func (c *Conductor) speakerFor(src Source, playbook string) Speaker {
	if playbook == "" {
		return Speaker{}
	}
	own := src != nil && src.Kind() == SourceGitHub && playbook == c.profiles.Current().GitHubPlaybook()
	return Speaker{Playbook: playbook, SourcePlaybook: own}
}

// rememberSlackChannel writes the id (and name, when Slack resolved one) into the catalogue
// so the Channels screen and the next turn's brief can find it. A failure is logged; the
// turn still runs.
func (c *Conductor) rememberSlackChannel(ctx context.Context, ev InboundEvent) {
	if ev.Channel == "" || c.store == nil {
		return
	}
	if err := c.store.UpsertSlackChannelName(ctx, ev.Channel, ev.ChannelName); err != nil {
		c.logger.WarnContext(ctx, "recording a slack channel name failed",
			"channel", ev.Channel, "error", err)
	}
}

// sourceChannel is the brief's optional Slack channel block. Absent when this event has
// no channel (every source but Slack). Description is looked up at brief time so a note
// edited in the UI applies on the next turn without a new mention.
func (c *Conductor) sourceChannel(ctx context.Context, ev InboundEvent) *BriefChannel {
	if ev.Channel == "" {
		return nil
	}
	ch := &BriefChannel{ID: ev.Channel, Name: ev.ChannelName}
	if c.store == nil {
		return ch
	}
	row, err := c.store.SlackChannel(ctx, ev.Channel)
	if err != nil {
		return ch
	}
	ch.Description = row.Description
	if ch.Name == "" {
		ch.Name = row.Name
	}
	return ch
}

// brief builds the turn brief. It never sets a field the runtime's schema does not have:
// the schema is strict at every level and an unknown key is a failed turn.
func (c *Conductor) brief(
	ctx context.Context, sess store.Session, j job, turnID string, ev InboundEvent,
	entries []BriefEntry, bundles []skills.Bundle, servers []mcp.Server, choice profiles.Choice,
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
		Source:    BriefSource{Kind: kind, Ref: ev.Ref, URL: ev.URL, Channel: c.sourceChannel(ctx, ev)},
		Profile: BriefProfile{
			Name:         profile.Name,
			DisplayName:  profile.DisplayName,
			SystemPrompt: systemPromptWithPersonality(profile.SystemPrompt, ev.PersonalityPrompt),
			Model:        choice.Model,
			Effort:       choice.Effort,
		},
		Playbook: BriefPlaybook{
			Name:         j.name,
			SystemPrompt: j.systemPrompt,
			AllowedTools: append([]string{}, j.allowedTools...),
			MaxTurns:     j.maxTurns,
			Interactive:  j.playbook.Interactive,
			GitHub:       j.playbook.GitHub,
		},
		Transcript:  entries,
		Instruction: ev.Text,
		Memory:      c.memoryFor(c.placedRunsOn(ctx, j)),
	}
	b.Provider = c.providerFor(choice.Agent, choice.CredentialOwner)
	// Where this turn redeems its capability. The capability itself is a secret and is
	// nowhere in here; scope is decided by what the conductor signed, so the brief does not
	// carry that either. Set only when there is something to mint for, which
	// provisionGitCapability decides from the same playbook.
	if c.githubApp() != nil || j.playbook.UsesGitHubAccount() {
		if scope, err := gitScopeOf(j.playbook); err == nil && len(scope.Repos) > 0 {
			b.GitCredentials = &BriefGitCredentials{URL: c.turnSurfaceURL(c.placedRunsOn(ctx, j)), TokenEnv: GitTokenEnv}
		}
	}
	if j.playbook.Browser {
		// The same flag that put the sidecar on the task spec puts its address in the
		// brief. They cannot disagree: a turn with the tools and no browser, or a browser
		// no tool can reach, is worse than a turn with neither.
		b.Browser = &BriefBrowser{CDPURL: browserCDPURL}
	}
	if j.playbook.Desktop {
		// The same tools, pointed at the desktop's own Chromium: the one the person sees.
		b.Browser = &BriefBrowser{CDPURL: desktopCDPURL}
		b.Desktop = true
	}
	for _, r := range j.playbook.Repos {
		b.Repos = append(b.Repos, BriefRepo{Name: r.Name, URL: r.URL, DefaultBranch: r.DefaultBranch})
	}
	// The playbook's persona, then the profile's. Absent when neither names one, which is
	// the runtime keeping its fallback rather than the conductor choosing an identity.
	if g := profile.GitFor(j.playbook); g.Set() {
		b.Git = &BriefGit{Name: g.Name, Email: g.Email}
	}
	for _, s := range bundles {
		b.Playbook.Skills = append(b.Playbook.Skills, BriefSkill{Name: s.Name, SHA256: s.SHA256})
		b.Playbook.SkillsDir = skills.ContainerDir
	}
	for _, srv := range servers {
		// resolveMCPServers has already refused a config that does not parse.
		config, _ := mcp.ParseConfig(srv.Config)
		entry := BriefMCPServer{Name: srv.Name, URL: srv.URL, Config: config}
		// A server with no stored credential is named without one. The env var is only
		// promised where a secret is actually attached below, because a brief naming a
		// variable the container does not have is a turn that fails on its harness config.
		if srv.TokenSecretVersion > 0 {
			entry.TokenEnv = mcp.TokenEnv(srv.Name)
		}
		b.Playbook.MCPServers = append(b.Playbook.MCPServers, entry)
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
//
// owner is whose credential the turn spends (profiles.Choice.CredentialOwner). It matters
// only where the kind of credential changes the endpoint: an OpenAI subscription token.
func (c *Conductor) providerFor(agent, owner string) *BriefProvider {
	b, ok := profiles.FindBackend(agent)
	if !ok {
		b, _ = profiles.FindBackend(profiles.DefaultAgent)
	}
	out := &BriefProvider{ID: b.Provider, APIKeyEnv: profiles.KeyEnvFor(b.Provider)}
	switch b.Provider {
	case profiles.ProviderXAI:
		// PODIUM_AGENT_XAI_BASE_URL names the host, not an API root: validateXAIKey builds
		// `<base>/v1/models` from the same value. The harness is handed a provider baseURL
		// and appends the endpoint to it directly, so it needs the version in the URL —
		// without it a turn dies on its first request with a 404 from
		// https://api.x.ai/responses.
		out.BaseURL = strings.TrimSuffix(c.xaiBaseURL, "/") + "/v1"
	case profiles.ProviderOpenAI:
		out.BaseURL = c.openaiEndpoint(owner)
	case profiles.ProviderGoogle:
		// Same rule as xAI: PODIUM_AGENT_GOOGLE_BASE_URL names the host, which is what
		// validateGoogleKey builds from, and the harness wants the API root with its version.
		out.BaseURL = strings.TrimSuffix(c.googleBaseURL, "/") + "/v1beta"
	}
	return out
}

// openaiEndpoint is where an OpenAI turn sends the harness. An API key talks to
// api.openai.com; a ChatGPT subscription token talks to Codex. The stored auth kind is
// what picks, and a missing row is the key path — that is also what a hand-built
// Conductor with no store produces.
func (c *Conductor) openaiEndpoint(owner string) string {
	api := strings.TrimSuffix(c.openaiBaseURL, "/") + "/v1"
	if c.authKind(profiles.ProviderOpenAI, owner) == "oauth" {
		return strings.TrimSuffix(c.openaiCodexBaseURL, "/")
	}
	return api
}

// authKind is how owner's credential for provider was obtained, "oauth" for a sign-in, as the
// settings row records it. A missing row is "", the key path.
func (c *Conductor) authKind(provider, owner string) string {
	if c.store == nil {
		return ""
	}
	var row struct {
		AuthKind string `json:"auth_kind"`
	}
	if err := c.store.GetSetting(context.Background(),
		profiles.ProviderSettingKey(provider, owner), &row); err != nil {
		return ""
	}
	return row.AuthKind
}

// claudeSignIn is whether owner's own Anthropic credential is a Claude subscription sign-in.
func (c *Conductor) claudeSignIn(owner string) bool {
	return owner != "" && c.authKind(profiles.ProviderAnthropic, owner) == "oauth"
}

// globalClaudeSignIn is whether an admin signed in a Claude subscription for everyone, beside
// the global Anthropic key. The settings row is what says so, as it is for claudeSignIn.
func (c *Conductor) globalClaudeSignIn() bool {
	if c.store == nil {
		return false
	}
	var row struct {
		AuthKind string `json:"auth_kind"`
	}
	if err := c.store.GetSetting(context.Background(), profiles.ClaudeSubscriptionSettingKey, &row); err != nil {
		return false
	}
	return row.AuthKind == "oauth"
}

// taskSpec is the task one turn runs. The secrets are exactly the playbook's, plus the
// reserved credential the conductor always adds: a playbook only ever gets the credentials its
// own file names, and the one the backend it runs on needs.
// placed is playbook with where its turn runs decided: its own runs_on, Modal for an image
// Modal holds, or the conductor's default when it names none. A default that cannot be read
// is the fleet's own machines.
func (c *Conductor) placed(ctx context.Context, playbook profiles.Playbook) profiles.Playbook {
	if playbook.RunsOn != "" {
		return playbook
	}
	// An image Modal holds runs only in a Modal sandbox, whatever the fleet's default is: the
	// spec would place it there anyway, and the brief's surface URL and the playbook's Modal
	// settings have to follow where it actually runs.
	if _, ok := spec.ModalImage(playbook.Image); ok {
		playbook.RunsOn = "modal"
		return playbook
	}
	if c.store == nil {
		return playbook
	}
	var row profiles.RunsOn
	if err := c.store.GetSetting(ctx, profiles.RunsOnKey, &row); err != nil && !errors.Is(err, store.ErrNotFound) {
		c.logger.WarnContext(ctx, "reading where turns run failed; using this fleet's own nodes", "error", err)
	}
	playbook.RunsOn = row.RunsOn
	return playbook
}

// runtimeCommand is the agent runtime's ENTRYPOINT, named again for a turn in a cloud sandbox.
// A container gets a task's command from its image when the spec names none, but a sandbox
// that runs the task as a process has no image config to read: Modal clears the ENTRYPOINT so
// the sandbox stays up, and keeps none Podium can see on an image built with Modal's SDK. Under
// Docker (a boat sandbox) a named command replaces the image's, so this is the same command
// either way. An image of your own built FROM the runtime keeps this path.
var runtimeCommand = []string{"node", "/opt/podium-agent/dist/main.js"}

// desktopCommand is a desktop playbook's: podium-desktop starts the desktop, waits until its
// browser and its stream answer, and execs the runtime, so the desktop lives as long as the
// turn. Set explicitly on every placement, because nothing else in the image would run it.
var desktopCommand = append([]string{"podium-desktop", "--"}, runtimeCommand...)

// desktopHold is how long a desktop turn's containers stay up after its command exits. A
// desktop needs its port published while the turn runs; expose is the only way to publish
// one, and expose always holds afterwards. Nothing is left to look at once the turn has
// gone — the desktop went with its command — so the hold is as short as a ttl can be.
const desktopHold = time.Second

// desktopCDPURL is where a desktop turn's browser tools find its Chromium: in the turn's own
// container, on loopback, where podium-desktop started it.
const desktopCDPURL = "http://127.0.0.1:9222"

func (c *Conductor) taskSpec(
	src Source, playbook profiles.Playbook, encodedBrief string, ev InboundEvent, bundles []skills.Bundle,
	servers []mcp.Server, choice profiles.Choice, capabilitySecret, login string,
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

	if playbook.Docker {
		env[profiles.DockerHostEnv] = dockerSidecarHost
	}

	// One read-only directory per skill, which is where the brief tells the runtime to
	// look. Files and not variables: an environment string is capped at 128 KiB.
	var mounts []spec.FileMount
	for _, b := range bundles {
		mounts = append(mounts, b.Mount())
	}

	s := &spec.TaskSpec{
		Image:     playbook.Image,
		Mounts:    mounts,
		Env:       env,
		Labels:    append([]string(nil), playbook.Labels...),
		Resources: playbook.Resources,
		Timeout:   playbook.Timeout,
		Secrets: append(append(append([]spec.SecretRef(nil), globalSecretRefs(playbook.Secrets)...),
			userSecretRefsOrEmpty(login, playbook.UserSecrets)...),
			c.reservedSecrets(choice, servers, capabilitySecret)...),
		MaxAttempts: 1,
		// A turn is not idempotent: it may already have posted a final. Running it twice
		// would say the same thing twice, so a lost node is surfaced to the human instead.
		RetryOnNodeLoss: false,
		Sandbox:         playbook.SandboxFor(playbook.RunsOn),
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
	if playbook.Expose != nil {
		x := *playbook.Expose
		x.Ports = maps.Clone(x.Ports)
		s.Expose = &x
	}
	if playbook.Desktop {
		if s.Expose == nil {
			s.Expose = &spec.Expose{TTL: spec.Duration(desktopHold)}
		}
		if s.Expose.Ports == nil {
			s.Expose.Ports = map[string]spec.ExposedPort{}
		}
		for name, port := range profiles.DesktopPorts {
			s.Expose.Ports[name] = spec.ExposedPort{Port: port}
		}
	}
	s.ApplyDefaults()
	switch {
	case playbook.Desktop:
		s.Command = slices.Clone(desktopCommand)
	case s.UsesSandbox():
		s.Command = slices.Clone(runtimeCommand)
	}
	return s
}

// skillBundles reads and packs the Agent Skills this turn's job names. A job that names none
// reads nothing: the skills directory does not have to exist, or be configured, for a bot
// that does not use skills.
func (c *Conductor) skillBundles(ctx context.Context, j job) ([]skills.Bundle, error) {
	if len(j.skills) == 0 {
		return nil, nil
	}
	bundles, err := c.skills().Bundles(ctx, j.skills)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", j.name, err)
	}
	return bundles, nil
}

// mcpServers resolves the playbook's mcp_servers list against the registry, in the order the
// playbook named them. A playbook that names none reads nothing, so a bot that does not use
// MCP needs no database for this.
//
// A name this turn cannot have — nobody registered it, this person has not connected it and
// there is no shared one, it is turned off, its sign-in was never finished — is handled
// differently for the two kinds of job:
//
//   - The ASSISTANT runs without it. It comes back in the second list, the brief carries it,
//     and the prompt tells the agent which servers it lacks and why, so the agent answers
//     "I have no access to Gmail" itself instead of the person getting a system error. That
//     is not a turn quietly running with fewer tools: the agent knows, and says so.
//   - A PLAYBOOK fails. It runs unattended in a container with a brief written for the tools
//     it declares, and a task that ran with fewer than that is the one outcome nobody can
//     diagnose afterwards — the same rule a missing skill follows. The error names the
//     server, which is the operator's own text and the part anybody can act on.
//
// Either kind fails on what nobody in the conversation can fix: a registry that cannot be
// read, or a saved config that no longer parses.
func (c *Conductor) mcpServers(ctx context.Context, j job, login string) ([]mcp.Server, []BriefUnavailableMCPServer, error) {
	if len(j.mcpServers) == 0 {
		return nil, nil, nil
	}
	if c.store == nil {
		return nil, nil, fmt.Errorf("this conductor has no database, so it has no MCP server registry")
	}
	rows, err := c.store.ListMcpServers(ctx)
	if err != nil {
		return nil, nil, err
	}
	servers, unavailable, err := resolveMCPServers(turnOwner(login), j.mcpServers, rows)
	if err != nil {
		return nil, nil, err
	}
	if err := requireMCPServers(j, unavailable); err != nil {
		return nil, nil, err
	}
	return servers, unavailable, nil
}

// requireMCPServers is the playbook's strictness: any server it names that this turn cannot
// have is an error. The assistant's turn runs without them, so it never fails here.
func requireMCPServers(j job, unavailable []BriefUnavailableMCPServer) error {
	if j.onHost || len(unavailable) == 0 {
		return nil
	}
	errs := make([]error, 0, len(unavailable))
	for _, u := range unavailable {
		errs = append(errs, fmt.Errorf("the MCP server %q is unavailable: %s", u.Name, u.Reason))
	}
	return errors.Join(errs...)
}

// logUnavailableMCP records the servers a turn runs without. The agent tells the person; the
// log is for the operator who wonders why a turn did not use a server it names.
func (c *Conductor) logUnavailableMCP(ctx context.Context, turnID, job string, unavailable []BriefUnavailableMCPServer) {
	for _, u := range unavailable {
		c.logger.InfoContext(ctx, "the turn runs without an mcp server it names",
			"turn_id", turnID, "job", job, "mcp_server", u.Name, "reason", u.Reason)
	}
}

// credentialOwner is whose model credential a turn spends: the person who asked, when they
// saved their own for the provider this backend spends, and the global one otherwise. A turn
// with no person — Slack, Linear, GitHub, the dev token — always spends the global one.
//
// A control plane that cannot be asked falls back to the global credential and says so: a
// turn that runs on the company key beats a turn that does not run, and the log is where an
// operator finds out it happened.
//
// onChatHost is whether the turn is answered on the chat host. A Claude subscription sign-in
// is spent only there, by opencode's claude-agent provider running Claude Code: every other
// turn runs a harness that would send the token as an API key, so it spends the global key.
func (c *Conductor) credentialOwner(ctx context.Context, login, agent string, onChatHost bool) string {
	owner := turnOwner(login)
	if owner == "" || c.podium == nil {
		return ""
	}
	b, ok := profiles.FindBackend(agent)
	if !ok {
		b, _ = profiles.FindBackend(profiles.DefaultAgent)
	}
	v, err := c.podium.PersonalSecretVersion(ctx, owner, profiles.PersonalSecretFor(b.Provider))
	if err != nil {
		c.logger.WarnContext(ctx, "could not check for a personal model credential; the turn "+
			"spends the global one", "login", owner, "provider", b.Provider, "error", err)
		return ""
	}
	if v == 0 {
		return ""
	}
	if b.Provider == profiles.ProviderAnthropic && c.claudeSignIn(owner) && (!onChatHost || !c.chatHostClaude) {
		c.logger.InfoContext(ctx, "a Claude subscription is only spent by the assistant on the chat host; "+
			"this turn spends the global key", "login", owner)
		return ""
	}
	return owner
}

// turnCredentialOwner is credentialOwner for a turn of one conversation: nobody's once the
// conversation is shared. A shared conversation has one context whoever speaks in it, so it
// spends one credential, the company's. Turns from different people then share a provider's
// prompt cache instead of each writing their own, and nobody's own key is spent by somebody
// else's question. Only the model key moves: the person asking still brings their own
// secrets, MCP servers and GitHub account.
func (c *Conductor) turnCredentialOwner(ctx context.Context, sess store.Session, login, agent string, onChatHost bool) string {
	if c.sharedConversation(ctx, sess) {
		return ""
	}
	return c.credentialOwner(ctx, login, agent, onChatHost)
}

// sharedConversation is whether a conversation is shared: a Slack thread or a pull request
// always is, and a web chat is once it is multiplayer (shared, or a second person spoke in
// it; store.Chat.Multiplayer, which never goes back). A chat whose record cannot be read is
// treated as shared: the company key is what every turn falls back to anyway, and spending
// a person's own key in a conversation they may have shared is the outcome to avoid.
func (c *Conductor) sharedConversation(ctx context.Context, sess store.Session) bool {
	switch sess.SourceKind {
	case SourceSlack, SourceGitHub:
		return true
	}
	if c.store == nil || sess.SourceKey == "" {
		return false
	}
	chat, err := c.store.ChatBySourceKey(ctx, sess.SourceKey)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		c.logger.WarnContext(ctx, "could not tell whether the conversation is shared; the turn "+
			"spends the global credential", "session_id", sess.ID, "error", err)
		return true
	}
	return chat.Multiplayer
}

// turnOwner is the MCP registry a turn reads. A Slack turn, the dev token, and the
// conductor itself have no person, so they keep the unowned bot rows.
func turnOwner(login string) string {
	switch login {
	case "", "unknown", "local", "agent":
		return ""
	default:
		return login
	}
}

// asker is the signed-in login a turn's personal secrets attach to. Slack, Linear, and
// GitHub have none: a mentioner's name is not a Podium login, and a mirrored chat's login
// is empty. A Slack speaker's email is a separate hint, used for MCP servers only.
func (c *Conductor) asker(ctx context.Context, src Source, sess store.Session) string {
	kind := sess.SourceKind
	if src != nil && src.Kind() != "" {
		kind = src.Kind()
	}
	switch kind {
	case SourceSlack, SourceLinear, SourceGitHub, KindDev:
		return ""
	}
	if c.store == nil || sess.SourceKey == "" {
		return ""
	}
	chat, err := c.store.ChatBySourceKey(ctx, sess.SourceKey)
	if err != nil {
		return ""
	}
	return chat.Login
}

// globalSecretRefs copies a playbook's company secrets and drops any owner. SecretRef
// unmarshals owner from YAML, so a secrets: entry can arrive with one. Company secrets
// stay global. Only user_secrets, stamped with the turn's login, are personal.
func globalSecretRefs(refs []spec.SecretRef) []spec.SecretRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]spec.SecretRef, len(refs))
	for i, ref := range refs {
		ref.Owner = ""
		out[i] = ref
	}
	return out
}

// userSecretRefs copies a playbook's personal secrets onto the person this turn is for.
// A turn with no person cannot name any. The owner is set here so CreateTask can tell a
// personal secret from a global one.
func userSecretRefs(login string, refs []spec.SecretRef) ([]spec.SecretRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if turnOwner(login) == "" {
		return nil, errors.New("there is no person for this turn, so it cannot use a personal secret")
	}
	out := make([]spec.SecretRef, len(refs))
	for i, ref := range refs {
		if strings.HasPrefix(ref.Name, "podium.agent.") || ref.Name == "podium.agent" ||
			spec.IsPersonalMCPSecret(ref.Name) || spec.IsPersonalMCPOAuthSecret(ref.Name) ||
			spec.IsPersonalModelKeySecret(ref.Name) {
			return nil, fmt.Errorf("personal secret %q is reserved for the conductor", ref.Name)
		}
		out[i] = ref
		out[i].Owner = login
	}
	return out, nil
}

// userSecretRefsOrEmpty is what a task spec attaches. A connected GitHub account is not
// among them: the turn redeems a capability for a token scoped to it instead.
func userSecretRefsOrEmpty(login string, refs []spec.SecretRef) []spec.SecretRef {
	out, err := userSecretRefs(login, refs)
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(out, func(ref spec.SecretRef) bool {
		return ref.Name == profiles.GitHubTokenSecret
	})
}

// resolveMCPServers picks the named servers in the order the playbook named them.
//
// An empty owner is the global list, which a turn with no person uses in full. A person
// uses their own row of that name. When they have none, a global row is used only if it
// is enabled and its fallback switch is on. A row they turned off is not replaced by the
// global one. Separated from the read so it can be asserted without a database.
//
// A name that does not resolve is returned as unavailable, with a reason written for the
// agent to pass on. Only a config that does not parse is an error.
func resolveMCPServers(owner string, names []string, rows []mcp.Server) ([]mcp.Server, []BriefUnavailableMCPServer, error) {
	personal := map[string]mcp.Server{}
	global := map[string]mcp.Server{}
	for _, r := range rows {
		switch {
		case r.Owner == "":
			global[r.Name] = r
		case owner != "" && strings.EqualFold(r.Owner, owner):
			if prev, ok := personal[r.Name]; ok && prev.Owner == owner {
				continue
			}
			personal[r.Name] = r
		}
	}
	out := make([]mcp.Server, 0, len(names))
	var unavailable []BriefUnavailableMCPServer
	for _, name := range names {
		srv, reason := pickMCPServer(owner, name, personal, global)
		if reason != "" {
			unavailable = append(unavailable, BriefUnavailableMCPServer{Name: name, Reason: reason})
			continue
		}
		if _, err := mcp.ParseConfig(srv.Config); err != nil {
			return nil, nil, fmt.Errorf("the MCP server %q: %w", name, err)
		}
		out = append(out, srv)
	}
	return out, unavailable, nil
}

// The reasons a named server is unavailable, as the agent is told them. They are written to
// be repeated to the person, so they say what that person can do about it.
const (
	mcpReasonUnregistered = "no MCP server by this name is registered on this Podium; an operator would have to add it"
	mcpReasonNotConnected = "the person in this conversation has not connected it, and there is no shared one " +
		"they can use; they can connect their own under MCP servers in Podium"
	mcpReasonOff       = "it is registered but turned off"
	mcpReasonPersonOff = "the person in this conversation turned their own connection to it off"
	mcpReasonSignIn    = "the person in this conversation started connecting it but never finished signing in; " +
		"they can sign in under MCP servers in Podium"
)

// pickMCPServer chooses one name, or says why nothing qualifies.
func pickMCPServer(owner, name string, personal, global map[string]mcp.Server) (mcp.Server, string) {
	if owner == "" {
		srv, ok := global[name]
		switch {
		case !ok:
			return mcp.Server{}, mcpReasonUnregistered
		case !srv.Enabled:
			return mcp.Server{}, mcpReasonOff
		}
		return srv, ""
	}
	if srv, ok := personal[name]; ok {
		switch {
		case !srv.Enabled:
			return mcp.Server{}, mcpReasonPersonOff
		case srv.Kind() == mcp.AuthOAuth && srv.TokenSecretVersion == 0:
			// A sign-in that never completed has nothing to present. Handing the harness a
			// server it cannot authenticate to fails the turn on its config instead.
			return mcp.Server{}, mcpReasonSignIn
		}
		return srv, ""
	}
	srv, ok := global[name]
	switch {
	case !ok || !srv.Fallback:
		return mcp.Server{}, mcpReasonNotConnected
	case !srv.Enabled:
		return mcp.Server{}, mcpReasonOff
	}
	return srv, ""
}

// skills is the library one turn resolves its names against: the directories under
// PODIUM_AGENT_SKILLS_DIR on this host.
func (c *Conductor) skills() skills.Library {
	return skills.Library{Dir: c.skillsDir}
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

	// Pinned by digest like every other base image Podium runs, and to the same 29.x the
	// -dev runtime image's CLI is built against. The digest is the multi-arch index, so it
	// resolves on amd64 and arm64 alike.
	dockerSidecarImage = "docker:29-dind@sha256:" +
		"1e08cdb63405ca788aea94ef35b792d1e299607c667d33d7bcbcae1fd2611ced"

	// dockerSidecarReady is generous because a loaded node can be slow to start dockerd.
	// A turn that waits two minutes for its daemon is still a turn; one that starts
	// without it fails on the agent's first docker command.
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
	browserSidecarImage = "chromedp/headless-shell:156.0.8078.12@sha256:" +
		"f70908c8020555a3bf89a070140913b443a03ead28c1a66563c3912a3b32b08d"

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
		Env: map[string]string{"DOCKER_TLS_CERTDIR": ""},
		// Docker 29 sleeps 15s before listening on plain TCP unless TLS is turned off
		// explicitly. The entrypoint prepends dockerd and its --host flags to a command
		// that starts with a dash.
		Command:        []string{"--tls=false"},
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
// none of them — it never leaves the conductor's host at all.
//
// The memory key is on every turn of a host that has memory, because a brief with a memory
// block whose api_key_env is unset is a failed turn (exit 2) — so that injection is not
// optional.
func (c *Conductor) reservedSecrets(
	choice profiles.Choice, servers []mcp.Server, capabilitySecret string,
) []spec.SecretRef {
	b, ok := profiles.FindBackend(choice.Agent)
	if !ok {
		b, _ = profiles.FindBackend(profiles.DefaultAgent)
	}
	// The person's own credential when the turn was decided to spend it, from their list;
	// the global one otherwise. Either way it lands in the same variable.
	model := spec.SecretRef{
		Name:   profiles.CredentialSecretFor(b.Provider, choice.CredentialOwner),
		Owner:  choice.CredentialOwner,
		Target: spec.SecretTargetEnv,
		Key:    profiles.KeyEnvFor(b.Provider),
	}
	refs := []spec.SecretRef{model}
	if c.memory != nil {
		refs = append(refs, spec.SecretRef{
			Name:   profiles.MemoryKeySecret,
			Target: spec.SecretTargetEnv,
			Key:    profiles.MemoryKeyEnv,
		})
	}
	// One secret per MCP server the playbook was granted, and only for the ones that have a
	// credential stored. The playbook cannot name these secrets itself — profiles refuses
	// the prefix — so being in this list is the whole of how a turn gets one.
	for _, srv := range servers {
		if srv.TokenSecretVersion == 0 {
			continue
		}
		ref := spec.SecretRef{
			Name:   mcp.CredentialSecret(srv),
			Target: spec.SecretTargetEnv,
			Key:    mcp.TokenEnv(srv.Name),
		}
		// A person's server is their secret. The owner is what makes CreateTask resolve
		// it from their list rather than the company list.
		if srv.Owner != "" {
			ref.Owner = srv.Owner
		}
		refs = append(refs, ref)
	}
	// This turn's authority to mint a GitHub token, when it has one. The playbook cannot
	// name this secret itself — profiles refuses the prefix — so being handed it here is
	// the whole of how a turn gets one.
	if capabilitySecret != "" {
		refs = append(refs, spec.SecretRef{
			Name:   capabilitySecret,
			Target: spec.SecretTargetEnv,
			Key:    GitTokenEnv,
		})
	}
	return refs
}

// failTurn records a turn that never got as far as a task, or one whose brief was
// impossible, and shows the failure on the triggering message.
func (c *Conductor) failTurn(
	ctx context.Context, src Source, sess store.Session, j job,
	turn store.Turn, ref string, started time.Time, status string,
) {
	if err := c.store.FinishTurn(ctx, turn.ID, status, nil, nil, ""); err != nil {
		c.logger.ErrorContext(ctx, "finishing a failed turn failed", "turn_id", turn.ID, "error", err)
	}
	c.finish(ctx, src, ref, ReactionFailed)
	c.forgetRoutineTurn(turn.ID)
	c.metrics.Turns.WithLabelValues(sess.SourceKind, j.name, status).Inc()
	c.metrics.TurnDuration.WithLabelValues(j.name).Observe(time.Since(started).Seconds())
}

// post says one thing and returns the message id, or "" when it could not be said. A
// source that cannot post is logged and the turn carries on: the work is more use to a
// human than the placeholder was.
func (c *Conductor) post(ctx context.Context, src Source, ref string, out Outbound) string {
	// An answer, a failure or a question in a conversation that lives elsewhere is recorded
	// and owed through the outbox (outbox.go), which also makes the first attempt.
	// A routine run's output is held until it ends (routine.go). Its answer or its failure
	// ends the hold; an answer from a run that reported idle is not said at all.
	if (out.Type == OutFinal || out.Type == OutFailure) && c.releaseRoutine(ctx, src, ref) && out.Type == OutFinal {
		return ""
	}
	if durableOut(out.Type) {
		if id, handled := c.postDurable(ctx, src, ref, out); handled {
			return id
		}
	}
	id, err := src.Post(ctx, ref, out)
	if err != nil {
		c.logger.WarnContext(ctx, "posting to the conversation failed",
			"ref", ref, "type", out.Type, "error", err)
		return ""
	}
	// Said out loud, so it belongs in the copy a reader sees.
	c.mirrorSaid(ctx, src, ref, out)
	return id
}

// finish shows the turn's outcome on the triggering message.
func (c *Conductor) finish(ctx context.Context, src Source, ref string, kind Reaction) {
	// A routine run that ended without an answer still ends its hold.
	c.releaseRoutine(ctx, src, ref)
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
	c.recoverWork(ctx, true)
	// Turns that were queued when this conductor (or another) stopped are still turns.
	c.adoptQueues(ctx)
}

// recoverWork resumes what was in flight. At startup that is this conductor's own work from
// before it died; later (adopt) it is work another conductor left when it died. Either way a
// piece of work is only taken when its lease can be: one another conductor is still renewing
// is that conductor's, and it is left alone.
func (c *Conductor) recoverWork(ctx context.Context, startup bool) {
	// Delegated tasks first: they run on a node and are still running right now, whereas
	// the turns below are being cleaned up after. See delegate.go.
	c.recoverDelegations(ctx, startup)

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

		c.mu.Lock()
		local := c.sessions[sess.ID] != nil && c.sessions[sess.ID].running
		c.mu.Unlock()
		if local {
			continue
		}
		if turn.TaskID == "" && !startup {
			// Nothing to follow, and its own conductor finishes or fails it.
			continue
		}
		if turn.TaskID == "" && !c.acquire(ctx, turnLease(turn.ID)) {
			// A host turn another conductor is running right now.
			continue
		}
		if turn.TaskID == "" {
			c.release(ctx, turnLease(turn.ID))
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

		if !c.acquire(ctx, store.SessionLease(sess.ID)) {
			// Another conductor serves this session, and follows this task.
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

		// A resumed turn always has a task, because the branch above has already finished
		// every turn that had none — a host turn included, since one dies with the process
		// that ran it. So the job here is always a playbook's, and the session's playbook is
		// always a real one.
		j := playbookJob(c.profiles.Current().Playbooks[sess.Playbook])
		run := &turnRun{
			sink:      &sink{c: c, src: src, ref: turn.TriggerRef, taskID: turn.TaskID, speaker: c.speakerFor(src, sess.Playbook)},
			sess:      sess,
			job:       j,
			turn:      turn,
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
			if ctx.Err() == nil {
				c.release(ctx, store.SessionLease(sessID))
			}
		}(run, sess.ID)
	}
}

// background is the context for work that outlives the call that started it. Before Run it
// is context.Background(), which is what a test constructing a Conductor by hand gets.
func (c *Conductor) background() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.baseCtx == nil {
		return context.Background()
	}
	return c.baseCtx
}

// firstLine is the first line of text, bounded, for a menu entry or an announcement. A
// system prompt and a model's instruction are both multi-line and both far longer than
// anything that belongs in one.
func firstLine(text string, limit int) string {
	s := strings.TrimSpace(text)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > limit {
		return strings.TrimSpace(string(r[:limit])) + "…"
	}
	return s
}

// sourceOf finds the source a session belongs to, or nil when it is not configured in this
// process (Slack tokens removed, say).
func (c *Conductor) sourceOf(kind string) Source {
	for _, src := range c.sourceList() {
		if src.Kind() == kind {
			return src
		}
	}
	return nil
}

func (c *Conductor) sourceList() []Source {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sources)
}

// ReplaceSource swaps the source of one kind for src, which is how a Slack pair or a GitHub
// App saved under Settings → Connections takes effect without a restart. Nil removes that
// kind. The caller stops the old source: its events channel closing is what ends its drain,
// and a turn already running keeps posting through it.
//
// A source added once Run has started is drained until its channel closes or the
// conductor's context ends. It is not part of Run's wait, which only covers the sources Run
// started with.
func (c *Conductor) ReplaceSource(kind string, src Source) {
	c.mu.Lock()
	c.sources = slices.DeleteFunc(c.sources, func(s Source) bool { return s.Kind() == kind })
	if src != nil {
		c.sources = append(c.sources, src)
		if ds, ok := src.(deliverySetter); ok {
			ds.SetDeliverer(c)
		}
	}
	ctx := c.baseCtx
	c.mu.Unlock()
	if src != nil && ctx != nil && ctx.Err() == nil {
		go c.drain(ctx, src)
	}
}

// githubApp is the GitHub App turns mint installation tokens from, nil when there is none.
func (c *Conductor) githubApp() *github.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.github
}

// SetGitHub swaps the GitHub App turns mint from. Nil turns it off: a running turn that asks
// for a token afterwards is told there is no App, as it would be after a restart.
func (c *Conductor) SetGitHub(gh *github.Client) error {
	if gh != nil && (c.mintSecret == "" || c.gitTaskURL == "") {
		return errors.New("conductor: a GitHub App needs a mint secret and the URL a task " +
			"container reaches this conductor on")
	}
	c.mu.Lock()
	c.github = gh
	c.mu.Unlock()
	return nil
}
