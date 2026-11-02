// Package netlisten opens Podium's TCP listeners as plain TCP.
//
// Go enables multipath TCP (MPTCP) on Linux listeners by default from go 1.24 in go.mod, so
// every net.Listen("tcp", …) here was an MPTCP listener wherever the kernel allows it. Podium
// gains nothing from MPTCP, and an MPTCP listener is the setup the public exploit of
// CVE-2026-98432 (a use-after-free in the kernel's IPv4 TCP handshake, Linux 4.4 until fixed
// in 5.10.272 / 5.15.223 / 6.1.190 / 6.6.159 / 6.12.113 / 6.18.56 / 7.2.10 / 7.3) relies on.
// Opting out here does not fix the kernel — the bug is in plain IPv4 TCP — so the fix is still
// a patched kernel; this removes the known path to it from every host Podium runs on, and does
// not depend on GODEBUG=multipathtcp=0 being set in the environment.
package netlisten

import (
	"context"
	"net"
)

// Config is a ListenConfig with MPTCP off.
func Config() net.ListenConfig {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	return lc
}

// Listen is net.Listen with MPTCP off. A non-TCP network is passed through as it is.
func Listen(network, address string) (net.Listener, error) {
	lc := Config()
	return lc.Listen(context.Background(), network, address)
}
