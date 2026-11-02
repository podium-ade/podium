package netlisten

import (
	"net"
	"testing"
)

func TestListenersAreNotMultipath(t *testing.T) {
	lc := Config()
	if lc.MultipathTCP() {
		t.Fatal("the listen config enables MPTCP")
	}
	ln, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		t.Fatalf("listener is %T", ln)
	}
	// MultipathTCP on the accepted side is what reports it; dial and check.
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			_ = c.Close()
		}
	}()
	c, err := tl.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if mp, _ := c.MultipathTCP(); mp {
		t.Fatal("an accepted connection is MPTCP")
	}
}
