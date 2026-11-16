package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/tickets"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeIssues answers Lookup from a table, by viewer, and records what it was asked.
type fakeIssues struct {
	visible map[string][]string // identifier -> logins who may see it; "*" is an admin
	asked   []string
}

func (f *fakeIssues) Lookup(_ context.Context, v tickets.Viewer, ref linear.IssueRef, _ bool) (tickets.Issue, error) {
	f.asked = append(f.asked, ref.String())
	for _, who := range f.visible[ref.Identifier] {
		if who == v.Login || (who == "*" && v.Admin) {
			return tickets.Issue{Ticket: linear.Ticket{ID: "id-" + ref.Identifier, Identifier: ref.Identifier, Title: "T " + ref.Identifier}}, nil
		}
	}
	return tickets.Issue{}, tickets.ErrNotFound
}

func (f *fakeIssues) KnownTeams(context.Context) map[string]bool { return map[string]bool{"EME": true} }

func TestGetLinearIssuesAnswersOnlyForIssuesTheCallerMaySee(t *testing.T) {
	f := &fakeIssues{visible: map[string][]string{"EME-6": {"ana@acme.com", "*"}, "EME-7": {"*"}}}
	s := NewAgentService(AgentServiceOptions{LinearIssues: f, Logger: quietLogger()})
	req := connect.NewRequest(&agentv1.GetLinearIssuesRequest{
		Refs: []string{"EME-6", "EME-7", "UTF-8", "not a ref", "https://linear.app/acme/issue/EME-6/x", "EME-6"},
	})

	res, err := s.GetLinearIssues(loginCtx("ana@acme.com"), req)
	require.NoError(t, err)
	var got []string
	for _, i := range res.Msg.GetIssues() {
		got = append(got, i.GetRef()+"="+i.GetIdentifier())
	}
	assert.Equal(t, []string{"EME-6=EME-6", "https://linear.app/acme/issue/EME-6/x=EME-6"}, got,
		"EME-7 is hidden from her exactly as UTF-8 is absent")
	assert.NotContains(t, f.asked, "UTF-8", "a team the workspace does not have is never asked about")

	admin := context.WithValue(loginCtx("root@acme.com"), infraKey{}, true)
	res, err = s.GetLinearIssues(admin, req)
	require.NoError(t, err)
	assert.Len(t, res.Msg.GetIssues(), 3, "an admin sees every issue")
}

func TestGetLinearIssuesNeedsACallerAndBoundsTheBatch(t *testing.T) {
	s := NewAgentService(AgentServiceOptions{LinearIssues: &fakeIssues{}, Logger: quietLogger()})
	_, err := s.GetLinearIssues(context.Background(), connect.NewRequest(&agentv1.GetLinearIssuesRequest{Refs: []string{"EME-1"}}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	many := make([]string, maxLinearIssueRefs+1)
	for i := range many {
		many[i] = "EME-1"
	}
	_, err = s.GetLinearIssues(loginCtx("ana@acme.com"), connect.NewRequest(&agentv1.GetLinearIssuesRequest{Refs: many}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	none := NewAgentService(AgentServiceOptions{Logger: quietLogger()})
	res, err := none.GetLinearIssues(loginCtx("ana@acme.com"), connect.NewRequest(&agentv1.GetLinearIssuesRequest{Refs: []string{"EME-1"}}))
	require.NoError(t, err)
	assert.Empty(t, res.Msg.GetIssues(), "a conductor with no Linear describes nothing")
}
