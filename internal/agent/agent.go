// Package agent wires the conductor together: its own store, a Podium API client, the
// profile directory, the sources, and one HTTP server for the AgentService, health and
// metrics. It imports nothing from internal/server or internal/node — podium-agent is an
// ordinary API client of the control plane.
package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/podium-ade/podium/internal/agent/api"
	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/ghreview"
	agentlinear "github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/memory"
	"github.com/podium-ade/podium/internal/agent/notify"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/routines"
	agentslack "github.com/podium-ade/podium/internal/agent/slack"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/webhooks"
	"github.com/podium-ade/podium/internal/netlisten"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// ShutdownTimeout is how long Run gives in-flight work to finish after a signal.
const ShutdownTimeout = 10 * time.Second

// readyTimeout bounds each dependency probe on /readyz.
const readyTimeout = 3 * time.Second

// reconcileInterval is how often the stored half of the profile is re-read.
//
// A write through the API swaps the profile in the same process immediately, so this is not
// how a change normally lands. It is the backstop: a second conductor on the same database,
// a row changed with psql, or a write whose own rebuild failed on a blip. Reading four rows
// on a timer costs nothing and is the difference between "the UI works" and "the UI works
// as long as one process is doing the writing".
const reconcileInterval = 15 * time.Second

// Agent is a configured, not-yet-listening conductor.
type Agent struct {
	cfg       config.Config
	logger    *slog.Logger
	store     *store.Store
	podium    *podium.Client
	profiles  *profiles.Live
	svc       *api.AgentService
	turns     *api.TurnService
	gitcred   *api.GitCredentialService
	conductor *conductor.Conductor
	// chatHost is the opencode 2 service conversations are answered on, when there is one.
	chatHost *chathost.Host
	live     *liveConnections
	// routines fires routines on a schedule (internal/agent/routines). Nil when this conductor
	// has no host runtime: a routine's run is a conversation, which only a host turn answers.
	routines *routines.Scheduler
	chat     *chat.Source
	dev      *api.DevSource
	memory   memory.Client
	http     *http.Server
	// surface is the optional second listener that serves the turn surface and nothing
	// else (PODIUM_AGENT_SURFACE_LISTEN). Nil when none is configured.
	surface *http.Server

	ln net.Listener
}

