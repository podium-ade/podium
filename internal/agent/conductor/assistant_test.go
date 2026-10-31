package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/pkg/spec"
)

// TestTheAssistantJobKnowsWhatItIsApartFromWhereItRuns: the assistant is the assistant
// wherever it is placed, and only the host placement is a process here.
func TestTheAssistantJobKnowsWhatItIsApartFromWhereItRuns(t *testing.T) {
	a := (&profiles.Profile{}).Assistant()

	host := assistantJob(a, profiles.RunsOnHost)
	assert.True(t, host.assistant)
	assert.True(t, host.onHost)
	assert.Empty(t, host.runsOn, "a host turn is placed by being a child of this process")

	for _, where := range []string{profiles.RunsOnNodes, "modal", "boat"} {
		j := assistantJob(a, where)
		assert.True(t, j.assistant, where)
		assert.False(t, j.onHost, where)
		assert.Equal(t, where, j.runsOn)
		assert.Equal(t, hostTools, j.allowedTools, "the tool list does not change with the placement")
		assert.Equal(t, "podium agent, the assistant", j.provenance())
	}

	pb := playbookJob(profiles.Playbook{Name: "coder"})
	assert.False(t, pb.assistant)
	assert.False(t, pb.onHost)
}

// TestWhereTheAssistantRunsWithoutASetting is the install's default: the option, then the
// host when there is one, then nowhere.
func TestWhereTheAssistantRunsWithoutASetting(t *testing.T) {
	ctx := t.Context()
	assert.Equal(t, "", (&Conductor{}).assistantPlacement(ctx), "no host and nowhere else: no assistant")
	assert.Equal(t, profiles.RunsOnHost, (&Conductor{host: &HostRuntime{}}).assistantPlacement(ctx))
	assert.Equal(t, "modal", (&Conductor{host: &HostRuntime{}, assistantRunsOn: "modal"}).assistantPlacement(ctx),
		"the option wins over the host")
	assert.Equal(t, profiles.RunsOnNodes, (&Conductor{assistantRunsOn: profiles.RunsOnNodes}).assistantPlacement(ctx),
		"a conductor with no host runtime can still have an assistant")
	assert.True(t, (&Conductor{assistantRunsOn: "boat"}).hasAssistant(ctx))
	assert.False(t, (&Conductor{}).hasAssistant(ctx))
}

// TestEachPlacementReachesTheTurnSurfaceFromItsOwnVantagePoint: loopback from the host, the
// task URL from a node, the sandbox URL from a provider — and the task URL again when no
// sandbox URL is set, which is right for a provider inside the operator's network.
func TestEachPlacementReachesTheTurnSurfaceFromItsOwnVantagePoint(t *testing.T) {
	c := &Conductor{
		host:           &HostRuntime{TurnURL: "http://127.0.0.1:8090"},
		gitTaskURL:     "http://host.docker.internal:8090",
		sandboxTaskURL: "https://surface.example.ts.net",
	}
	assert.Equal(t, "http://127.0.0.1:8090", c.turnSurfaceURL(profiles.RunsOnHost))
	assert.Equal(t, "http://host.docker.internal:8090", c.turnSurfaceURL(profiles.RunsOnNodes))
	assert.Equal(t, "http://host.docker.internal:8090", c.turnSurfaceURL(""), "the fleet's nodes")
	assert.Equal(t, "https://surface.example.ts.net", c.turnSurfaceURL("modal"))
	assert.Equal(t, "https://surface.example.ts.net", c.turnSurfaceURL("boat"))

	c.sandboxTaskURL = ""
	assert.Equal(t, "http://host.docker.internal:8090", c.turnSurfaceURL("modal"))
	c.host = nil
	assert.Empty(t, c.turnSurfaceURL(profiles.RunsOnHost), "no host runtime, no loopback surface")
}

