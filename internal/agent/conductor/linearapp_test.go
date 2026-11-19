package conductor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/pkg/spec"
)

// appToken is what the Linear app's token source answers in these tests.
const appToken = "lin_oauth_app_8f2c41d9"

// setVault records the secrets the conductor writes.
type setVault struct {
	podiumv1connect.UnimplementedSecretServiceHandler
	mu   sync.Mutex
	sets map[string][]string
}

func (v *setVault) SetSecret(
	_ context.Context, req *connect.Request[podiumv1.SetSecretRequest],
) (*connect.Response[podiumv1.SetSecretResponse], error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sets[req.Msg.GetName()] = append(v.sets[req.Msg.GetName()], string(req.Msg.GetValue()))
	return connect.NewResponse(&podiumv1.SetSecretResponse{}), nil
}

func (v *setVault) writes(name string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.sets[name]...)
}

// linearAppConductor is a conductor whose Linear app answers token, or err when token is "",
// with a vault that records what it is given and a log the test can read.
func linearAppConductor(t *testing.T, token string) (*Conductor, *setVault, *bytes.Buffer) {
	t.Helper()
	vault := &setVault{sets: map[string][]string{}}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewSecretServiceHandler(vault))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	c := &Conductor{
		podium: podium.New(srv.URL, "agent-token"),
		logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		linearAppToken: func(context.Context) (string, error) {
			if token == "" {
				return "", errors.New("linear: the app was revoked")
			}
			return token, nil
		},
		host: &HostRuntime{Credential: func(context.Context, string) (string, error) { return "sk-model", nil }},
	}
	return c, vault, logs
}

// rowsWithAdasLinear is a registry with Ada's own Linear and a company wiki.
var rowsWithAdasLinear = []mcp.Server{
	{Owner: "ada@acme.com", Name: "linear", URL: mcp.LinearURL, Enabled: true, AuthKind: mcp.AuthOAuth, TokenSecretVersion: 2},
	{Name: "wiki", URL: "https://wiki.example/mcp", Enabled: true, Fallback: true, TokenSecretVersion: 1},
}

// A run as Podium gets Linear signed in as the app, in the place its job named it; the
// registry's Linear is nobody's to spend on it.
func TestARunAsPodiumGetsLinearAsTheApp(t *testing.T) {
	c, vault, logs := linearAppConductor(t, appToken)
	j := assistantJob(profiles.Assistant{MCPServers: []string{"wiki", "linear"}})

	servers, unavailable, err := c.turnMCPServers(t.Context(), j, "", true, rowsWithAdasLinear)
	require.NoError(t, err)
	assert.Empty(t, unavailable)
	require.Len(t, servers, 2)
	assert.Equal(t, "wiki", servers[0].Name)
	assert.Equal(t, mcp.LinearAppServer(), servers[1])
	assert.Empty(t, vault.writes(mcp.LinearAppTokenSecret), "a host turn reads the token itself")

	// A host turn: the token in the runtime's environment, under the variable the brief names.
	h := &hostRun{r: &turnRun{sink: &sink{c: c}}, provider: &BriefProvider{ID: "anthropic", APIKeyEnv: "ANTHROPIC_API_KEY"},
		servers: servers[1:]}
	env, err := h.env(t.Context(), hostPaths{})
	require.NoError(t, err)
	assert.Contains(t, env, mcp.TokenEnv("linear")+"="+appToken)

	// The brief names the server and the variable, not the token.
	playbook := profiles.Playbook{MCPServers: []string{"linear"}}
	sc := skillsConductor(t, "", playbook)
	choice := sc.profiles.Current().Resolve(sc.profiles.Current().Playbooks["coder"], profiles.Override{})
	b := sc.brief(t.Context(), store.Session{ID: "sess_1"}, j, "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, servers, choice)
	encoded, err := b.Encode()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	encoded = string(raw)
	assert.Contains(t, encoded, mcp.LinearURL)
	assert.Contains(t, encoded, mcp.TokenEnv("linear"))
	assert.NotContains(t, encoded, appToken)
	assert.NotContains(t, logs.String(), appToken)
}

// A task container receives the app's token as every MCP credential: a secret the task spec
// names, which the conductor writes once for each token rather than once a run.
func TestATaskRunAsPodiumReceivesTheAppsTokenAsASecret(t *testing.T) {
	c, vault, logs := linearAppConductor(t, appToken)
	j := playbookJob(profiles.Playbook{Name: "investigate", MCPServers: []string{"linear"}})

	servers, unavailable, err := c.turnMCPServers(t.Context(), j, "", true, nil)
	require.NoError(t, err)
	assert.Empty(t, unavailable, "a playbook naming linear has it, though the registry has none")
	require.Equal(t, []mcp.Server{mcp.LinearAppServer()}, servers)
	_, _, err = c.turnMCPServers(t.Context(), j, "", true, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{appToken}, vault.writes(mcp.LinearAppTokenSecret), "the same token is written once")

	sc := skillsConductor(t, "", profiles.Playbook{MCPServers: []string{"linear"}})
	playbook := sc.profiles.Current().Playbooks["coder"]
	choice := sc.profiles.Current().Resolve(playbook, profiles.Override{})
	sp := sc.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, servers, choice, "", "")
	assert.Contains(t, sp.Secrets, spec.SecretRef{
		Name: mcp.LinearAppTokenSecret, Target: spec.SecretTargetEnv, Key: mcp.TokenEnv("linear"),
	})
	assert.NotContains(t, logs.String(), appToken)

	// The secret is the conductor's: it is under the prefix a playbook may not name
	// (profiles.refuseReservedSecrets), so no playbook gets the token without naming linear.
	assert.True(t, strings.HasPrefix(mcp.LinearAppTokenSecret, mcp.SecretPrefix))
}

