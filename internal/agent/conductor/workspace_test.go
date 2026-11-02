package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

func TestApplyWorkspace(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{
		Name:  "coder",
		Repos: []profiles.Repo{{Name: "app", URL: "https://github.com/acme/app.git", DefaultBranch: "main"}},
	})
	assert.Equal(t, "sess_1.coder", s.WorkspaceSession, "one workspace per conversation and playbook")

	pr := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(pr, store.Session{ID: "sess_2", SourceKind: SourceGitHub}, profiles.Playbook{Name: "coder"})
	assert.Equal(t, "sess_2", pr.WorkspaceSession, "a pull request keeps one workspace for every playbook")
	assert.Equal(t, "https://github.com/acme/app.git", s.WorkspaceRepo)
	assert.Equal(t, spec.Duration(DefaultWorkspaceWarm), s.WorkspaceWarm)
	assert.False(t, s.WorkspacePublishBase)
}

func TestApplyWorkspacePublishesABaseOnlyWhenAsked(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	pb := profiles.Playbook{
		Repos: []profiles.Repo{{URL: "https://github.com/acme/app.git"}},
		Env:   map[string]string{workspacePublishEnv: "1"},
	}
	applyWorkspace(s, store.Session{ID: "sess_1"}, pb)
	assert.True(t, s.WorkspacePublishBase)

	s = &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Env: map[string]string{workspacePublishEnv: "1"}})
	assert.False(t, s.WorkspacePublishBase, "a base needs a repository to be stored under")
}

func TestApplyWorkspaceHonoursAZeroWarmWindow(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Env: map[string]string{workspaceWarmEnv: "0s"}})
	assert.Equal(t, "sess_1", s.WorkspaceSession)
	assert.Equal(t, spec.Duration(0), s.WorkspaceWarm)
}

func TestApplyWorkspaceIgnoresABadDuration(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Env: map[string]string{workspaceWarmEnv: "soon"}})
	assert.Equal(t, spec.Duration(5*time.Minute), s.WorkspaceWarm)
}

func TestAPlaybooksSnapshotLeavesOutRebuildableDirectoriesUnlessItSaysOtherwise(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Name: "coder"})
	assert.Equal(t, DefaultWorkspaceExclude, s.Env[spec.WorkspaceExcludeEnv])

	s = &spec.TaskSpec{Image: "alpine:3", Env: map[string]string{spec.WorkspaceExcludeEnv: ""}}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Name: "coder", Env: map[string]string{spec.WorkspaceExcludeEnv: ""}})
	assert.Empty(t, s.Env[spec.WorkspaceExcludeEnv], "a playbook that names nothing keeps everything")
}
