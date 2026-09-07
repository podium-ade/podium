package docker

import "context"

// EgressPolicy is the hook a future egress allow-list plugs into.
//
// Today a task's network is a plain bridge with `internal: false`: the task and its
// sidecars reach each other, the internet, and whatever else the node's host routes. A
// tailnet is included — a container leaves through the host and inherits its routes, so on
// a tailnet node a task can open the control plane's own 443, which is how a task drives a
// browser against a live stack. Nothing here restricts that; what confines a worker today
// is the Tailscale ACL, not this file. Restricting the internet half means programming the
// node's firewall for the task's subnet, which is host-specific (nftables on Linux, nothing
// usable on Docker Desktop) and is deliberately not part of this step.
//
// When it arrives, Apply is called after the network is created and before any container
// joins it, and Revoke during Teardown after the last container is gone. An implementation
// that cannot enforce the list must return an error rather than silently allowing
// everything: a task that believes it is confined and is not is worse than one that fails.
type EgressPolicy interface {
	Apply(ctx context.Context, taskID, networkID string, allow []string) error
	Revoke(ctx context.Context, taskID, networkID string) error
}

// openEgress is the current behaviour, written down so the zero value of a future
// Options.Egress field has a name and a meaning.
type openEgress struct{}

func (openEgress) Apply(context.Context, string, string, []string) error { return nil }
func (openEgress) Revoke(context.Context, string, string) error          { return nil }

var _ EgressPolicy = openEgress{}
