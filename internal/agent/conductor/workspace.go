package conductor

import (
	"time"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// DefaultWorkspaceWarm is how long a playbook task stays up after it answers, waiting
// for the next instruction. The snapshot is taken when that wait ends. A playbook sets
// PODIUM_WORKSPACE_WARM to change it, including 0s to leave as soon as the turn ends.
const DefaultWorkspaceWarm = 5 * time.Minute

// DefaultDesktopWarm is a desktop playbook's: a desktop is something a person keeps working
// with between messages, and what it shows is gone when the turn's command exits. The wait
// counts against the playbook's timeout, so a desktop playbook wants a timeout well past it.
const DefaultDesktopWarm = 30 * time.Minute

// workspaceWarmEnv is the playbook env that overrides DefaultWorkspaceWarm. It is a Go
// duration. The node turns the result into PODIUM_WORKSPACE_WARM_SECONDS for the runtime.
const workspaceWarmEnv = "PODIUM_WORKSPACE_WARM"

// workspacePublishEnv publishes this task's workspace as the shared base for the
// playbook's first repository. It is off unless the value is "1": a session snapshot is
// not a base anyone else should start from.
const workspacePublishEnv = "PODIUM_PUBLISH_BASE"

// DefaultWorkspaceExclude is what a playbook's snapshots leave out when it names nothing:
// dependency trees and build caches, which the next task rebuilds and which are most of a
// workspace's size.
const DefaultWorkspaceExclude = "node_modules,.cache,__pycache__,.venv,target"

// workspaceKey is the snapshot a playbook's tasks in a conversation keep: one per conversation
// AND playbook. A conversation's playbooks run on different images, and one sharing another's
// workspace restored a dev image's node_modules and build caches into a data image, and two of
// them running at once each stored over what the other had done.
//
// A pull request is the exception, and keeps one workspace for every playbook: they all work
// on its one branch, a review reads what the fix changed, and one at a time changes it.
func workspaceKey(sess store.Session, playbook string) string {
	if sess.SourceKind == SourceGitHub {
		return sess.ID
	}
	return spec.WorkspacePlaybookKey(sess.ID, playbook)
}

// applyWorkspace marks a node task as a conversation's workspace for its playbook. Host turns
// never call it: they have no volume. The repo lets a workspace that has never snapshotted
// start from the base, once one has been published.
func applyWorkspace(s *spec.TaskSpec, sess store.Session, playbook profiles.Playbook) {
	if s == nil || sess.ID == "" {
		return
	}
	s.WorkspaceSession = workspaceKey(sess, playbook.Name)
	// Rebuildable directories stay out of the snapshot unless the playbook says otherwise —
	// including by setting the variable to nothing, which keeps everything.
	if _, set := playbook.Env[spec.WorkspaceExcludeEnv]; !set {
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[spec.WorkspaceExcludeEnv] = DefaultWorkspaceExclude
	}
	if len(playbook.Repos) > 0 {
		s.WorkspaceRepo = playbook.Repos[0].URL
	}
	if playbook.Env[workspacePublishEnv] == "1" && s.WorkspaceRepo != "" {
		s.WorkspacePublishBase = true
	}
	warm := DefaultWorkspaceWarm
	if playbook.Desktop {
		warm = DefaultDesktopWarm
	}
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
