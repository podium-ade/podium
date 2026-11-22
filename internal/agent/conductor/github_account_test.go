package conductor

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/podium"
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

// A playbook that pushes as the asker is briefed to redeem a capability, like an App turn,
// and its task carries no copy of the person's token.
func TestAPlaybookUsingTheAskersAccountRedeemsACapability(t *testing.T) {
	c := skillsConductor(t, "", accountPlaybook())
	c.mintSecret, c.gitTaskURL = "conductor-bearer", "http://conductor.test:8090"
	playbook := c.profiles.Current().Playbooks["coder"]
	require.True(t, playbook.UsesGitHubAccount())

	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(t.Context(), store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil, choice)
	require.NotNil(t, b.GitCredentials, "there is no App, and the turn still mints")

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice,
		GitCapabilitySecret("turn_1"), "ada@acme.com")
	for _, ref := range sp.Secrets {
		assert.NotEqual(t, profiles.GitHubTokenSecret, ref.Name, "the person's token never reaches the task")
	}

	capability, err := c.mintCapability("turn_1", GitScope{Owner: "podium-ade", Repos: []string{"podium"}, Login: "ada@acme.com"})
	require.NoError(t, err)
	_, scope, err := c.parseCapability(capability)
	require.NoError(t, err)
	assert.Equal(t, "ada@acme.com", scope.Login, "the person is inside the signature")
}

func TestAPlaybookUsingTheAskersAccountMustListItsRepos(t *testing.T) {
	playbook := accountPlaybook()
	playbook.Repos = nil
	c := skillsConductor(t, "", playbook)
	c.mintSecret, c.gitTaskURL = "conductor-bearer", "http://conductor.test:8090"
	_, err := c.provisionGitCapability(t.Context(), "turn_1", c.profiles.Current().Playbooks["coder"], "ada@acme.com", true)
	require.ErrorContains(t, err, "must list its repos")
}

func TestGitHubPersonaNeedsAPerson(t *testing.T) {
	c := skillsConductor(t, "", accountPlaybook())
	playbook := c.profiles.Current().Playbooks["coder"]

	got, err := c.githubPersona(t.Context(), "", playbook, false)
	require.ErrorContains(t, err, "nobody in Podium asked")
	assert.ErrorContains(t, err, "no GitHub App is connected to Podium", "it says why the App cannot stand in")
	assert.Nil(t, got)

	got, err = c.githubPersona(t.Context(), "ada@acme.com", profiles.Playbook{}, false)
	require.NoError(t, err)
	assert.Nil(t, got, "a playbook that does not use the account keeps its own persona")
}

func TestGitHubPersonaIsTheNoreplyAddressGitHubLinks(t *testing.T) {
	got := githubPersonaOf(store.GitHubAccount{GitHubID: 4242, GitHubLogin: "ada-gh", Name: "Ada L"})
	assert.Equal(t, &BriefGit{Name: "Ada L", Email: "4242+ada-gh@users.noreply.github.com"}, got)

	got = githubPersonaOf(store.GitHubAccount{GitHubID: 7, GitHubLogin: "bob"})
	assert.Equal(t, "bob", got.Name)
}

// A turn with nobody behind it writes as the App, and when the App cannot stand in the
// refusal says why rather than sending a routine to the web chat.
func TestATurnWithNobodyBehindItSaysWhyTheAppCannotStandIn(t *testing.T) {
	withApp := func(playbook profiles.Playbook) *Conductor {
		c := skillsConductor(t, "", playbook)
		c.mintSecret, c.gitTaskURL = "conductor-bearer", "http://conductor.test:8090"
		c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		require.NoError(t, c.SetGitHub(&github.Client{}))
		return c
	}
	coder := func(c *Conductor) profiles.Playbook { return c.profiles.Current().Playbooks["coder"] }

	t.Run("the App stands in", func(t *testing.T) {
		c := withApp(accountPlaybook())
		for _, asPodium := range []bool{false, true} {
			got, err := c.githubPersona(t.Context(), "", coder(c), asPodium)
			require.NoError(t, err)
			assert.Nil(t, got, "nil is the App")
		}
	})

	t.Run("a playbook with no repositories", func(t *testing.T) {
		playbook := accountPlaybook()
		playbook.Repos = nil
		c := withApp(playbook)
		_, err := c.githubPersona(t.Context(), "", coder(c), false)
		require.ErrorContains(t, err, "lists no repositories")
		_, err = c.githubPersona(t.Context(), "", coder(c), true)
		require.ErrorContains(t, err, "this run acts as Podium")
		assert.ErrorContains(t, err, "lists no repositories")
	})

	t.Run("repositories of two owners", func(t *testing.T) {
		playbook := accountPlaybook()
		playbook.Repos = append(playbook.Repos, profiles.Repo{Name: "other", URL: "https://github.com/someone-else/other"})
		c := withApp(playbook)
		_, err := c.githubPersona(t.Context(), "", coder(c), true)
		require.ErrorContains(t, err, "more than one GitHub account")
	})

	t.Run("no App", func(t *testing.T) {
		c := skillsConductor(t, "", accountPlaybook())
		_, err := c.githubPersona(t.Context(), "", coder(c), true)
		require.ErrorContains(t, err, "this run acts as Podium, which writes to GitHub as its GitHub App")
		assert.ErrorContains(t, err, "no GitHub App is connected")
	})

	t.Run("the deployment's name", func(t *testing.T) {
		c := skillsConductor(t, "", accountPlaybook())
		c.brand.at, c.brand.last = time.Now(), branding{Deployment: "Acme AI"}
		c.podium = podium.New("http://podium.test", "agent-token")
		_, err := c.githubPersona(t.Context(), "", coder(c), false)
		require.ErrorContains(t, err, "nobody in Acme AI asked")
		assert.NotContains(t, err.Error(), "Podium")
	})
}
