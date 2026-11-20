//go:build integration

package conductor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

// personalSecrets is a control plane that knows which personal secrets each person saved.
type personalSecrets struct {
	podiumv1connect.UnimplementedSecretServiceHandler
	saved map[string][]string // owner → secret names
}

func (p *personalSecrets) ListSecrets(
	_ context.Context, req *connect.Request[podiumv1.ListSecretsRequest],
) (*connect.Response[podiumv1.ListSecretsResponse], error) {
	out := &podiumv1.ListSecretsResponse{}
	for _, name := range p.saved[req.Msg.GetOwner()] {
		out.Secrets = append(out.Secrets, &podiumv1.Secret{Name: name, Owner: req.Msg.GetOwner(), Version: 1})
	}
	return connect.NewResponse(out), nil
}

// With the identity policy's shared_model_key at "plan_then_app", a turn of a shared
// conversation spends the subscription of the person who asked it, and the company's
// credential for anybody without one — never a personal API key, and never a Claude plan off
// the chat host. At the default, every turn of it spends the company's, as before.
func TestASharedConversationSpendsTheAskersOwnPlanWhenThePolicySaysSo(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	const (
		claudePlan = "claude-plan@podium.test"
		gptPlan    = "gpt-plan@podium.test"
		gptKey     = "gpt-key@podium.test"
		nothing    = "nothing@podium.test"
	)
	secrets := &personalSecrets{saved: map[string][]string{
		claudePlan: {profiles.PersonalSecretFor(profiles.ProviderAnthropic)},
		gptPlan:    {profiles.PersonalSecretFor(profiles.ProviderOpenAI)},
		gptKey:     {profiles.PersonalSecretFor(profiles.ProviderOpenAI)},
	}}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewSecretServiceHandler(secrets))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, row := range []struct{ provider, owner string }{
		{profiles.ProviderAnthropic, claudePlan},
		{profiles.ProviderOpenAI, gptPlan},
	} {
		require.NoError(t, st.PutSetting(ctx, profiles.ProviderSettingKey(row.provider, row.owner),
			map[string]string{"auth_kind": "oauth"}))
	}
	slack := store.Session{SourceKind: conductor.SourceSlack, SourceKey: "slack:C1:1.1"}
	owner := func(login, agent string, onChatHost bool) string {
		return conductor.TurnCredentialOwnerForTest(ctx, st, srv.URL, slack, login, agent, onChatHost, true)
	}

	assert.Empty(t, owner(gptPlan, profiles.AgentOpenAI, false), "by default a shared conversation spends the company's")

	_, err := st.PutIdentityPolicy(ctx, store.IdentityPolicy{SharedModelKey: store.IdentityPlanThenApp})
	require.NoError(t, err)
	assert.Equal(t, gptPlan, owner(gptPlan, profiles.AgentOpenAI, false), "a ChatGPT plan, in a task or on the host")
	assert.Equal(t, claudePlan, owner(claudePlan, profiles.AgentClaude, true), "a Claude plan on the chat host")
	assert.Empty(t, owner(claudePlan, profiles.AgentClaude, false), "a Claude plan is spent only on the chat host")
	assert.Empty(t, owner(gptKey, profiles.AgentOpenAI, false), "a personal API key is not a plan")
	assert.Empty(t, owner(nothing, profiles.AgentOpenAI, false), "nothing of their own: the company's")
	assert.Empty(t, owner("", profiles.AgentOpenAI, false), "nobody behind the turn: the company's")
	assert.Empty(t, owner(gptPlan, profiles.AgentClaude, true), "a plan for another provider is not this turn's")
}
