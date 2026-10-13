package runner

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestParseRoutes(t *testing.T) {
	routes, err := ParseRoutes([]string{"3000=task:3000", "5011=accounts:5011"})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[1] != (Route{Port: 5011, Target: "accounts:5011"}) {
		t.Fatalf("routes = %v", routes)
	}
	for _, bad := range [][]string{nil, {"3000"}, {"x=task:1"}, {"3000=task"}, {"0=task:1"}} {
		if _, err := ParseRoutes(bad); err == nil {
			t.Errorf("ParseRoutes(%q) accepted a bad route", bad)
		}
	}
}

// TestGatewayForwardsBothWays: a request through the gateway reaches the service, the reply
// comes back, and the client's half-close reaches the service as EOF.
func TestGatewayForwardsBothWays(t *testing.T) {
	svc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close() }()
	go func() {
		c, err := svc.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		body, _ := io.ReadAll(c)
		_, _ = c.Write(append([]byte("echo:"), body...))
	}()

	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- ServeGateway(ctx, "127.0.0.1", []Route{{Port: port, Target: svc.Addr().String()}})
	}()

	var c net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	_, _ = c.Write([]byte("hello"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := bufio.NewReader(c).ReadString(0)
	_ = c.Close()
	if got != "echo:hello" {
		t.Errorf("reply = %q, want echo:hello", got)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeGateway: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway did not stop")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}
