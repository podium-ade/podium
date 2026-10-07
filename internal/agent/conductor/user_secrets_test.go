package conductor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// A signed-in turn attaches that person's secrets, with the owner set, and that person's
// MCP credential. A company secret stays global. The same build with no person omits the
// personal names: a Slack turn must not mount them.
func TestASignedInTurnAttachesThatPersonsSecretsAndMCPCredential(t *testing.T) {
	playbook := profiles.Playbook{
		Secrets: []spec.SecretRef{{
			Name: "company_webhook", Target: spec.SecretTargetEnv, Key: "HOOK",
		}},
		UserSecrets: []spec.SecretRef{{
			Name: "linear_key", Target: spec.SecretTargetEnv, Key: "LINEAR_KEY",
		}},
		MCPServers: []string{"linear"},
	}
	c := skillsConductor(t, "", playbook)
	playbook = c.profiles.Current().Playbooks["coder"]
	aliceSrv := registered("linear", true, 2)
	aliceSrv.Owner = "alice@acme.com"
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil,
		[]mcp.Server{aliceSrv}, choice, "", "alice@acme.com")
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name: "linear_key", Target: spec.SecretTargetEnv, Key: "LINEAR_KEY", Owner: "alice@acme.com",
	})
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name: "company_webhook", Target: spec.SecretTargetEnv, Key: "HOOK",
	})
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name: mcp.PersonalTokenSecret("alice@acme.com", "linear"), Target: spec.SecretTargetEnv, Key: mcp.TokenEnv("linear"),
	})
	for _, ref := range sp.Secrets {
		assert.NotEqual(t, mcp.TokenSecret("linear"), ref.Name)
	}

	none, err := userSecretRefs("", playbook.UserSecrets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "there is no person")
	assert.Nil(t, none)

	bare := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil,
		nil, choice, "", "")
	for _, ref := range bare.Secrets {
		assert.NotEqual(t, "linear_key", ref.Name)
		assert.Empty(t, ref.Owner)
	}
}

// A company secret keeps no owner, even when the playbook file unmarshalled one. Only
// user_secrets are personal, and those take the turn's login.
func TestCompanySecretsStayGlobalWhenTheFileNamesAnOwner(t *testing.T) {
	playbook := profiles.Playbook{
		Secrets: []spec.SecretRef{{
			Name: "ADA_NOTE", Target: spec.SecretTargetEnv, Key: "ADA_NOTE", Owner: "ada@acme.com",
		}},
		UserSecrets: []spec.SecretRef{{
			Name: "linear_key", Target: spec.SecretTargetEnv, Key: "LINEAR_KEY",
		}},
	}
	c := skillsConductor(t, "", playbook)
	playbook = c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	slack := c.taskSpec(kindSource{SourceSlack}, playbook, "encoded-brief", InboundEvent{}, nil,
		nil, choice, "", "")
	var sawAda bool
	for _, ref := range slack.Secrets {
		assert.Empty(t, ref.Owner)
		if ref.Name == "ADA_NOTE" {
			sawAda = true
		}
		assert.NotEqual(t, "linear_key", ref.Name)
	}
	assert.True(t, sawAda)

	signedIn := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil,
		nil, choice, "", "ada@acme.com")
	assert.Contains(t, signedIn.Secrets, spec.SecretRef{
		Name: "ADA_NOTE", Target: spec.SecretTargetEnv, Key: "ADA_NOTE",
	})
	assert.Contains(t, signedIn.Secrets, spec.SecretRef{
		Name: "linear_key", Target: spec.SecretTargetEnv, Key: "LINEAR_KEY", Owner: "ada@acme.com",
	})
}

func TestASlackTurnHasNoPersonEvenWhenTheSessionLooksLikeAChat(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{})
	sess := store.Session{SourceKind: SourceChat, SourceKey: "chat:chat_1"}
	assert.Empty(t, c.asker(context.Background(), kindSource{SourceSlack}, sess))
	assert.Empty(t, c.asker(context.Background(), kindSource{SourceLinear}, sess))
	assert.Empty(t, c.asker(context.Background(), kindSource{SourceGitHub}, sess))
	assert.Empty(t, c.asker(context.Background(), kindSource{KindDev}, sess))
}

func TestPersonalSecretNamesReservedForTheConductorAreRefused(t *testing.T) {
	_, err := userSecretRefs("alice@acme.com", []spec.SecretRef{{Name: "podium.agent.github_token"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podium.agent.github_token")

	_, err = userSecretRefs("alice@acme.com", []spec.SecretRef{{Name: profiles.MemoryKeySecret}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), profiles.MemoryKeySecret)
}

// Two registrations of the same server name stay on different secrets, and neither is the
// bot's secret. The owner is hashed because a login is an email.
func TestTwoPeopleDoNotShareAnMCPTokenSecret(t *testing.T) {
	alice := mcp.PersonalTokenSecret("alice@acme.com", "linear")
	bob := mcp.PersonalTokenSecret("bob@acme.com", "linear")
	assert.NotEqual(t, alice, bob)
	assert.NotEqual(t, alice, mcp.TokenSecret("linear"))
	assert.NotEqual(t, bob, mcp.TokenSecret("linear"))
	assert.NotContains(t, alice, "@")
	assert.Equal(t, alice, mcp.CredentialSecret(mcp.Server{Owner: "alice@acme.com", Name: "linear"}))
	assert.Equal(t, mcp.TokenSecret("linear"), mcp.CredentialSecret(mcp.Server{Name: "linear"}))
}

// The registry filter is what a turn uses. Alice's row is not the bot's row, and a person
// with no row fails by name rather than falling through to the bot.
func TestMCPResolutionFollowsTheTurnsPerson(t *testing.T) {
	bot := registered("linear", true, 1)
	bot.URL = "https://bot.example/mcp"
	alice := registered("linear", true, 2)
	alice.Owner = "alice@acme.com"
	alice.URL = "https://alice.example/mcp"
	bob := registered("linear", true, 3)
	bob.Owner = "bob@acme.com"
	rows := []mcp.Server{bot, alice, bob}

	got, err := resolveMCPServers("alice@acme.com", []string{"linear"}, rows)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "https://alice.example/mcp", got[0].URL)
	assert.Equal(t, "alice@acme.com", got[0].Owner)

	slack, err := resolveMCPServers("", []string{"linear"}, rows)
	require.NoError(t, err)
	require.Len(t, slack, 1)
	assert.Equal(t, "https://bot.example/mcp", slack[0].URL)
	assert.Empty(t, slack[0].Owner)

	_, err = resolveMCPServers("carol@acme.com", []string{"notion"}, rows)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"notion"`)
}

type kindSource struct{ kind string }

func (k kindSource) Kind() string              { return k.kind }
func (kindSource) Events() <-chan InboundEvent { return nil }
func (kindSource) FetchTranscript(context.Context, string) ([]BriefEntry, error) {
	return nil, nil
}
func (kindSource) Post(context.Context, string, Outbound) (string, error) { return "", nil }
func (kindSource) Edit(context.Context, string, string, Outbound) error   { return nil }
func (kindSource) Attach(context.Context, string, Attachment) error       { return nil }
func (kindSource) React(context.Context, string, Reaction) error          { return nil }
