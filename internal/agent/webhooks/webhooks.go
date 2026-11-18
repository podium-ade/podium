// Package webhooks owns the conductor's inbound webhook listeners.
//
// The GitHub App and the Linear app each name the address their webhooks arrive on, and the
// two may be the same. One process cannot bind an address twice, so the listeners belong
// here rather than to the sources: one listener per address, and each source mounts its
// handler on a path of it (/webhooks/github, /webhooks/linear). The first path on an address
// binds it and the last one to go closes it, so a source swapped for another on the same
// address never lets go of a port the other is still using.
//
// Only the mounted paths are served. A tunnel that exposes a webhook address exposes those
// handlers and nothing else, as it did when each source had a listener of its own.
package webhooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/netlisten"
)

// shutdownWait bounds how long a listener that lost its last path waits for deliveries in
// flight before it closes them. It is what the GitHub source waited when it owned its own.
const shutdownWait = 5 * time.Second

// Server is the set of webhook listeners, by address. The zero value is not usable: call New.
type Server struct {
	logger *slog.Logger

	mu        sync.Mutex
	listeners map[string]*listener
}

// listener is one bound address and the handlers mounted on it.
type listener struct {
	addr string
	// bound is the address the socket actually has, which differs from addr for port 0.
	bound string
	srv   *http.Server
	// routes is the handler for each mounted path. Guarded by Server.mu.
	routes map[string]http.Handler
	// closing is set once the last path has gone and the listener is shutting down. A mount
	// that arrives then waits for done and binds the address afresh. Guarded by Server.mu.
	closing bool
	// done is closed when the listener has stopped serving, and err is why: nil after a
	// shutdown, the serve error otherwise.
	done chan struct{}
	err  error
}

// New returns a Server holding no address. Nil logger discards.
func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{logger: logger, listeners: map[string]*listener{}}
}

// Serve mounts h at path on addr and serves it until ctx ends, then takes it down again. It
// returns at once, with an error, when addr cannot be bound or path is already served there.
// It also returns, with an error, if the listener stops on its own.
//
// It is the shape of a source's Run: a source calls it from Run and returns what it returns,
// so stopping the source is what unmounts it, and Serve has returned only once the address
// is free again (or still held by another path).
func (s *Server) Serve(ctx context.Context, addr, path string, h http.Handler) error {
	l, err := s.mount(addr, path, h)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		s.unmount(l, path)
		return nil
	case <-l.done:
		s.unmount(l, path)
		return fmt.Errorf("webhooks: the listener on %s stopped: %w", addr, l.err)
	}
}

// Holds reports whether this process is listening on addr already. The address is compared
// as written: 0.0.0.0:8091 and :8091 are different addresses here, as they are to the
// operator who typed them.
func (s *Server) Holds(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[addr]
	return ok && !l.closing
}

// Paths is what is mounted on addr, sorted. Empty when nothing is.
func (s *Server) Paths(addr string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[addr]
	if !ok || l.closing {
		return nil
	}
	out := make([]string, 0, len(l.routes))
	for p := range l.routes {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func (s *Server) mount(addr, path string, h http.Handler) (*listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[addr]
	for ok && l.closing {
		// The last path is going and the port with it. Wait for it to go, then bind it
		// again, rather than fail on an address that is about to be free.
		s.mu.Unlock()
		<-l.done
		s.mu.Lock()
		l, ok = s.listeners[addr]
	}
	if !ok {
		var err error
		if l, err = s.bind(addr); err != nil {
			return nil, err
		}
	}
	if _, taken := l.routes[path]; taken {
		return nil, fmt.Errorf("webhooks: %s is already served on %s", path, addr)
	}
	l.routes[path] = h
	s.logger.Info("webhooks: serving", "listen", addr, "path", path)
	return l, nil
}

// bind opens addr and starts serving it. The caller holds s.mu.
func (s *Server) bind(addr string) (*listener, error) {
	ln, err := netlisten.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("webhooks: listen on %s: %w", addr, err)
	}
	l := &listener{
		addr: addr, bound: ln.Addr().String(),
		routes: map[string]http.Handler{}, done: make(chan struct{}),
	}
	l.srv = &http.Server{Handler: s.route(l), ReadHeaderTimeout: 10 * time.Second}
	s.listeners[addr] = l
	s.logger.Info("webhooks: listening", "listen", addr, "addr", ln.Addr().String())
	go func() {
		err := l.srv.Serve(ln)
		if !errors.Is(err, http.ErrServerClosed) {
			l.err = err
			s.logger.Error("webhooks: a listener stopped", "listen", addr, "error", err)
		}
		s.mu.Lock()
		if s.listeners[addr] == l {
			delete(s.listeners, addr)
		}
		s.mu.Unlock()
		close(l.done)
	}()
	return l, nil
}

// route sends a request to the handler mounted at its exact path. Anything else is a 404, so
// a listener shared by two sources serves neither one's paths to the other.
func (s *Server) route(l *listener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		h := l.routes[r.URL.Path]
		s.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// unmount takes path off l and, when it was the last, shuts the listener down and waits until
// the address is free.
func (s *Server) unmount(l *listener, path string) {
	s.mu.Lock()
	delete(l.routes, path)
	last := len(l.routes) == 0 && !l.closing
	if last {
		l.closing = true
	}
	s.mu.Unlock()
	s.logger.Info("webhooks: no longer serving", "listen", l.addr, "path", path)
	if !last {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if err := l.srv.Shutdown(ctx); err != nil {
		s.logger.Warn("webhooks: shutting a listener down failed", "listen", l.addr, "error", err)
		_ = l.srv.Close()
	}
	<-l.done
	s.logger.Info("webhooks: stopped listening", "listen", l.addr)
}

// Addr is the address a listener on addr actually bound, which differs from addr when it
// asked for port 0. Empty when nothing listens there. Tests use it to reach a listener.
func (s *Server) Addr(addr string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[addr]
	if !ok {
		return ""
	}
	return l.bound
}
