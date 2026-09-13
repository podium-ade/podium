package api

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func TestPlaybookAndSkillWriteRpcsRefuse(t *testing.T) {
	svc := NewAgentService(AgentServiceOptions{})
	ctx := loginCtx("alice")

	_, err := svc.CreatePlaybook(ctx, connect.NewRequest(&agentv1.CreatePlaybookRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "files")

	_, err = svc.UpdatePlaybook(ctx, connect.NewRequest(&agentv1.UpdatePlaybookRequest{}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.DeletePlaybook(ctx, connect.NewRequest(&agentv1.DeletePlaybookRequest{Name: "general"}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.UploadSkill(ctx, connect.NewRequest(&agentv1.UploadSkillRequest{}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "files")

	_, err = svc.SetSkillEnabled(ctx, connect.NewRequest(&agentv1.SetSkillEnabledRequest{Name: "pr-review"}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.DeleteSkill(ctx, connect.NewRequest(&agentv1.DeleteSkillRequest{Name: "pr-review"}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