// New opens the store, migrates it, loads the profile and builds every handler. It does not
// bind a socket; call Run for that.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Agent, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if ignored := config.DeprecatedGitHubEnv(); len(ignored) > 0 {
		logger.Warn("GitHub App environment variables are ignored. Set the App under Settings → Connections.",
			"ignored", ignored)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agent config: %w", err)
	}

	files, err := profiles.Load(cfg.ProfileDir)
	if err != nil {
		return nil, err
	}
	live := profiles.NewLive(files)

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}

	// A saved Slack connection replaces the environment. The GitHub App and the Linear app
	// have no environment fallback: Settings → Connections is the only source, and a saved
	// row is what this process uses. A half-set Slack pair is still a startup error.
	envCfg := cfg
	pc := podium.New(cfg.Server, cfg.APIToken)
	savedSlack, savedGitHub, savedLinear, err := connections.Load(ctx, st, pc)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("agent: the saved connections' credentials are in podium-server's "+
			"secret store, which could not be read: %w", err)
	}
	cfg = connections.Overlay(cfg, savedSlack, savedGitHub, savedLinear)
	if savedSlack != nil || savedGitHub != nil || savedLinear != nil {
		if err := cfg.Validate(); err != nil {
			st.Close()
			return nil, fmt.Errorf("agent config: saved connection: %w", err)
		}
		logger.Info("connections: using a saved connection",
			"slack", savedSlack != nil, "github", savedGitHub != nil, "linear_app", savedLinear != nil)
	}

	// Apply stored overrides before anything reads the profile. A failure here is not
	// fatal: the files alone are a working bot, and starting on them beats refusing to
	// start because an override no longer applies.
	if _, err := api.ReloadProfile(ctx, st, live); err != nil {
		logger.WarnContext(ctx, "the stored profile overrides could not be applied; running the "+
			"profile directory alone. Fix it on Agent → Assistant", "error", err)
	}
	profile := live.Current()

	a := &Agent{
		cfg:      cfg,
		logger:   logger,
		store:    st,
		podium:   pc,
		profiles: live,
	}

	// The registry is built before the sources: the Linear sources share collectors of their
	// own and register them on this one, once.
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Slack, the GitHub review source and the GitHub App are the connections Settings can
	// change while the conductor runs. See live.go.
	a.live = &liveConnections{
		env:      envCfg,
		store:    st,
		vault:    pc,
		logger:   logger,
		webhooks: webhooks.New(logger),
		running:  cfg,
	}
	slackSrc, ghSrc, gh, err := a.live.build(cfg)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.live.slack, a.live.gh = slackSrc, ghSrc

	var sources []conductor.Source
	if slackSrc != nil {
		sources = append(sources, slackSrc)
	}
	// Linear is a saved agent app, which turns the poller off, or the poller on
	// PODIUM_AGENT_LINEAR_API_KEY, or neither. See buildLinear.
	a.live.linearPlaybook = func() string { return live.Current().LinearPlaybook() }
	a.live.linearMetrics = agentlinear.NewMetrics(registry)
	// The poller asks rather than reading the store itself: an issue with no session is a
	// fresh assignment, and one with a session is a follow-up whose new comments are the
	// ones after the last turn started.
	a.live.linearSession = func(ctx context.Context, sourceKey string) (time.Time, bool) {
		sess, err := st.GetSessionByKey(ctx, sourceKey)
		if err != nil {
			return time.Time{}, false
		}
		if sess.LastTurnAt != nil {
			return *sess.LastTurnAt, true
		}
		return time.Time{}, true
	}
	a.live.linearCursor = agentlinear.CursorStore{
		Get: func(ctx context.Context) (time.Time, error) {
			at, err := st.GetLinearCursor(ctx, store.LinearCursorKey)
			if errors.Is(err, store.ErrNotFound) {
				return time.Time{}, nil
			}
			return at, err
		},
		Put: func(ctx context.Context, at time.Time) error {
			return st.PutLinearCursor(ctx, store.LinearCursorKey, at)
		},
	}
	linApp, linPoll, err := a.live.buildLinear(cfg, savedLinear)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.live.linearApp, a.live.linearPoll, a.live.linearRow = linApp, linPoll, savedLinear
	if src := a.live.linearSource(); src != nil {
		sources = append(sources, src)
	}
	if cfg.LinearAppEnabled() && cfg.LinearEnabled() {
		logger.Info("linear: the saved agent app is in use, so PODIUM_AGENT_LINEAR_API_KEY is " +
			"not polled. Remove the app under Settings → Connections to poll again")
	}
	if ghSrc != nil {
		sources = append(sources, ghSrc)
	}
	// The web chat is always on: it needs no credential and no external service, and the
	// UI's Chat tab is only as good as the source behind it.
	a.chat, err = chat.New(chat.Options{
		Store:       st,
		DisplayName: profile.DisplayName,
		UIURL:       cfg.WebURL(),
		Logger:      logger,
		// Live frames reach a watcher whichever conductor it is connected to.
		Bus: st,
	})
	if err != nil {
		st.Close()
		return nil, err
	}
	sources = append(sources, a.chat)

	if cfg.DevSource {
		logger.Warn("DEV SOURCE ENABLED — TEST ONLY. /dev/inbound injects messages as if a human " +
			"had sent them and /dev/outbound reports everything this bot says. Never set " +
			"PODIUM_AGENT_DEV_SOURCE outside a test.")
		a.dev = api.NewDevSource()
		sources = append(sources, a.dev)
	}

	// Shared memory. Optional: with no URL the briefs carry no memory block, nothing is
	// retained, readyz does not probe it and the Memory tab says it is not configured.
	var briefMemory *conductor.BriefMemory
	var memorySandboxMCPURL string
	if cfg.MemoryEnabled() {
		a.memory, err = memory.New(memory.Options{
			BaseURL: cfg.MemoryURL,
			Bank:    cfg.MemoryBank,
			APIKey:  cfg.MemoryAPIKey,
		})
		if err != nil {
			st.Close()
			return nil, err
		}
		briefMemory = &conductor.BriefMemory{
			// The TASK URL, not this process's: a turn runs in a container on a node and
			// reaches the memory service from there.
			MCPURL:    memory.MCPURL(cfg.MemoryTaskURL, cfg.MemoryBank),
			APIKeyEnv: profiles.MemoryKeyEnv,
		}
		if cfg.MemorySandboxURL != "" {
			// The same memory as a cloud sandbox reaches it; the conductor briefs a turn
			// placed on a provider with it. See conductor.Options.MemorySandboxMCPURL.
			memorySandboxMCPURL = memory.MCPURL(cfg.MemorySandboxURL, cfg.MemoryBank)
		}
		logger.Info("memory: enabled", "bank", cfg.MemoryBank,
			"mcp_url", briefMemory.MCPURL, "api_key_env", briefMemory.APIKeyEnv)
	} else {
		logger.Info("memory: not configured; turns will run without one. " +
			"Set PODIUM_AGENT_MEMORY_URL and PODIUM_AGENT_MEMORY_API_KEY to enable it.")
	}

	// The host runtime, when this host has one. It is built before the conductor and reads
	// the credential through a.svc, which is built after it: the closure resolves when a
	// turn asks, not now.
	var host *conductor.HostRuntime
	if cfg.HostRuntime != "" {
		host = &conductor.HostRuntime{
			Node:     cfg.HostNode,
			Entry:    cfg.HostRuntime,
			Runner:   cfg.RunnerBin,
			StateDir: cfg.HostDir,
			Credential: func(ctx context.Context, provider string) (string, error) {
				return a.svc.HostCredential(ctx, provider)
			},
			PersonalCredential: func(ctx context.Context, login, provider string) (string, error) {
				return a.svc.PersonalHostCredential(ctx, login, provider)
			},
			TurnURL:   turnURL(cfg.Listen),
			Isolation: cfg.HostIsolation,
			Bwrap:     cfg.HostBwrap,
			MemoryMax: cfg.HostMemoryMax,
			Warm:      cfg.HostWarm,
		}
		if host.Isolation == "none" {
			host.Isolation = conductor.IsolationNone
		}
		if host.TurnURL != "" {
			host.PullRequestMCPURL = host.TurnURL + api.PullRequestMCPRESTPath
			host.GitHubReadMCPURL = host.TurnURL + api.GitHubReadMCPRESTPath
		}
		if cfg.MemoryEnabled() {
			// The memory server as THIS PROCESS reaches it. A task is told
			// cfg.MemoryTaskURL, which is an address that resolves in a container.
			host.MemoryMCPURL = memory.MCPURL(cfg.MemoryURL, cfg.MemoryBank)
			host.MemoryAPIKey = cfg.MemoryAPIKey
		}
		logger.Info("host turns: enabled — a conversation is answered by this process and "+
			"delegates to a task when it needs a container",
			"runtime", cfg.HostRuntime, "node", cfg.HostNode, "runner", cfg.RunnerBin,
			"isolation", cmp.Or(host.Isolation, "none"), "memory_max", cmp.Or(host.MemoryMax, "none"))
		if host.Isolation == conductor.IsolationNone {
			logger.Warn("host turns: not isolated — every turn runs as this user and can read " +
				"another turn's environment. Set PODIUM_AGENT_HOST_ISOLATION=bwrap where more than " +
				"one person talks to this conductor.")
		}
	} else {
		logger.Info("host turns: not configured; every turn runs as a task. " +
			"Set PODIUM_AGENT_HOST_RUNTIME and PODIUM_AGENT_RUNNER_BIN to answer " +
			"conversations on this host.")
	}

	// Who to tell, outside the conversation, that a task they asked for has ended: a Slack
	// direct message while Slack is connected, then email when SMTP is set.
	notifiers := []conductor.Notifier{notify.Slack{Current: func() *agentslack.Source {
		if a.live == nil {
			return nil
		}
		return a.live.slackSource()
	}}}
	if cfg.SMTPURL != "" {
		email := notify.Email{URL: cfg.SMTPURL, From: cfg.SMTPFrom}
		if err := email.Validate(); err != nil {
			st.Close()
			return nil, err
		}
		notifiers = append(notifiers, email)
	}

	var chatHostOpt conductor.ChatHost
	if cfg.ChatHost != "" && host != nil {
		claude := resolveBinary(cfg.ChatHostClaude)
		var fenceExtra []string
		if claude != "" {
			fenceExtra = append(fenceExtra, claude)
		}
		a.chatHost, err = chathost.New(chathost.Options{
			Binary: cfg.ChatHost,
			Claude: claude,
			Dir:    filepath.Join(cmp.Or(cfg.HostDir, os.TempDir()), "podium-chat-host"),
			Wrap:   host.FenceService(fenceExtra...),
			Logger: logger,
		})
		if err != nil {
			st.Close()
			return nil, err
		}
		chatHostOpt = a.chatHost
		logger.Info("chat host: enabled — conversations are answered on one opencode service",
			"binary", cfg.ChatHost, "isolation", cmp.Or(host.Isolation, "none"),
			"claude", cmp.Or(claude, "off"))
	}

	if gh != nil {
		logger.Info("github app configured: turns will mint their own installation tokens",
			"app_id", a.cfg.GitHubAppID, "task_url", a.cfg.TaskURL)
	}

	a.conductor, err = conductor.New(conductor.Options{
		Store:                 st,
		Podium:                a.podium,
		Profiles:              live,
		Sources:               sources,
		Metrics:               conductor.NewMetrics(registry),
		Logger:                logger,
		Memory:                briefMemory,
		MemorySandboxMCPURL:   memorySandboxMCPURL,
		MemoryClient:          a.memory,
		XAIBaseURL:            a.cfg.XAIBaseURL,
		OpenAIBaseURL:         a.cfg.OpenAIBaseURL,
		OpenAICodexBaseURL:    a.cfg.OpenAICodexBaseURL,
		GoogleBaseURL:         a.cfg.GoogleBaseURL,
		SkillsDir:             a.cfg.SkillsDir,
		Host:                  host,
		HostMaxTurns:          a.cfg.HostMaxTurns,
		AllowedDomains:        a.cfg.AllowedDomains,
		TranscriptBudgetBytes: a.cfg.TranscriptBudgetBytes,
		TranscriptKeepRecent:  a.cfg.TranscriptKeepRecent,
		Mirror:                a.chat,
		GitHub:                gh,
		GitTaskURL:            a.cfg.TaskURL,
		// The conductor's own bearer, as the seed for the key a turn's capability is
		// signed with. Derived and not generated so a capability survives a restart, as
		// the turn holding it does. See conductor/gitcred.go.
		MintSecret: a.cfg.Token,
		// This conductor among the others on its database, and how a playbook placed on a
		// sandbox provider reaches back here. See conductor/lease.go and surface.go.
		ReplicaID:          a.cfg.Replica(),
		SandboxTaskURL:     a.cfg.SandboxURL,
		ChatHost:           chatHostOpt,
		ChatHostURL:        turnURL(a.cfg.Listen),
		ChatHostRender:     renderPrompt(a.cfg.HostNode, a.cfg.HostRuntime),
		AnthropicBaseURL:   a.cfg.AnthropicBaseURL,
		ChatHostClaude:     a.cfg.ChatHostClaude != "",
		ChatHostMaxTurns:   a.cfg.ChatHostMaxTurns,
		ChatHostMaxPerUser: a.cfg.ChatHostMaxPerUser,
		Notifiers:          notifiers,
		NotifyAwayAfter:    a.cfg.NotifyAway,
		PublicURL:          a.cfg.PublicURL,
		Keys:               keyHealth{a},
	})
	if err != nil {
		st.Close()
		return nil, err
	}

	var slackDir api.SlackDirectory
	if slackSrc != nil {
		slackDir = slackSrc
	}
	a.svc = api.NewAgentService(api.AgentServiceOptions{
		Store:                      a.store,
		Env:                        envCfg,
		Running:                    a.cfg,
		Secrets:                    a.podium,
		Model:                      profile.Model,
		AnthropicBaseURL:           a.cfg.AnthropicBaseURL,
		XAIBaseURL:                 a.cfg.XAIBaseURL,
		XAIOAuthIssuer:             a.cfg.XAIOAuthIssuer,
		XAIOAuthClientID:           a.cfg.XAIOAuthClientID,
		XAIOAuthScopes:             a.cfg.XAIOAuthScopes,
		OpenAIBaseURL:              a.cfg.OpenAIBaseURL,
		OpenAICodexBaseURL:         a.cfg.OpenAICodexBaseURL,
		OpenAIOAuthIssuer:          a.cfg.OpenAIOAuthIssuer,
		OpenAIOAuthClientID:        a.cfg.OpenAIOAuthClientID,
		GoogleBaseURL:              a.cfg.GoogleBaseURL,
		AnthropicOAuthClientID:     a.cfg.AnthropicOAuthClientID,
		AnthropicOAuthAuthorizeURL: a.cfg.AnthropicOAuthAuthorizeURL,
		AnthropicOAuthTokenURL:     a.cfg.AnthropicOAuthTokenURL,
		AnthropicOAuthRedirectURI:  a.cfg.AnthropicOAuthRedirectURI,
		AnthropicOAuthScopes:       a.cfg.AnthropicOAuthScopes,
		ClaudeAgent:                a.chatHost != nil && a.cfg.ChatHostClaude != "",
		Memory:                     a.memory,
		SkillsDir:                  a.cfg.SkillsDir,
		Profiles:                   live,
		ProfileDir:                 a.cfg.ProfileDir,
		Chat:                       a.chat,
		Tasks:                      a.podium,
		Turns:                      a.conductor,
		Deliveries:                 a.conductor,
		Canceller:                  a.conductor,
		Auditor:                    a.podium,
		Slack:                      slackDir,
		Connections:                a.live,
		Logger:                     a.logger,
	})
	a.live.conductor, a.live.svc = a.conductor, a.svc
	// The person who saved a model key is told when the provider stops accepting it, the
	// same way a person is told their task ended: Slack, then email.
	a.svc.SetKeyAlert(func(ctx context.Context, login, subject, body string) error {
		var errs []error
		for _, n := range notifiers {
			err := n.Notify(ctx, login, subject, body)
			if err == nil {
				return nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", n.Name(), err))
		}
		return errors.Join(errs...)
	})
	// Routines run in their chats, so they need the web chat and a host runtime to answer it.
	conductor.DescribeSchedule = routines.Describe
	if a.chat != nil && host != nil {
		a.routines, err = routines.New(routines.Options{Store: st, Runner: a.chat, Logger: logger})
		if err != nil {
			st.Close()
			return nil, err
		}
		a.svc.SetRoutines(a.routines)
		// Only where routines run may an assistant set one up for the person it talks to.
		a.conductor.SetRoutineCreator(a.svc)
	}
	// The turn surface. It is the conductor itself, and it is always mounted: a turn on the
	// host delegates through here, and a playbook's task anywhere redeems its git capability
	// here. Every call is authenticated by a live turn's own token or a signed capability, so
	// a conductor with neither has a surface that refuses everything.
	a.turns = api.NewTurnService(a.conductor, logger)
	// The minting surface, which unlike the turn surface is reached from a TASK. It serves
	// the App's installation tokens and the scoped tokens of a turn that pushes as the
	// person who asked, which needs no App private key, so it is always mounted.
	a.gitcred = api.NewGitCredentialService(a.conductor, logger)
	a.http = &http.Server{
		Handler:           a.mux(registry),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.SurfaceListen != "" {
		a.surface = &http.Server{
			Handler:           a.surfaceMux(),
			ReadHeaderTimeout: 10 * time.Second,
		}
	}

	kinds := make([]string, 0, len(sources))
	for _, src := range sources {
		kinds = append(kinds, src.Kind())
	}
	logger.Info("conductor configured", "config", cfg,
		"profile", profile.Name, "playbooks", profile.PlaybookNames(),
		"assistant_skills", profile.Assistant().Skills, "sources", kinds)
	if !cfg.SlackEnabled() && !cfg.LinearEnabled() && !cfg.LinearAppEnabled() && !cfg.GitHubSourceEnabled() {
		logger.Info("no Slack, Linear or GitHub review source: the web chat at /agent/chat is the only " +
			"way to start a turn. Set both PODIUM_AGENT_SLACK_APP_TOKEN and " +
			"PODIUM_AGENT_SLACK_BOT_TOKEN, or save a GitHub App or a Linear app under " +
			"Settings → Connections.")
	}
	return a, nil
}

// Close releases the store. It is safe to call on a partially built Agent.
func (a *Agent) Close() {
	if a.store != nil {
		a.store.Close()
	}
}

// mux routes the AgentService and the dev routes behind the bearer and leaves the
// operational endpoints open: a probe that needs a token reports the wrong thing.
func (a *Agent) mux(registry *prometheus.Registry) http.Handler {
	opts := []connect.HandlerOption{}
	rpc := http.NewServeMux()
	rpc.Handle(agentv1connect.NewAgentServiceHandler(a.svc, opts...))

	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	root.HandleFunc("/readyz", a.readyz)
	root.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	root.Handle("/"+agentv1connect.AgentServiceName+"/",
		api.RequireBearer(a.cfg.Token, api.RecordWebIdentities(a.store, a.logger, rpc)))
	a.mountSurface(root)
	if a.chatHost != nil {
		// The chat host's model gateway and tools. Each request names a conversation by a
		// token only that conversation's location holds, and is refused outside its turns.
		root.Handle("/chat-host/gateway/", a.conductor.ChatHostGateway())
		var pr http.Handler
		if a.live != nil {
			pr = ghreview.MCPHandler(func(token string) (string, bool) {
				return a.conductor.TurnPullRequest(context.Background(), token)
			}, a.live.githubSource)
		}
		root.Handle("/chat-host/mcp/", a.conductor.ChatHostMCP(pr))
	}
	if a.dev != nil {
		dev := api.RequireBearer(a.cfg.Token, a.dev.Handler())
		root.Handle(api.DevInboundPath, dev)
		root.Handle(api.DevOutboundPath, dev)
	}
	return root
}

// mountSurface puts the TURN SURFACE on mux: everything a running turn calls back into this
// conductor for, and nothing else.
//
//   - TurnService: delegate, check on a delegation, inject into one. A live turn's token.
//   - the pull-request MCP server: the diff and files of the one pull request that turn is
//     answering, read-only. The same token.
//   - GitCredentialService: mint a GitHub token. The signed capability a task carries,
//     naming one turn and the repositories its playbook listed.
//
// None of it is behind RequireBearer, and that is the point: a turn holds a credential of its
// own, minted for it and checked by the conductor, and it must never be given the operator's.
// podium-server proxies AgentService only. A turn reaches this from the host over loopback,
// from a node at PODIUM_AGENT_TASK_URL, and from a sandbox at PODIUM_AGENT_SANDBOX_URL — which
// is why surfaceMux exists: it is these routes alone, so the listener that faces sandboxes
// carries no operator API at all.
//
// It is served twice over: as HTTP resources under /v1/ (api.NewTurnREST), which is what this
// runtime calls, and as the Connect procedures runtimes built before that still call.
func (a *Agent) mountSurface(mux *http.ServeMux) {
	opts := []connect.HandlerOption{}
	var prMCP http.Handler
	if a.turns != nil {
		turnPath, turnHandler := agentv1connect.NewTurnServiceHandler(a.turns, opts...)
		mux.Handle(turnPath, turnHandler)
		prMCP = ghreview.MCPHandler(func(token string) (string, bool) {
			return a.conductor.TurnPullRequest(context.Background(), token)
		}, a.live.githubSource)
		mux.Handle(ghreview.MCPPath, prMCP)
	}
	if a.gitcred != nil {
		gitPath, gitHandler := agentv1connect.NewGitCredentialServiceHandler(a.gitcred, opts...)
		mux.Handle(gitPath, gitHandler)
	}
	if a.turns != nil {
		// The GitHub reader a routine's turn gets, read as the routine's owner.
		mux.Handle(api.GitHubReadMCPRESTPath, a.conductor.GitHubReadMCP())
	}
	if a.turns != nil || a.gitcred != nil {
		mux.Handle(api.TurnRESTPrefix, api.NewTurnREST(a.turns, a.gitcred, prMCP))
	}
}

// surfaceMux is the turn surface and a health probe, for PODIUM_AGENT_SURFACE_LISTEN.
func (a *Agent) surfaceMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	a.mountSurface(mux)
	return mux
}

