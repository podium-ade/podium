// Package server wires the control plane together: store, transport, Connect handlers, the
// scheduler and the log fan-out, behind one HTTP server.
package server

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

	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/server/api"
	"github.com/alvaroibarguen/podium/internal/server/logs"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/scheduler"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/internal/transport"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
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
	scheduler scheduler.Scheduler
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

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}

	listener, err := dev.New(dev.Options{Listen: cfg.DevListen, Token: cfg.DevToken})
	if err != nil {
		st.Close()
		return nil, err
	}

	logSvc := logs.New(st, logger)
	nodeSvc := nodes.NewService(st, logSvc, logger)
	s := &Server{
		cfg:       cfg,
		logger:    logger,
		store:     st,
		transport: listener,
		nodes:     nodeSvc,
		logs:      logSvc,
		scheduler: scheduler.NewNaive(st, nodeSvc, logger),
		serveErr:  make(chan error, 1),
	}
	s.http = &http.Server{
		Handler:           s.mux(),
		Protocols:         plaintextHTTP2(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
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
		api.NewTaskService(s.store, s.logs, s.nodes, s.logger), opts...))
	rpc.Handle(podiumv1connect.NewNodeServiceHandler(s.nodes, opts...))
	rpc.Handle(podiumv1connect.NewNodeAdminServiceHandler(
		api.NewNodeAdminService(s.store, s.nodes.Registry(), s.logger), opts...))

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
	} {
		root.Handle("/"+service+"/", authenticated)
	}
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
	writePlain(w, http.StatusOK, "ok")
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
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveErr <- err
	}()

	s.logger.InfoContext(ctx, "podium-server listening",
		"addr", ln.Addr().String(), "transport", s.cfg.Transport)
	return nil
}

// Addr is the bound address, valid once Start has returned.
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.cfg.DevListen
	}
	return s.ln.Addr().String()
}

// URL is the base URL a Connect client should dial.
func (s *Server) URL() string { return "http://" + s.Addr() }

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

// Close releases the store. Call it after Shutdown.
func (s *Server) Close() { s.store.Close() }

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