// TestAnAssistantTasksBriefDelegatesThroughItsPlacementsSurface: the delegation block and the
// pull request's MCP server point where a turn on that placement can reach, and the brief
// says it is the assistant without claiming to be on the host.
func TestAnAssistantTasksBriefDelegatesThroughItsPlacementsSurface(t *testing.T) {
	c := &Conductor{gitTaskURL: "http://host.docker.internal:8090", sandboxTaskURL: "https://surface.example.ts.net/"}
	menu := []DelegablePlaybook{{Name: "reviewer", GitHub: true}}

	b := &Brief{Source: BriefSource{Kind: SourceGitHub}}
	c.assistantBrief(b, "modal", menu, nil, nil)
	assert.True(t, b.Assistant)
	assert.Empty(t, b.RunsOn, "a task's brief leaves runs_on absent: it has a container and an inbox")
	require.NotNil(t, b.Delegation)
	assert.Equal(t, "https://surface.example.ts.net/", b.Delegation.URL)
	assert.Equal(t, TurnTokenEnv, b.Delegation.TokenEnv)
	require.Len(t, b.Playbook.MCPServers, 1)
	assert.Equal(t, PullRequestMCPName, b.Playbook.MCPServers[0].Name)
	assert.Equal(t, "https://surface.example.ts.net/mcp/pull-request", b.Playbook.MCPServers[0].URL)

	nodes := &Brief{Source: BriefSource{Kind: SourceChat}}
	c.assistantBrief(nodes, profiles.RunsOnNodes, menu, nil, nil)
	require.NotNil(t, nodes.Delegation)
	assert.Equal(t, "http://host.docker.internal:8090", nodes.Delegation.URL)
	assert.Empty(t, nodes.Playbook.MCPServers, "the pull-request server is a pull request's alone")

	lost := &Brief{Source: BriefSource{Kind: SourceChat}}
	(&Conductor{}).assistantBrief(lost, "modal", menu, nil, nil)
	assert.True(t, lost.Assistant)
	assert.Nil(t, lost.Delegation, "a turn with no way back here is not offered delegation it cannot use")
}

func TestThePullRequestMCPPathMatchesTheOneMounted(t *testing.T) {
	// ghreview.MCPPath, which agent.go mounts. The conductor cannot import the source it
	// serves, so the two are held together here.
	assert.Equal(t, "/mcp/pull-request", pullRequestMCPPath)
}

// TestAnAssistantTaskIsTheAssistantAndNothingMore: an image, a wall clock, a placement and
// the turn's token. No session workspace, so nothing is restored or snapshotted; no sidecars;
// a warm window so the next message is injected.
func TestAnAssistantTaskIsTheAssistantAndNothingMore(t *testing.T) {
	c := &Conductor{profiles: profiles.NewLive(&profiles.Profile{Name: "podium", Model: "claude-opus-5"})}
	profile := c.profiles.Current()
	a := profile.Assistant()
	a.Timeout = 10 * time.Minute
	j := assistantJob(a, "modal")
	choice := j.choose(profile, profiles.Override{})

	s := c.assistantTaskSpec(chatSource{}, j, "encoded", InboundEvent{}, nil, nil, choice, TurnTokenSecret("turn_1"), "")

	assert.Equal(t, profiles.DefaultRuntimeImage(), s.Image)
	assert.Equal(t, "encoded", s.Env[BriefEnv])
	require.NotNil(t, s.Sandbox)
	assert.Equal(t, "modal", s.Sandbox.Provider)
	assert.Equal(t, spec.Duration(10*time.Minute+assistantTaskGrace), s.Timeout)
	assert.Empty(t, s.WorkspaceSession, "the assistant has no workspace to keep")
	assert.Empty(t, s.WorkspaceRepo)
	assert.Equal(t, spec.Duration(DefaultWorkspaceWarm), s.WorkspaceWarm)
	assert.Empty(t, s.Sidecars)
	assert.Nil(t, s.Expose)
	assert.Equal(t, 1, s.MaxAttempts)
	assert.False(t, s.RetryOnNodeLoss)

	var token *spec.SecretRef
	for i, ref := range s.Secrets {
		if ref.Name == TurnTokenSecret("turn_1") {
			token = &s.Secrets[i]
		}
	}
	require.NotNil(t, token, "the delegation token travels as a secret, never in the spec's env")
	assert.Equal(t, TurnTokenEnv, token.Key)
	assert.NotContains(t, s.Env, TurnTokenEnv)

	nodes := c.assistantTaskSpec(chatSource{}, assistantJob(a, profiles.RunsOnNodes), "e", InboundEvent{}, nil, nil, choice, "", "")
	assert.Nil(t, nodes.Sandbox, "the fleet's own nodes take no sandbox block")

	c.assistantImage = "registry.example/assistant:1"
	c.assistantWarm = -1
	custom := c.assistantTaskSpec(chatSource{}, j, "e", InboundEvent{}, nil, nil, choice, "", "")
	assert.Equal(t, "registry.example/assistant:1", custom.Image)
	assert.Zero(t, custom.WorkspaceWarm, "a negative warm window leaves as soon as the turn ends")
	for _, ref := range custom.Secrets {
		assert.NotEqual(t, TurnTokenEnv, ref.Key, "no token, no token secret")
	}
}

func TestATurnTokenSecretIsReservedForTheConductor(t *testing.T) {
	assert.Equal(t, profiles.TurnTokenPrefix+"turn_1", TurnTokenSecret("turn_1"))
}
