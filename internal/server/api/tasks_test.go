package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// A listed task leaves out what an agent task carries by the hundred KB, and keeps what the
// list shows. The stored task itself is not changed: GetTask reads the same row.
func TestListedTaskDropsEnvAndMountContents(t *testing.T) {
	task := store.Task{
		ID: "task_1",
		Spec: spec.TaskSpec{
			Image: "ghcr.io/podium-ade/podium-agent:0.7.3",
			Env:   map[string]string{"PODIUM_AGENT_TURN": "a long turn"},
			Mounts: []spec.FileMount{{
				Target: "/podium/files/skills/pr-review",
				Files:  map[string]string{"SKILL.md": "a long skill"},
			}},
		},
	}

	got := listedTask(task).GetSpec()
	require.Equal(t, "ghcr.io/podium-ade/podium-agent:0.7.3", got.GetImage())
	require.Empty(t, got.GetEnv())
	require.Len(t, got.GetMounts(), 1)
	require.Equal(t, "/podium/files/skills/pr-review", got.GetMounts()[0].GetTarget())
	require.Empty(t, got.GetMounts()[0].GetFiles())

	require.Equal(t, "a long turn", task.Spec.Env["PODIUM_AGENT_TURN"])
	require.Equal(t, "a long skill", task.Spec.Mounts[0].Files["SKILL.md"])
}

func TestADesktopLinkIsOnlyForARunningDesktopTask(t *testing.T) {
	desktop := spec.TaskSpec{Expose: &spec.Expose{Ports: map[string]spec.ExposedPort{"desktop": {Port: 6901}}}}
	web := spec.TaskSpec{Expose: &spec.Expose{Ports: map[string]spec.ExposedPort{"web": {Port: 3000}}}}

	require.NoError(t, desktopProblem(store.Task{ID: "t", Status: store.StatusRunning, Spec: desktop}))
	require.ErrorContains(t, desktopProblem(store.Task{ID: "t", Status: store.StatusRunning, Spec: web}),
		"not a desktop playbook")
	require.ErrorContains(t, desktopProblem(store.Task{ID: "t", Status: store.StatusRunning}),
		"not a desktop playbook")
	for _, st := range []store.Status{store.StatusQueued, store.StatusProvisioning, store.StatusSucceeded} {
		require.ErrorContains(t, desktopProblem(store.Task{ID: "t", Status: st, Spec: desktop}),
			"only while its task runs", st)
	}
}
