package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// gatewayDialTimeout bounds one connection attempt to a service inside the task. A
// service that is not listening yet is the normal case while the command builds it.
const gatewayDialTimeout = 5 * time.Second

// Route is one port the gateway listens on and the service inside the task it forwards to.
type Route struct {
	Port   int
	Target string // host:port on the task network
}

// ParseRoutes reads the gateway's arguments: PORT=HOST:PORT, one per exposed port.
func ParseRoutes(args []string) ([]Route, error) {
	if len(args) == 0 {
		return nil, errors.New("no routes: want PORT=HOST:PORT")
	}
	routes := make([]Route, 0, len(args))
	for _, a := range args {
		port, target, ok := strings.Cut(a, "=")
		n, err := strconv.Atoi(port)
		if !ok || err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("route %q: want PORT=HOST:PORT", a)
		}
		if _, _, err := net.SplitHostPort(target); err != nil {
			return nil, fmt.Errorf("route %q: %w", a, err)
		}
		routes = append(routes, Route{Port: n, Target: target})
	}
	return routes, nil
}

// Gateway is `podium-runner gateway`: the container a node puts on an exposed task's
// network. It listens on every exposed port and forwards each connection to the sidecar or
// the task container that owns it, by name, which is what lets a node publish a task's
// ports on one address whatever its platform can route to.
func Gateway(args []string) int {
	routes, err := ParseRoutes(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "podium-runner gateway: %v\n", err)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := ServeGateway(ctx, "", routes); err != nil {
		fmt.Fprintf(os.Stderr, "podium-runner gateway: %v\n", err)
		return exitInternal
	}
	return 0
}

// ServeGateway forwards until ctx ends. host is the listen address, empty for every one.
func ServeGateway(ctx context.Context, host string, routes []Route) error {
	lns := make([]net.Listener, 0, len(routes))
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()
	for _, r := range routes {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(r.Port)))
		if err != nil {
			return err
		}
		lns = append(lns, ln)
	}
	var wg sync.WaitGroup
	for i, ln := range lns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			forward(ln, routes[i].Target)
		}()
	}
	<-ctx.Done()
	for _, ln := range lns {
		_ = ln.Close()
	}
	wg.Wait()
	return nil
}

func forward(ln net.Listener, target string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go Splice(c, func() (net.Conn, error) {
			return net.DialTimeout("tcp", target, gatewayDialTimeout)
		})
	}
}

// Splice connects c to what dial returns and copies both ways until both sides are done,
// passing a half-close along so a request/response protocol still sees its EOF.
func Splice(c net.Conn, dial func() (net.Conn, error)) {
	defer func() { _ = c.Close() }()
	up, err := dial()
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}
