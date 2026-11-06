//go:build integration

package conductor_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/pkg/spec"
)

// secrets: owner is a company secret written as if it belonged to a person. YAML keeps the
// field. A Slack turn still attaches it with no owner.
func TestASlackTurnWithAnOwnedCompanySecretAttachesNoOwner(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/profile.yaml", []byte(`name: podium
display_name: Podium
system_prompt: be Podium
model: claude-opus-5
default_playbook: general
`), 0o600))
	require.NoError(t, os.MkdirAll(dir+"/playbooks", 0o750))
	require.NoError(t, os.WriteFile(dir+"/playbooks/general.yaml", []byte(`image: podium-agent-runtime:dev
system_prompt: answer the question
allowed_tools: [read, grep]
timeout: 15m
slack_channels: [C1]
secrets:
  - name: ADA_NOTE
    target: env
    key: ADA_NOTE
    owner: ada@acme.com
`), 0o600))
	loaded, err := profiles.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "ada@acme.com", loaded.Playbooks["general"].Secrets[0].Owner,
		"the file unmarshals owner; the turn is what drops it")

	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Profiles = profiles.NewLive(loaded)
	})

	require.NoError(t, src.Send(context.Background(), threadInbound("C1/43.1", "use the company note")))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	var sawAda bool
	for _, ref := range fake.Specs()[0].GetSecrets() {
		assert.Empty(t, ref.GetOwner(), "a slack turn attaches no owner")
		if ref.GetName() == "ADA_NOTE" {
			sawAda = true
		}
	}
	assert.True(t, sawAda, "the company secret is still attached, as a global")
}

// A Slack mention from somebody who resolved to a Podium login is that person's turn: their
// personal secrets are attached with them as the owner, exactly as a web chat's would be.
// (Somebody who did not resolve never gets this far — gate_integration_test.go.)
func TestASlackTurnFromAPodiumPersonAttachesTheirPersonalSecrets(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	p := testProfile(t)
	pb := p.Playbooks["general"]
	pb.UserSecrets = []spec.SecretRef{{
		Name: "linear_key", Target: spec.SecretTargetEnv, Key: "LINEAR_KEY",
	}}
	p.Playbooks["general"] = pb
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Profiles = profiles.NewLive(p)
	})

	ev := threadInbound("C1/40.1", "use my linear key")
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	var owner string
	for _, ref := range fake.Specs()[0].GetSecrets() {
		if ref.GetName() == "linear_key" {
			owner = ref.GetOwner()
		}
	}
	assert.Equal(t, testSpeaker, owner, "the speaker's own secret, not a company one")
}

// A playbook that names a server nobody registered fails in the conductor, by name, and
// never asks the control plane for a task.
func TestAMissingMCPServerFailsTheTurnBeforeATask(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)

	p := testProfile(t)
	pb := p.Playbooks["general"]
	pb.MCPServers = []string{"notion"}
	p.Playbooks["general"] = pb
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Profiles = profiles.NewLive(p)
	})

	ev := inbound("C1/41.1", "look at notion")
	require.NoError(t, src.Send(context.Background(), ev))
	waitFor(t, 30*time.Second, "the turn to name the missing server", func() bool {
		for _, rec := range posts(src.Records(), conductor.OutFailure) {
			if strings.Contains(rec.Text, "notion") {
				return true
			}
		}
		return false
	})
	assert.Empty(t, fake.Specs())
}

// A Slack speaker who is a Podium person gets their own MCP server over the unowned row of
// the same name, the way their web chat does.
func TestASlackTurnUsesTheSpeakersOwnMCPServer(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	require.NoError(t, st.InsertMcpServer(ctx, mcp.Server{
		Name: "linear", URL: "https://bot.example/mcp", Enabled: true, Config: "{}",
	}, "local"))
	require.NoError(t, st.InsertMcpServer(ctx, mcp.Server{
		Owner: testSpeaker, Name: "linear", URL: "https://alice.example/mcp", Enabled: true, Config: "{}",
	}, testSpeaker))

	fake := newFakePodium(t)
	src := fakesource.New(conductor.SourceSlack)
	t.Cleanup(src.Close)

	p := testProfile(t)
	pb := p.Playbooks["general"]
	pb.MCPServers = []string{"linear"}
	p.Playbooks["general"] = pb
	startWith(t, st, fake, src, func(o *conductor.Options) {
		o.Profiles = profiles.NewLive(p)
	})

	ev := threadInbound("C1/42.1", "what is assigned to me")
	require.NoError(t, src.Send(ctx, ev))
	waitFor(t, 30*time.Second, "a task to be created", func() bool { return len(fake.Specs()) == 1 })

	brief := decodeBrief(t, fake.Specs()[0])
	require.Len(t, brief.Playbook.MCPServers, 1)
	assert.Equal(t, "https://alice.example/mcp", brief.Playbook.MCPServers[0].URL)
}
