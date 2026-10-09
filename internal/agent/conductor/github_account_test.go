package conductor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

func accountPlaybook() profiles.Playbook {
	return profiles.Playbook{
		Repos: []profiles.Repo{{Name: "podium", URL: "https://github.com/podium-ade/podium", DefaultBranch: "main"}},
		UserSecrets: []spec.SecretRef{{
			Name: profiles.GitHubTokenSecret, Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN",
		}},
	}
}

// A playbook that pushes as the asker gets their token and no App capability, which the
// runtime would otherwise prefer.
func TestAPlaybookUsingTheAskersAccountGetsNoAppCapability(t *testing.T) {
	c := skillsConductor(t, "", accountPlaybook())
	c.github = &github.Client{}
	playbook := c.profiles.Current().Playbooks["coder"]
	require.True(t, playbook.UsesGitHubAccount())

	name, err := c.provisionGitCapability(t.Context(), "turn_1", playbook)
	require.NoError(t, err)
	assert.Empty(t, name)

	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(t.Context(), store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil, choice)
	assert.Nil(t, b.GitCredentials)

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "ada@acme.com")
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name: profiles.GitHubTokenSecret, Target: spec.SecretTargetEnv, Key: "GITHUB_TOKEN", Owner: "ada@acme.com",
	})
}

func TestGitHubPersonaNeedsAPerson(t *testing.T) {
	c := skillsConductor(t, "", accountPlaybook())
	playbook := c.profiles.Current().Playbooks["coder"]

	got, err := c.githubPersona(t.Context(), "", playbook)
	require.ErrorContains(t, err, "no Podium user")
	assert.Nil(t, got)

	got, err = c.githubPersona(t.Context(), "ada@acme.com", profiles.Playbook{})
	require.NoError(t, err)
	assert.Nil(t, got, "a playbook that does not use the account keeps its own persona")
}

func TestGitHubPersonaIsTheNoreplyAddressGitHubLinks(t *testing.T) {
	got := githubPersonaOf(store.GitHubAccount{GitHubID: 4242, GitHubLogin: "ada-gh", Name: "Ada L"})
	assert.Equal(t, &BriefGit{Name: "Ada L", Email: "4242+ada-gh@users.noreply.github.com"}, got)

	got = githubPersonaOf(store.GitHubAccount{GitHubID: 7, GitHubLogin: "bob"})
	assert.Equal(t, "bob", got.Name)
}
