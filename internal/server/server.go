// Package server wires the control plane together: store, transport, Connect handlers, the
// scheduler and the log fan-out, behind one HTTP server.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1/agentv1connect"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/server/api"
	"github.com/alvaroibarguen/podium/internal/server/artifacts"
	"github.com/alvaroibarguen/podium/internal/server/logs"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/scheduler"
	"github.com/alvaroibarguen/podium/internal/server/secrets"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/internal/transport"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

// ShutdownTimeout is how long Run gives in-flight work to finish after a signal.
const ShutdownTimeout = 10 * time.Second

// compressMinBytes is the payload size below which Connect does not bother compressing.
const compressMinBytes = 1024

// Server is a configured, not-yet-listening control plane.
type Server struct {
	cfg       Config
	logger    *slog.Logger
	store     *store.Store
	transport transport.Listener
	nodes     *nodes.Service
	logs      *logs.Service
	secrets   *secrets.Service
	artifacts *artifacts.Service
	scheduler *scheduler.Service
	http      *http.Server

	ln         net.Listener
	baseCancel context.CancelFunc
	serveErr   chan error
}

// New opens the store, migrates it, and builds every handler. It does not bind a socket; call
// Start or Run for that.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("server config: %w", err)
	}

	masterKey, err := loadMasterKey(cfg, logger)
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

	listener, err := newListener(cfg, st, logger)
	if err != nil {
		st.Close()
		return nil, err
	}

	timing := scheduler.TimingFromEnv()
	logSvc := logs.New(st, logger)
	nodeSvc := nodes.NewService(st, logSvc, logger)
	nodeSvc.SetAllowUntaggedNodes(cfg.TSAllowUntaggedNodes)
	nodeSvc.SetWatchdog(nodes.Watchdog{
		Interval:         timing.Watchdog,
		UnreachableAfter: timing.UnreachableAfter,
		OfflineAfter:     timing.OfflineAfter,
	})
	logSvc.SetSlots(nodeSvc.Registry())
	secretSvc := secrets.New(st, masterKey, logger)
	artifactSvc, err := newArtifacts(ctx, cfg, st, logger)
	if err != nil {
		st.Close()
		return nil, err
	}
	nodeSvc.SetArtifacts(artifactSvc)
	logSvc.SetArchive(artifactSvc, cfg.Rollup)
	warnAboutPlaintextTransport(ctx, cfg, st, logger)
	if timing != scheduler.DefaultTiming() {
		logger.Warn("scheduler timers are shrunk for testing; this is not a production configuration",
			"env", scheduler.FastTimersEnv, "tick", timing.Tick, "offline_after", timing.OfflineAfter)
	}
	s := &Server{
		cfg:       cfg,
		logger:    logger,
		store:     st,
		transport: listener,
		nodes:     nodeSvc,
		logs:      logSvc,
		secrets:   secretSvc,
		artifacts: artifactSvc,
		scheduler: scheduler.New(st, nodeSvc, secretSvc, timing, logger),
		serveErr:  make(chan error, 1),
	}
	s.http = &http.Server{
		Handler:           s.mux(),
		Protocols:         plaintextHTTP2(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// newListener builds the transport named by PODIUM_TRANSPORT. The dev transport keeps its
// loopback-only guard exactly as it was: the tailnet transports are additions beside it, not a
// loosening of it.
func newListener(cfg Config, st *store.Store, logger *slog.Logger) (transport.Listener, error) {
	identity := tailnet.IdentityOptions{NodeTag: cfg.TSRequiredNodeTag}
	if cfg.TSAllowUntaggedNodes {
		logger.Warn("PODIUM_TS_ALLOW_UNTAGGED_NODES is on: any untagged tailnet device may " +
			"enroll as a node with a valid enrollment token. The ACL tag is the network-level " +
			"proof that a caller is an authorised worker; without it only the token stands " +
			"between the tailnet and a running container. Tag your workers " + tailnet.DefaultNodeTag +
			" and turn this off.")
	}
	switch cfg.Transport {
	case TransportTailnet:
		return tailnet.New(tailnet.Options{
			Hostname: cfg.TSHostname,
			StateDir: cfg.TSStateDir,
			AuthKey:  cfg.TSAuthKey,
			Identity: identity,
			Users:    userStore{st},
			Logger:   logger,
		})
	case TransportHost:
		return tailnet.NewHost(tailnet.HostOptions{
			Identity: identity,
			Users:    userStore{st},
			Logger:   logger,
		})
	default:
		return dev.New(dev.Options{Listen: cfg.DevListen, Token: cfg.DevToken})
	}
}

// userStore adapts the store to the one method the tailnet transport needs. The transport does
// not care what a user row looks like, only that a login has been recorded.
type userStore struct{ st *store.Store }

func (u userStore) UpsertUser(ctx context.Context, login, displayName string) error {
	_, err := u.st.UpsertUser(ctx, login, displayName)
	return err
}

// newArtifacts builds the object-store service. An unconfigured endpoint is a supported
// deployment — a task does not need artifacts to run — and so is an endpoint that is down
// at start: the bucket probe is best effort and /readyz is what reports the truth. Refusing
// to start would make S3 a dependency of running any task at all, which is exactly the
// coupling step 10 exists to avoid.
func newArtifacts(ctx context.Context, cfg Config, st *store.Store, logger *slog.Logger) (*artifacts.Service, error) {
	if !cfg.S3.Enabled() {
		logger.Info("artifacts are off: PODIUM_S3_ENDPOINT is not set")
		return artifacts.New(st, nil, logger), nil
	}
	s3, err := artifacts.NewS3(cfg.S3)
	if err != nil {
		return nil, err
	}
	svc := artifacts.New(st, s3, logger)
	probeCtx, cancel := context.WithTimeout(ctx, artifactProbeTimeout)
	defer cancel()
	if err := svc.EnsureBucket(probeCtx); err != nil {
		logger.WarnContext(ctx, "the object store is not reachable; artifacts and log roll-up will fail "+
			"until it is. Tasks are unaffected: /readyz reports 503 and uploads emit a retryable error.",
			"endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket, "error", err)
	} else {
		logger.Info("artifacts enabled", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket)
	}
	return svc, nil
}

// artifactProbeTimeout bounds the start-up and readiness probes of the object store.
const artifactProbeTimeout = 5 * time.Second

// loadMasterKey resolves PODIUM_MASTER_KEY_FILE, then PODIUM_MASTER_KEY. Returning (nil,
// nil) is legitimate and means secrets are disabled: this is still a task runner without
// them, and demanding a key from every deployment that never uses one is a worse default
// than saying so at the first SetSecret.
func loadMasterKey(cfg Config, logger *slog.Logger) (*secrets.Key, error) {
	if cfg.MasterKeyFile != "" {
		key, err := secrets.LoadKeyFile(cfg.MasterKeyFile)
		if err != nil {
			return nil, err
		}
		logger.Info("secrets enabled", "key_id", key.ID(), "source", "PODIUM_MASTER_KEY_FILE")
		return key, nil
	}
	if cfg.MasterKey != "" {
		key, err := secrets.ParseKey([]byte(cfg.MasterKey))
		if err != nil {
			return nil, fmt.Errorf("PODIUM_MASTER_KEY: %w", err)
		}
		logger.Warn("PODIUM_MASTER_KEY holds the master key in an environment variable, where " +
			"every process that can read /proc, every `docker inspect` and every crash dump can see it. " +
			"It decrypts every secret this server holds. Use PODIUM_MASTER_KEY_FILE with a 0600 file " +
			"outside development.")
		logger.Info("secrets enabled", "key_id", key.ID(), "source", "PODIUM_MASTER_KEY")
		return key, nil
	}
	return nil, nil
}

// warnAboutPlaintextTransport says out loud what the dev transport costs once real secrets
// exist. Assign carries resolved secret values in the clear and relies on the transport to
// protect them: the tailnet transport is WireGuard, but the dev transport is plain HTTP.
// It is bound to loopback for exactly this reason, and that is a property worth stating
// rather than assuming. It never blocks startup.
func warnAboutPlaintextTransport(ctx context.Context, cfg Config, st *store.Store, logger *slog.Logger) {
	if cfg.Transport != TransportDev {
		return
	}
	rows, err := st.ListSecrets(ctx)
	if err != nil || len(rows) == 0 {
		return
	}
	logger.WarnContext(ctx, "PODIUM_TRANSPORT=dev serves plain HTTP, and an assignment carries "+
		"resolved secret values in the clear. Anything that can read the loopback socket — another "+
		"process on this machine, a packet capture, a proxy — sees them. This is why the dev "+
		"transport refuses to bind anything but loopback. Use PODIUM_TRANSPORT=tailnet, whose "+
		"WireGuard tunnel is what actually protects them, for anything beyond development.",
		"secrets", len(rows), "listen", cfg.DevListen)
}

// readyProber is the optional half of a transport that has something of its own to report on
// /readyz — for the tailnet transports, whether the device is still up and when its key expires.
type readyProber interface {
	Ready(ctx context.Context) (string, error)
}

// baseURLer is the optional half of a transport that knows the URL clients should dial. Only the
// tailnet transports do: they learn their MagicDNS name from the control plane.
type baseURLer interface {
	BaseURL() string
}

// plaintextHTTP2 keeps HTTP/1.1 for unary Connect calls and curl, and adds cleartext HTTP/2 so
// the node's bidirectional stream works without TLS. It replaces x/net's deprecated h2c wrapper.
func plaintextHTTP2() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

// mux routes the Connect services behind the identity middleware and leaves the operational
// endpoints open: a probe that needs a token is a probe that reports the wrong thing.
func (s *Server) mux() http.Handler {
	opts := []connect.HandlerOption{connect.WithCompressMinBytes(compressMinBytes)}

	rpc := http.NewServeMux()
	rpc.Handle(podiumv1connect.NewTaskServiceHandler(
		api.NewTaskService(s.store, s.logs, s.nodes, s.secrets, s.logger), opts...))
	rpc.Handle(podiumv1connect.NewNodeServiceHandler(s.nodes, opts...))
	rpc.Handle(podiumv1connect.NewNodeAdminServiceHandler(
		api.NewNodeAdminService(s.store, s.nodes.Registry(), s.nodes, s.logger), opts...))
	rpc.Handle(podiumv1connect.NewIdentityServiceHandler(
		api.NewIdentityService(s.cfg.AgentEnabled()), opts...))
	rpc.Handle(podiumv1connect.NewSecretServiceHandler(
		api.NewSecretService(s.secrets, s.logger), opts...))
	rpc.Handle(podiumv1connect.NewArtifactServiceHandler(
		api.NewArtifactService(s.artifacts, s.logger), opts...))

	metrics := prometheus.NewRegistry()
	metrics.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	root.HandleFunc("/readyz", s.readyz)
	root.Handle("/metrics", promhttp.HandlerFor(metrics, promhttp.HandlerOpts{}))

	// Every Connect procedure lives under /<fully-qualified service>/, so the identity
	// middleware is mounted on those three prefixes rather than on "/". That leaves "/" for the
	// embedded UI, whose assets are not secrets: a browser has no bearer token when it loads
	// index.html, and the bundle asks the operator for one before it calls anything.
	authenticated := transport.WithIdentity(s.transport, rpc)
	for _, service := range []string{
		podiumv1connect.TaskServiceName,
		podiumv1connect.NodeServiceName,
		podiumv1connect.NodeAdminServiceName,
		podiumv1connect.IdentityServiceName,
		podiumv1connect.SecretServiceName,
		podiumv1connect.ArtifactServiceName,
	} {
		root.Handle("/"+service+"/", authenticated)
	}
	// The conductor's service is mounted only when there is a conductor. With the URL
	// unset the prefix falls through to the SPA handler below, so a browser that asks for
	// it gets index.html rather than a 404 — and the UI has already hidden the Agent screen
	// because WhoAmI said agent_enabled is false. readyz deliberately does not probe the
	// conductor: a control plane whose agent is down is still a working task runner, and
	// readyz is what a load balancer and the compose healthcheck gate on.
	if s.cfg.AgentEnabled() {
		agent, err := api.NewAgentProxy(s.cfg.AgentURL, s.cfg.AgentToken, s.logger)
		if err != nil {
			// Validate has already parsed the URL, so this cannot fire in a started
			// server; refusing to serve the prefix beats serving it wrongly.
			s.logger.Error("the agent proxy could not be built; the agent API is not mounted", "error", err)
		} else {
			root.Handle("/"+agentv1connect.AgentServiceName+"/",
				transport.WithIdentity(s.transport, agent))
		}
	}
	// The artifact proxy is a plain HTTP route rather than a Connect procedure because a
	// 512 MB artifact has to stream. It sits behind the same identity middleware.
	root.Handle(api.ArtifactDownloadPrefix,
		transport.WithIdentity(s.transport, api.NewArtifactDownloadHandler(s.artifacts, s.logger)))
	root.Handle("/", api.NewUIHandler(s.logger))
	return root
}

// readyz reports whether the dependencies this process cannot work without are reachable.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.logger.WarnContext(ctx, "readyz: postgres unreachable", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "postgres unreachable")
		return
	}
	if err := s.artifacts.Ready(ctx); err != nil {
		// Artifacts being down does not stop a task from running; it stops files from
		// being kept. Saying so on /readyz is the point — a load balancer that drains this
		// process would be the wrong reaction, and an operator who never hears about it is
		// the worse one.
		s.logger.WarnContext(ctx, "readyz: object store unreachable", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "object store unreachable: "+err.Error())
		return
	}
	detail := "ok"
	if p, ok := s.transport.(readyProber); ok {
		d, err := p.Ready(ctx)
		if err != nil {
			s.logger.WarnContext(ctx, "readyz: transport not ready", "error", err)
			writePlain(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		detail = "ok, " + d
	}
	writePlain(w, http.StatusOK, detail)
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body + "\n"))
}

// Start binds the socket and starts serving, the scheduler and the log fan-out. It returns as
// soon as the listener is up.
func (s *Server) Start(ctx context.Context) error {
	ln, err := s.transport.Listen(ctx)
	if err != nil {
		return err
	}
	s.ln = ln

	baseCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.baseCancel = cancel
	s.http.BaseContext = func(net.Listener) context.Context { return baseCtx }

	go func() {
		if err := s.logs.Run(baseCtx); err != nil && baseCtx.Err() == nil {
			s.logger.ErrorContext(baseCtx, "task event fan-out stopped", "error", err)
		}
	}()
	go func() {
		if err := s.scheduler.Run(baseCtx); err != nil && baseCtx.Err() == nil {
			s.logger.ErrorContext(baseCtx, "scheduler stopped", "error", err)
		}
	}()
	go func() {
		if err := s.nodes.RunWatchdog(baseCtx); err != nil && baseCtx.Err() == nil {
			s.logger.ErrorContext(baseCtx, "node health watchdog stopped", "error", err)
		}
	}()
	go func() {
		if err := s.logs.RunRollUp(baseCtx); err != nil && baseCtx.Err() == nil {
			s.logger.ErrorContext(baseCtx, "log roll-up stopped", "error", err)
		}
	}()
	go func() {
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveErr <- err
	}()

	s.logger.InfoContext(ctx, "podium-server listening",
		"addr", ln.Addr().String(), "transport", s.cfg.Transport, "url", s.URL())
	return nil
}

// Addr is the bound address, valid once Start has returned.
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.cfg.DevListen
	}
	return s.ln.Addr().String()
}

