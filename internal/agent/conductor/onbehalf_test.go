package conductor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

// A request is named by the name its account shows and where it was asked, never by whether
// the person is in Podium.
func TestTheRequesterIsNamedByTheirNameAndTheSurface(t *testing.T) {
	c := &Conductor{}
	slack := &kindSource{SourceSlack}
	outsider := InboundEvent{Author: "Dana", SpeakerProvider: store.ProviderSlack, SpeakerID: "T1/U9"}

	r := c.requesterOf(t.Context(), "", slack, outsider)
	assert.Equal(t, "Dana (via Slack)", r.label())
	assert.Equal(t, "slack:T1/U9", r.Account)

	r = c.requesterOf(t.Context(), "ada@acme.com", slack, InboundEvent{Author: "Ada L"})
	assert.Equal(t, "ada@acme.com (via Slack)", r.label(), "a Podium person is named as Podium knows them")

	r = c.requesterOf(t.Context(), "ada@acme.com", &kindSource{SourceChat}, InboundEvent{})
	assert.Equal(t, "ada@acme.com", r.label(), "the web chat is no surface to name")

	assert.Equal(t, requester{}, c.requesterOf(t.Context(), "", &kindSource{SourceLinear}, InboundEvent{}),
		"a run nobody spoke in, a routine's, is for nobody")
}

func TestTheLinearNoteNamesTheDeploymentAndTheSurface(t *testing.T) {
	note := linearOnBehalfNote("Acme AI", requester{Name: "Dana", Via: "Slack"})
	assert.Contains(t, note, "`Created by Acme AI on behalf of Dana (via Slack).`")
	assert.Contains(t, note, "as the Acme AI Linear app")
	assert.NotContains(t, note, "Podium")
	assert.NotContains(t, note, "not in")
}

// recordedAudit is the control plane's audit log, for a conductor under unit test.
type recordedAudit struct {
	mu      sync.Mutex
	entries []*podiumv1.RecordAuditRequest
}

func (a *recordedAudit) RecordAudit(
	_ context.Context, req *connect.Request[podiumv1.RecordAuditRequest],
) (*connect.Response[podiumv1.RecordAuditResponse], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, req.Msg)
	return connect.NewResponse(&podiumv1.RecordAuditResponse{}), nil
}

func onBehalfConductor(t *testing.T) (*Conductor, *recordedAudit) {
	t.Helper()
	audit := &recordedAudit{}
	mux := http.NewServeMux()
	mux.Handle(podiumv1connect.NewAuditServiceHandler(audit))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Conductor{
		podium: podium.New(srv.URL, "agent-token"),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.brand.at, c.brand.last = time.Now(), branding{Deployment: "Acme AI"}
	return c, audit
}

func TestATurnOnTheLinearAppSaysWhoseRequestItIsAndIsAudited(t *testing.T) {
	dana := requester{Name: "Dana", Via: "Slack", Account: "slack:T1/U9"}

	t.Run("the Linear app", func(t *testing.T) {
		c, audit := onBehalfConductor(t)
		b := &Brief{Instruction: "create a ticket for the flaky login"}
		c.withOnBehalfOf(t.Context(), b, dana, "", "chat_1", "turn_1", []mcp.Server{mcp.LinearAppServer()}, nil)

		assert.Contains(t, b.Instruction, "`Created by Acme AI on behalf of Dana (via Slack).`")
		require.Len(t, audit.entries, 1)
		e := audit.entries[0]
		assert.Equal(t, auditOnBehalf, e.GetAction())
		assert.Equal(t, "slack:T1/U9", e.GetActor(), "somebody with no login is the account that asked")
		assert.Equal(t, "chat_1", e.GetSubject())
		assert.JSONEq(t, `{"requester":"Dana","via":"Slack","account":"slack:T1/U9","turn_id":"turn_1","apps":["linear"]}`,
			e.GetDetailsJson())
	})

	t.Run("their own Linear", func(t *testing.T) {
		c, audit := onBehalfConductor(t)
		b := &Brief{Instruction: "create a ticket"}
		own := mcp.Server{Name: mcp.LinearName, URL: mcp.LinearURL}
		c.withOnBehalfOf(t.Context(), b, dana, "dana@acme.com", "chat_1", "turn_1", []mcp.Server{own}, nil)
		assert.Equal(t, "create a ticket", b.Instruction, "a person's own Linear already names them")
		assert.Empty(t, audit.entries)
	})

	t.Run("the GitHub App", func(t *testing.T) {
		c, audit := onBehalfConductor(t)
		b := &Brief{Instruction: "fix it", GitCredentials: &BriefGitCredentials{URL: "u", TokenEnv: "T"}}
		c.withOnBehalfOf(t.Context(), b, dana, "dana@acme.com", "chat_1", "turn_1", nil, nil)
		require.Len(t, audit.entries, 1)
		assert.Equal(t, "dana@acme.com", audit.entries[0].GetActor())
		assert.Contains(t, audit.entries[0].GetDetailsJson(), `"apps":["github"]`)
	})

	t.Run("nobody asked", func(t *testing.T) {
		c, audit := onBehalfConductor(t)
		b := &Brief{Instruction: "triage"}
		c.withOnBehalfOf(t.Context(), b, requester{}, "", "chat_1", "turn_1", []mcp.Server{mcp.LinearAppServer()}, nil)
		assert.Equal(t, "triage", b.Instruction, "a routine's run writes as the organization for nobody")
		assert.Empty(t, audit.entries)
	})
}

// Somebody who is not a Podium person, writing to GitHub through the App, is named the same way.
func TestTheGitHubNoteNamesAnOutsiderByTheSurface(t *testing.T) {
	c, _ := onBehalfConductor(t)
	b := &Brief{Instruction: "fix it", GitCredentials: &BriefGitCredentials{URL: "u", TokenEnv: "T"}}
	c.withRequestedBy(t.Context(), b, "", requester{Name: "Dana", Via: "Slack"}, nil)
	assert.Equal(t, "Dana (via Slack)", b.RequestedBy)
	assert.Contains(t, b.Instruction, "`Opened by Acme AI on behalf of Dana (via Slack).`")
}
