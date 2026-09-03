// Package agent wires the conductor together: its own store, a Podium API client, the
// profile directory, the sources, and one HTTP server for the AgentService, health and
// metrics. It imports nothing from internal/server or internal/node — podium-agent is an
// ordinary API client of the control plane.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alvaroibarguen/podium/internal/agent/api"
	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/config"
	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/podium"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	agentslack "github.com/alvaroibarguen/podium/internal/agent/slack"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	"github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// ShutdownTimeout is how long Run gives in-flight work to finish after a signal.
const ShutdownTimeout = 10 * time.Second

// readyTimeout bounds each dependency probe on /readyz.
const readyTimeout = 3 * time.Second

// Agent is a configured, not-yet-listening conductor.
type Agent struct {
	cfg       config.Config
	logger    *slog.Logger
	store     *store.Store
	podium    *podium.Client
	profile   *profiles.Profile
	conductor *conductor.Conductor
	slack     *agentslack.Source
	dev       *api.DevSource
	memory    memory.Client
	http      *http.Server

	ln net.Listener
}

// New opens the store, migrates it, loads the profile and builds every handler. It does not
// bind a socket; call Run for that.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Agent, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agent config: %w", err)
	}

	profile, err := profiles.Load(cfg.ProfileDir)
	if err != nil {
		return nil, err
	}

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}

	a := &Agent{
		cfg:     cfg,
		logger:  logger,
		store:   st,
		podium:  podium.New(cfg.Server, cfg.APIToken),
		profile: profile,
	}

	var sources []conductor.Source
	if cfg.SlackEnabled() {
		a.slack, err = agentslack.New(agentslack.Options{
			AppToken: cfg.SlackAppToken,
			BotToken: cfg.SlackBotToken,
			Logger:   logger,
			// A reply in a thread the bot is already in continues the conversation without
			// a mention. The source asks rather than reading the store itself.
			KnownSession: func(ctx context.Context, sourceKey string) bool {
				_, err := st.GetSessionByKey(ctx, sourceKey)
				return err == nil
			},
		})
		if err != nil {
			st.Close()
			return nil, err
		}
		sources = append(sources, a.slack)
	}
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
		logger.Info("memory: enabled", "bank", cfg.MemoryBank,
			"mcp_url", briefMemory.MCPURL, "api_key_env", briefMemory.APIKeyEnv)
	} else {
		logger.Info("memory: not configured; turns will run without one. " +
			"Set PODIUM_AGENT_MEMORY_URL and PODIUM_AGENT_MEMORY_API_KEY to enable it.")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	a.conductor, err = conductor.New(conductor.Options{
		Store:        st,
		Podium:       a.podium,
		Profile:      profile,
		Sources:      sources,
		Metrics:      conductor.NewMetrics(registry),
		Logger:       logger,
		Memory:       briefMemory,
		MemoryClient: a.memory,
	})
	if err != nil {
		st.Close()
		return nil, err
	}

	a.http = &http.Server{
		Handler:           a.mux(registry),
		ReadHeaderTimeout: 10 * time.Second,
	}

	kinds := make([]string, 0, len(sources))
	for _, src := range sources {
		kinds = append(kinds, src.Kind())
	}
	logger.Info("conductor configured", "config", cfg,
		"profile", profile.Name, "skills", profile.SkillNames(), "sources", kinds)
	if len(kinds) == 0 {
		logger.Warn("no source is enabled: nothing will ever start a turn. Set both " +
			"PODIUM_AGENT_SLACK_APP_TOKEN and PODIUM_AGENT_SLACK_BOT_TOKEN.")
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
	rpc.Handle(agentv1connect.NewAgentServiceHandler(api.NewAgentService(api.AgentServiceOptions{
		Store:            a.store,
		Secrets:          a.podium,
		Model:            a.profile.Model,
		AnthropicBaseURL: a.cfg.AnthropicBaseURL,
		Memory:           a.memory,
		Logger:           a.logger,
	}), opts...))

	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	root.HandleFunc("/readyz", a.readyz)
	root.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	root.Handle("/"+agentv1connect.AgentServiceName+"/", api.RequireBearer(a.cfg.Token, rpc))
	if a.dev != nil {
		dev := api.RequireBearer(a.cfg.Token, a.dev.Handler())
		root.Handle(api.DevInboundPath, dev)
		root.Handle(api.DevOutboundPath, dev)
	}
	return root
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
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Listen, err)
	}
	a.ln = ln
	a.logger.InfoContext(ctx, "conductor listening", "addr", ln.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.publishMemoryKey(runCtx)

	serveErr := make(chan error, 1)
	go func() {
		if err := a.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	sourceErr := make(chan error, 1)
	if a.slack != nil {
		go func() { sourceErr <- a.slack.Run(runCtx) }()
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
	select {
	case <-conductorDone:
	case <-shutdownCtx.Done():
		a.logger.WarnContext(ctx, "a turn was still in flight at shutdown; it will be resumed on the next start")
	}
	return runErr
}
