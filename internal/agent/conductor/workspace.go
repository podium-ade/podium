package conductor

import (
	"time"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/pkg/spec"
)

// DefaultWorkspaceWarm is how long a playbook task stays up after it answers, waiting
// for the next instruction. The snapshot is taken when that wait ends. A playbook sets
// PODIUM_WORKSPACE_WARM to change it, including 0s to leave as soon as the turn ends.
const DefaultWorkspaceWarm = 5 * time.Minute

// workspaceWarmEnv is the playbook env that overrides DefaultWorkspaceWarm. It is a Go
// duration. The node turns the result into PODIUM_WORKSPACE_WARM_SECONDS for the runtime.
const workspaceWarmEnv = "PODIUM_WORKSPACE_WARM"

// applyWorkspace marks a node task as a session workspace. Host turns never call it:
// they have no volume. The repo lets a session that has never snapshotted start from
// the base, once one has been published.
func applyWorkspace(s *spec.TaskSpec, sessionID string, playbook profiles.Playbook) {
	if s == nil || sessionID == "" {
		return
	}
	s.WorkspaceSession = sessionID
	if len(playbook.Repos) > 0 {
		s.WorkspaceRepo = playbook.Repos[0].URL
	}
	warm := DefaultWorkspaceWarm
	if raw, ok := playbook.Env[workspaceWarmEnv]; ok {
		d, err := time.ParseDuration(raw)
		if err == nil {
			warm = d
		}
	}
	if warm > 0 {
		s.WorkspaceWarm = spec.Duration(warm)
	}
}
