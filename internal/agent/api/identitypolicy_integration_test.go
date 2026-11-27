//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// Anyone reads the identity policy; only an admin sets it; a choice a setting does not offer
// is refused; and a save changes only what it names, so two admins saving different settings
// from pages loaded before either save both keep theirs.
func TestTheIdentityPolicyIsSetByAdminsOneSettingAtATime(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	set := func(p *agentv1.IdentityPolicy) error {
		_, err := f.adminClient("alice").SetIdentityPolicy(ctx, connect.NewRequest(&agentv1.SetIdentityPolicyRequest{Policy: p}))
		return err
	}

	got, err := f.clientAs("bob").GetIdentityPolicy(ctx, connect.NewRequest(&agentv1.GetIdentityPolicyRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "person_then_app", got.Msg.GetPolicy().GetLinear())
	assert.Equal(t, "refuse", got.Msg.GetPolicy().GetSlackOutsiders())
	assert.Equal(t, "app", got.Msg.GetPolicy().GetSharedModelKey(), "a shared conversation spends the company's by default")

	_, err = f.clientAs("bob").SetIdentityPolicy(ctx, connect.NewRequest(&agentv1.SetIdentityPolicyRequest{
		Policy: &agentv1.IdentityPolicy{SlackOutsiders: "answer"},
	}))
	assert.Equal(t, "permission_denied", codeOf(err))
	assert.Equal(t, "invalid_argument", codeOf(set(&agentv1.IdentityPolicy{GithubRead: "app"})))
	assert.Equal(t, "invalid_argument", codeOf(set(&agentv1.IdentityPolicy{SharedModelKey: "person_then_app"})),
		"a shared conversation never spends a personal API key")

	require.NoError(t, set(&agentv1.IdentityPolicy{Linear: "app"}))
	require.NoError(t, set(&agentv1.IdentityPolicy{SlackOutsiders: "answer"}))
	require.NoError(t, set(&agentv1.IdentityPolicy{SharedModelKey: "plan_then_app"}))

	got, err = f.clientAs("bob").GetIdentityPolicy(ctx, connect.NewRequest(&agentv1.GetIdentityPolicyRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "app", got.Msg.GetPolicy().GetLinear(), "the second save did not put the first back")
	assert.Equal(t, "answer", got.Msg.GetPolicy().GetSlackOutsiders())
	assert.Equal(t, "plan_then_app", got.Msg.GetPolicy().GetSharedModelKey())
	assert.Equal(t, "refuse", got.Msg.GetPolicy().GetLinearOutsiders(), "what nobody set is its default")
}