// readyz reports whether the dependencies the conductor cannot work without are reachable:
// its own database, the Podium API, and the shared memory when one is configured.
func (a *Agent) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.logger.WarnContext(ctx, "readyz: the agent database is unreachable", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "agent database unreachable")
		return
	}
	who, err := a.podium.WhoAmI(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "readyz: the Podium API is unreachable", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "podium api unreachable")
		return
	}
	// The memory service is a dependency of every turn's prompt, so a conductor that
	// cannot reach it is not ready — but a turn still runs and finishes without it, which
	// is why this is last and why nothing else in the process treats it as fatal.
	if a.memory != nil {
		if err := a.memory.Ready(ctx); err != nil {
			a.logger.WarnContext(ctx, "readyz: the shared memory is unreachable", "error", err)
			writePlain(w, http.StatusServiceUnavailable, "memory unreachable")
			return
		}
	}
	writePlain(w, http.StatusOK, "ok, podium as "+who.GetLogin())
}

// reconcileProfile re-reads the stored half of the profile on a timer. See
// reconcileInterval: the write path already swaps the profile in this process, so this only
// catches a change this process did not make.
//
// A failure keeps the profile that is running. A conductor that has been serving turns for
// an hour must not lose its playbooks because Postgres blinked, and the reason is reported on
// the Playbooks screen either way.
func (a *Agent) reconcileProfile(ctx context.Context) {
	tick := time.NewTicker(reconcileInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := a.svc.Reconcile(ctx); err != nil && ctx.Err() == nil {
				a.logger.WarnContext(ctx, "rebuilding the profile from the database failed; "+
					"the conductor is still running the last one that loaded", "error", err)
			}
		}
	}
}

