package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentapi "github.com/podium-ade/podium/internal/agent/api"
	"github.com/podium-ade/podium/internal/agent/ghreview"
)

func TestTheSurfaceRoutesAreTheConductors(t *testing.T) {
	if legacyTurnSurfaceRoutes[2] != ghreview.MCPPath {
		t.Fatalf("server proxies %q, conductor serves %q", legacyTurnSurfaceRoutes[2], ghreview.MCPPath)
	}
	if turnSurfaceRoutes[3] != agentapi.PullRequestMCPRESTPath {
		t.Fatalf("server proxies %q, conductor serves %q", turnSurfaceRoutes[3], agentapi.PullRequestMCPRESTPath)
	}
	if !strings.HasPrefix(turnSurfaceRoutes[0], agentapi.TurnRESTPrefix) {
		t.Fatalf("resources outside %s", agentapi.TurnRESTPrefix)
	}
}

// The turn's own credential reaches the conductor; nothing of the server's is added, and a
// caller cannot pose as the server's word about a person.
func TestTheTurnSurfaceProxyCarriesTheTurnsCredentialOnly(t *testing.T) {
	var got http.Header
	var path string
	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer conductor.Close()

	h, err := NewTurnSurfaceProxy(conductor.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, TurnSurfacePrefix+"/podium.agent.v1.TurnService/Delegate", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer turn-credential")
	req.Header.Set(AgentLoginHeader, "admin@example.com")
	req.Header.Set(AgentScopeHeader, agentScopeAll)
	req.Header.Set(AgentInfraHeader, "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if path != "/podium.agent.v1.TurnService/Delegate" {
		t.Fatalf("path %q", path)
	}
	if a := got.Get("Authorization"); a != "Bearer turn-credential" {
		t.Fatalf("authorization %q", a)
	}
	for _, h := range []string{AgentLoginHeader, AgentScopeHeader, AgentInfraHeader} {
		if v := got.Get(h); v != "" {
			t.Fatalf("%s reached the conductor: %q", h, v)
		}
	}
}

// Only the surface's own routes are forwarded: the conductor's operator API is on the same
// listener, and nothing under the prefix may reach it.
func TestTheTurnSurfaceProxyForwardsItsRoutesAndNothingElse(t *testing.T) {
	var forwarded []string
	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer conductor.Close()
	h, err := NewTurnSurfaceProxy(conductor.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/turn-surface/v1/delegations":                                                http.StatusOK,
		"/turn-surface/v1/delegations/dlg_1/cancel":                                   http.StatusOK,
		"/turn-surface/v1/git-tokens":                                                 http.StatusOK,
		"/turn-surface/v1/mcp/pull-request":                                           http.StatusOK,
		"/turn-surface/v1/chats":                                                      http.StatusNotFound,
		"/turn-surface/v1/delegations/../../podium.agent.v1.AgentService/ListChats":   http.StatusNotFound,
		"/turn-surface/podium.agent.v1.TurnService/Delegate":                          http.StatusOK,
		"/turn-surface/podium.agent.v1.GitCredentialService/MintToken":                http.StatusOK,
		"/turn-surface/mcp/pull-request":                                              http.StatusOK,
		"/turn-surface/podium.agent.v1.AgentService/ListChats":                        http.StatusNotFound,
		"/turn-surface/metrics":                                                       http.StatusNotFound,
		"/turn-surface/mcp/pull-request/../../podium.agent.v1.AgentService/ListChats": http.StatusNotFound,
		"/turn-surface/podium.agent.v1.TurnService/../AgentService/ListChats":         http.StatusNotFound,
		"/turn-surface/podium.agent.v1.TurnService//Delegate":                         http.StatusNotFound,
		"/turn-surface/podium.agent.v1.TurnServiceX/Delegate":                         http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
	for _, p := range forwarded {
		if !isTurnSurfaceRoute(p) {
			t.Errorf("forwarded %s", p)
		}
	}
	if len(forwarded) != 7 {
		t.Errorf("forwarded %v", forwarded)
	}
}
