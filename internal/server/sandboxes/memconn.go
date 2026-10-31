package sandboxes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// memListener is a net.Listener whose connections are in-memory pipes. The server serves its
// own node service on one, so a provider's runner reaches it with no socket, port or token:
// the only way in is to hold the listener.
type memListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *memListener) Addr() net.Addr { return memAddr{} }

// dial makes a connection to the listener.
func (l *memListener) dial(ctx context.Context) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		return nil, errors.New("sandboxes: the in-memory node service is closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "podium-server" }

// memURL is the base URL a runner's client uses; the host is never resolved.
const memURL = "http://podium-server.internal"

// serveInMemory serves handler on an in-memory listener over cleartext HTTP/2 — the node stream
// is bidirectional — and returns a client that reaches it, and a stop function.
func serveInMemory(handler http.Handler) (*http.Client, func()) {
	l := newMemListener()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: handler, Protocols: protocols, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	tr := &http.Transport{
		Protocols: protocols,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return l.dial(ctx)
		},
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = l.Close()
		tr.CloseIdleConnections()
	}
	return &http.Client{Transport: tr}, stop
}