// turnURL is this listener as a host turn's own child process reaches it. A listener bound
// to every interface (":8090", "0.0.0.0:8090") is still reached over loopback from here:
// the turn runs on this machine, and its token is not something to send over a network.
func turnURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body + "\n"))
}

// publishMemoryKey copies the memory API key from this process's environment into Podium's
// secret store, so an operator configures it in exactly one place (.env) and every turn's
// container still receives it as an ordinary Podium secret.
//
// It writes on every start rather than only when the secret is missing, because .env is the
// authoritative copy: an operator who rotates the key and restarts the conductor must have
// the new one reach the next turn, and there is no endpoint that could read the stored value
// back to compare. The cost is a new secret version per restart, visible in
// `podium secret ls` and in the control plane's audit log.
//
// A failure here is not fatal. The control plane may still be starting, and the consequence
// is a turn that fails admission with "this bot is missing a credential" — recoverable by a
// restart, or by the operator running the equivalent by hand.
func (a *Agent) publishMemoryKey(ctx context.Context) {
	if !a.cfg.MemoryEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	version, err := a.podium.SetSecret(ctx, profiles.MemoryKeySecret, []byte(a.cfg.MemoryAPIKey))
	if err != nil {
		a.logger.ErrorContext(ctx, "storing the memory API key as a Podium secret failed; "+
			"turns will fail admission until it exists. Run "+
			"`podium secret set "+profiles.MemoryKeySecret+"` with the same value, or restart "+
			"this process once the control plane is up",
			"secret", profiles.MemoryKeySecret, "error", err)
		return
	}
	a.logger.InfoContext(ctx, "the memory API key is stored as a Podium secret",
		"secret", profiles.MemoryKeySecret, "version", version)
}

