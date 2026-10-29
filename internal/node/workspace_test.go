package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

// A read-only task restores its session's workspace and stores nothing back, so it can run
// beside the task that does write it without the two racing for the one snapshot.
func TestAReadOnlyWorkspaceRestoresAndStoresNothing(t *testing.T) {
	n := &Node{}
	ws := n.workspaceHooks("task_1", &spec.TaskSpec{WorkspaceSession: "sess_1"})
	require.NotNil(t, ws)
	assert.NotNil(t, ws.Restore)
	assert.NotNil(t, ws.Save, "a session's task stores its workspace")

	ws = n.workspaceHooks("task_1", &spec.TaskSpec{WorkspaceSession: "sess_1", WorkspaceReadOnly: true})
	require.NotNil(t, ws)
	assert.NotNil(t, ws.Restore, "it still starts from the session's files")
	assert.Nil(t, ws.Save, "and nothing it changes is kept")
}
