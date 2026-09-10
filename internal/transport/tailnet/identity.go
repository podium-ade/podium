// Package tailnet is the Tailscale transport. The server joins the tailnet as its own device
// (tsnet) or borrows the host's tailscaled, serves HTTPS on its MagicDNS name, and names every
// caller by asking Tailscale who is on the other end of the connection — a device tag for a
// node, a login name for a human. There is no password, session cookie or bearer token.
package tailnet

import (
	"fmt"
	"slices"
	"strings"

	"tailscale.com/client/tailscale/apitype"

	"github.com/podium-ade/podium/internal/transport"
)

// The canonical ACL tags from the design (§4.1). One device per install carries the server tag;
// every worker carries the node tag.
const (
	DefaultNodeTag   = "tag:podium-node"
	DefaultServerTag = "tag:podium-server"
)

// IdentityOptions is the tag policy Identify applies to a WhoIs answer.
type IdentityOptions struct {
	// NodeTag is the tag a device must carry to be treated as a worker,
	// PODIUM_TS_REQUIRED_NODE_TAG. Empty means DefaultNodeTag.
	NodeTag string
	// ServerTag is the tag the control plane itself carries. A caller wearing it is refused:
	// servers do not call servers, so such a connection is a misconfiguration or a loop.
	// Empty means DefaultServerTag.
	ServerTag string
}

func (o IdentityOptions) nodeTag() string {
	if o.NodeTag == "" {
		return DefaultNodeTag
	}
	return o.NodeTag
}

func (o IdentityOptions) serverTag() string {
	if o.ServerTag == "" {
		return DefaultServerTag
	}
	return o.ServerTag
}

// classify turns one WhoIs answer into an Identity. It is the whole of the transport's
// authentication decision and is deliberately free of I/O, so the table of cases can be tested
// against fabricated WhoIs responses.
//
// The order matters. The server tag is refused before the node tag is honoured, and a tagged
// device never falls through to the user path: Tailscale reports the tag owner's profile for a
// tagged device, so treating that profile as a human login would let any tagged machine act as
// the person who created its tag.
func classify(who *apitype.WhoIsResponse, remoteAddr string, opts IdentityOptions) (transport.Identity, error) {
	if who == nil || who.Node == nil {
		return transport.Identity{}, fmt.Errorf("tailnet: %s is not a tailnet peer: %w",
			remoteAddr, transport.ErrUnauthenticated)
	}
	tags := who.Node.Tags
	stableID := string(who.Node.StableID)
	device := strings.TrimSuffix(who.Node.Name, ".")

	if slices.Contains(tags, opts.serverTag()) {
		return transport.Identity{}, fmt.Errorf(
			"tailnet: %s carries %s; the control plane does not accept calls from another control plane: %w",
			device, opts.serverTag(), transport.ErrForbidden)
	}
	if slices.Contains(tags, opts.nodeTag()) {
		return transport.Identity{
			Kind:         transport.KindNode,
			Login:        device,
			NodeTags:     slices.Clone(tags),
			NodeStableID: stableID,
			RemoteAddr:   remoteAddr,
		}, nil
	}
	if len(tags) > 0 {
		return transport.Identity{}, fmt.Errorf(
			"tailnet: %s is tagged %s, which is neither %s nor a user device: %w",
			device, strings.Join(tags, ","), opts.nodeTag(), transport.ErrForbidden)
	}

	if who.UserProfile == nil || who.UserProfile.LoginName == "" {
		return transport.Identity{}, fmt.Errorf(
			"tailnet: %s has no ACL tag and no login name: %w", device, transport.ErrUnauthenticated)
	}
	return transport.Identity{
		Kind:         transport.KindUser,
		Login:        who.UserProfile.LoginName,
		DisplayName:  who.UserProfile.DisplayName,
		NodeStableID: stableID,
		RemoteAddr:   remoteAddr,
	}, nil
}