// Addr is the address the API is listening on, once Run has bound it.
func (a *Agent) Addr() string {
	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// Run binds the listener, starts the sources and the turn loop, and blocks until ctx is
// cancelled.
func (a *Agent) Run(ctx context.Context) error {
	ln, err := netlisten.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Listen, err)
	}
	a.ln = ln
	a.logger.InfoContext(ctx, "conductor listening", "addr", ln.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.publishMemoryKey(runCtx)
	go a.reconcileProfile(runCtx)
	// Before the refresh pass, so it reads from the secret store rather than from rows a
	// database written before the move still holds in clear.
	a.svc.MoveCredentialsIntoStore(runCtx)
	// A subscription access token lives about an hour and a turn can run for half of one,
	// so nothing but this keeps a signed-in provider working past its first hour.
	go a.svc.RefreshTokens(runCtx)

	go func() {
		if err := a.chat.RunRelay(runCtx); err != nil {
			a.logger.ErrorContext(runCtx, "the chat relay stopped", "error", err)
		}
	}()
	if a.chatHost != nil {
		go func() {
			if err := a.chatHost.Run(runCtx); err != nil {
				a.logger.ErrorContext(runCtx, "the chat host stopped for good", "error", err)
			}
		}()
	}

	serveErr := make(chan error, 2)
	go func() {
		if err := a.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	if a.surface != nil {
		sln, err := netlisten.Listen("tcp", a.cfg.SurfaceListen)
		if err != nil {
			_ = a.http.Close()
			return fmt.Errorf("listen on %s (PODIUM_AGENT_SURFACE_LISTEN): %w", a.cfg.SurfaceListen, err)
		}
		a.logger.InfoContext(ctx, "turn surface listening", "addr", sln.Addr().String(),
			"sandbox_url", a.cfg.SandboxURL)
		go func() {
			if err := a.surface.Serve(sln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("turn surface: %w", err)
			}
		}()
	}

	// A source's Run returning non-nil is fatal: a bot that cannot reach Slack or Linear
	// is a bot nobody can talk to, and failing loudly at boot is how a wrong credential is
	// found in the first minute rather than the first day.
	sourceErr := make(chan error, 3)
	a.live.start(runCtx, sourceErr)
	if a.routines != nil {
		// One conductor fires routines: every one that did would fire each of them twice, and
		// the run rows' unique key would turn the second into nothing but noise.
		go func() { _ = a.conductor.WhileLeader(runCtx, "source:routines", a.routines.Run) }()
	}
	conductorDone := make(chan struct{})
	go func() {
		defer close(conductorDone)
		if err := a.conductor.Run(runCtx); err != nil {
			a.logger.ErrorContext(runCtx, "the turn loop stopped", "error", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErr:
		if runErr != nil {
			runErr = fmt.Errorf("serve: %w", runErr)
		}
	case err := <-sourceErr:
		if err != nil {
			runErr = err
		}
	}

	cancel()
	if a.dev != nil {
		a.dev.Close()
	}
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer stop()
	if err := a.http.Shutdown(shutdownCtx); err != nil {
		a.logger.WarnContext(ctx, "shutting the API down failed", "error", err)
	}
	if a.surface != nil {
		if err := a.surface.Shutdown(shutdownCtx); err != nil {
			a.logger.WarnContext(ctx, "shutting the turn surface down failed", "error", err)
		}
	}
	select {
	case <-conductorDone:
	case <-shutdownCtx.Done():
		a.logger.WarnContext(ctx, "a turn was still in flight at shutdown; it will be resumed on the next start")
	}
	return runErr
}

// renderPrompt renders a chat-host turn's system prompt with the runtime beside entry: node
// runs its dist/render.js with the brief in PODIUM_AGENT_TURN, and prints {"system": ...}.
func renderPrompt(node, entry string) func(ctx context.Context, encoded string) (string, error) {
	if entry == "" {
		return nil
	}
	script := filepath.Join(filepath.Dir(entry), "render.js")
	return func(ctx context.Context, encoded string) (string, error) {
		cmd := exec.CommandContext(ctx, cmp.Or(node, "node"), script) //nolint:gosec // the configured runtime
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), conductor.BriefEnv + "=" + encoded}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("render the prompt: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		var doc struct {
			System string `json:"system"`
		}
		if err := json.Unmarshal(out, &doc); err != nil || doc.System == "" {
			return "", fmt.Errorf("render the prompt: not a prompt: %w", err)
		}
		return doc.System, nil
	}
}

// resolveBinary is bin as an absolute path with its symlinks followed, or "" when it is empty
// or not found. Claude Code's native install is a symlink into a versions directory, and a
// fenced service sees only the directory the link resolves into.
func resolveBinary(bin string) string {
	if bin == "" {
		return ""
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// keyHealth is the conductor's view of the stored model keys. The service that knows is
// built after the conductor, so this resolves it when a turn asks rather than now.
type keyHealth struct{ a *Agent }

func (k keyHealth) KeyRefused(ctx context.Context, provider string) (bool, error) {
	if k.a.svc == nil {
		return false, nil
	}
	return k.a.svc.KeyRefused(ctx, provider)
}

func (k keyHealth) SuspectKey(provider string) {
	if k.a.svc != nil {
		k.a.svc.SuspectKey(provider)
	}
}
