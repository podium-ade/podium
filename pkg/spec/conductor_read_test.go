package spec

import "testing"

func TestConductorMayReadOnlyItsOwnNames(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		want        bool
	}{
		{"podium.agent.anthropic_api_key", "", true},
		{"podium.agent.oauth.xai.refresh_token", "", true},
		{"podium.agent.mcp.linear.oauth", "", true},
		{"COMPANY_TOKEN", "", false},
		{"github.token", "", false},
		{"github.token", "ada@acme.com", true},
		{"mcp.linear_token", "ada@acme.com", true},
		{"mcp.linear.oauth", "ada@acme.com", true},
		{"ADA_NOTE", "ada@acme.com", false},
		{"podium.agent.anthropic_api_key", "ada@acme.com", false},
	} {
		if got := ConductorMayRead(tc.name, tc.owner); got != tc.want {
			t.Errorf("ConductorMayRead(%q, %q) = %v, want %v", tc.name, tc.owner, got, tc.want)
		}
	}
}