// URL is the base URL a Connect client should dial. Under the tailnet transports that is the
// MagicDNS name the certificate is issued for, not the address the socket is bound to.
func (s *Server) URL() string {
	if b, ok := s.transport.(baseURLer); ok {
		if url := b.BaseURL(); url != "" {
			return url
		}
	}
	return "http://" + s.Addr()
}

// Shutdown stops accepting, closes every node stream so the nodes reconnect elsewhere, and
// waits for in-flight requests until ctx expires. Streams still open at the deadline are cut.
func (s *Server) Shutdown(ctx context.Context) error {
	s.nodes.Registry().CloseAll()
	err := s.http.Shutdown(ctx)
	if s.baseCancel != nil {
		s.baseCancel()
	}
	if err != nil {
		_ = s.http.Close()
	}
	<-s.serveErr
	return err
}

// Close releases the store and leaves the tailnet. Call it after Shutdown.
func (s *Server) Close() {
	if c, ok := s.transport.(io.Closer); ok {
		if err := c.Close(); err != nil {
			s.logger.Warn("closing the transport failed", "error", err)
		}
	}
	s.store.Close()
}

// Run serves until ctx is cancelled, then shuts down within ShutdownTimeout.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	select {
	case err := <-s.serveErr:
		s.serveErr <- err
		return err
	case <-ctx.Done():
	}
	s.logger.Info("podium-server shutting down", "timeout", ShutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}
