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