// A run a person is behind keeps their own Linear, exactly as before.
func TestAPersonsRunKeepsTheirOwnLinear(t *testing.T) {
	c, vault, _ := linearAppConductor(t, appToken)
	j := assistantJob(profiles.Assistant{MCPServers: []string{"linear"}})

	servers, unavailable, err := c.turnMCPServers(t.Context(), j, "ada@acme.com", false, rowsWithAdasLinear)
	require.NoError(t, err)
	assert.Empty(t, unavailable)
	require.Len(t, servers, 1)
	assert.Equal(t, "ada@acme.com", servers[0].Owner)
	assert.Equal(t, mcp.AuthOAuth, servers[0].AuthKind)
	assert.Equal(t, mcp.PersonalTokenSecret("linear"), mcp.CredentialSecret(servers[0]))
	assert.Empty(t, vault.writes(mcp.LinearAppTokenSecret))
}

// With no Linear app, or one Linear revoked, a run as Podium runs without Linear and is told
// why, a playbook included; it never falls back to a person's or the company's Linear.
func TestARunAsPodiumWithNoLinearAppRunsWithoutLinear(t *testing.T) {
	for name, c := range map[string]*Conductor{
		"revoked": func() *Conductor { c, _, _ := linearAppConductor(t, ""); return c }(),
		"none": func() *Conductor {
			c, _, _ := linearAppConductor(t, appToken)
			c.linearAppToken = nil
			return c
		}(),
	} {
		for _, j := range []job{
			assistantJob(profiles.Assistant{MCPServers: []string{"linear", "wiki"}}),
			playbookJob(profiles.Playbook{Name: "investigate", MCPServers: []string{"linear", "wiki"}}),
		} {
			rows := append([]mcp.Server{{Name: "linear", URL: mcp.LinearURL, Enabled: true, TokenSecretVersion: 1}},
				rowsWithAdasLinear...)
			servers, unavailable, err := c.turnMCPServers(t.Context(), j, "", true, rows)
			require.NoError(t, err, name)
			require.Len(t, servers, 1, name)
			assert.Equal(t, "wiki", servers[0].Name, name)
			assert.Equal(t, []BriefUnavailableMCPServer{{Name: "linear", Reason: mcpReasonNoLinearApp}}, unavailable, name)
		}
	}
}

func TestARunAsPodiumThatNamesNoLinearGetsNone(t *testing.T) {
	c, vault, _ := linearAppConductor(t, appToken)
	j := playbookJob(profiles.Playbook{Name: "digest", MCPServers: []string{"wiki"}})
	servers, unavailable, err := c.turnMCPServers(t.Context(), j, "", true, rowsWithAdasLinear)
	require.NoError(t, err)
	assert.Empty(t, unavailable)
	require.Len(t, servers, 1)
	assert.Equal(t, "wiki", servers[0].Name)
	assert.Empty(t, vault.writes(mcp.LinearAppTokenSecret))
}

func TestARunIsPodiumsOnlyWithNobodyBehindIt(t *testing.T) {
	c := &Conductor{}
	ref := &RoutineRef{RoutineID: "rtn_1", RunID: "run_1"}
	assert.False(t, c.runsAsPodium(t.Context(), "ada@acme.com", ref), "a personal routine's run is its owner's")
	assert.False(t, c.runsAsPodium(t.Context(), "", nil), "a turn that is no routine's run")
	assert.False(t, c.runsAsPodium(t.Context(), "", ref), "with no database there is no system routine to be")
}
