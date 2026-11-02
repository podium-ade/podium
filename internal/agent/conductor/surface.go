package conductor

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// turnSurfaceURL is this conductor as a turn running on runsOn reaches it: where it delegates,
// reads its pull request and redeems a git capability. Each placement has a different vantage
// point, which is why there is more than one address:
//
//   - the host: loopback, because the turn is a child of this process and its token is not
//     something to send over a network;
//   - the fleet's nodes: GitTaskURL (PODIUM_AGENT_TASK_URL), which resolves from a container
//     on a Docker host;
//   - a sandbox provider: SandboxTaskURL (PODIUM_AGENT_SANDBOX_URL), because a cloud sandbox
//     is on nobody's Docker network and host.docker.internal resolves to nothing there. Unset,
//     it is GitTaskURL, which is right for a provider inside the operator's own network.
//
// Empty means a turn there has no way back here.
func (c *Conductor) turnSurfaceURL(runsOn string) string {
	switch runsOn {
	case profiles.RunsOnHost:
		if c.host == nil {
			return ""
		}
		return c.host.TurnURL
	case "", profiles.RunsOnNodes:
		return c.gitTaskURL
	default:
		if c.sandboxTaskURL != "" {
			return c.sandboxTaskURL
		}
		return c.gitTaskURL
	}
}

// placedRunsOn is where this job's turn runs, as turnSurfaceURL needs it: the host for the
// assistant, or the playbook's runs_on resolved against the conductor's default.
func (c *Conductor) placedRunsOn(ctx context.Context, j job) string {
	if j.onHost {
		return profiles.RunsOnHost
	}
	return c.placed(ctx, j.playbook).RunsOn
}
