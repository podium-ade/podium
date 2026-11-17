package api

import (
	"context"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// LinearDirectory is the Linear app as the playbook editor is allowed to see it: the
// workspace's teams and issue labels, which a playbook's linear_teams and linear_labels name.
type LinearDirectory interface {
	// LinearCatalog reports connected false, and nothing else, when no Linear app runs here.
	LinearCatalog(ctx context.Context) (teams []LinearTeam, labels []LinearLabel, connected bool, err error)
}

// LinearTeam is one team of the workspace.
type LinearTeam struct{ Key, Name string }

// LinearLabel is one issue label; TeamKey is empty for a workspace label.
type LinearLabel struct{ Name, TeamKey string }

// linearCatalogTimeout bounds asking Linear for the pickers. A slow answer leaves the editor
// taking typed values, which is all it needs.
const linearCatalogTimeout = 10 * time.Second

// ListLinearCatalog reports the Linear workspace's teams and labels. A failure to ask Linear
// is said in the response rather than as an error: the editor still works without the list.
func (s *AgentService) ListLinearCatalog(
	ctx context.Context, _ *connect.Request[agentv1.ListLinearCatalogRequest],
) (*connect.Response[agentv1.ListLinearCatalogResponse], error) {
	out := &agentv1.ListLinearCatalogResponse{}
	if s.linear == nil {
		return connect.NewResponse(out), nil
	}
	ctx, cancel := context.WithTimeout(ctx, linearCatalogTimeout)
	defer cancel()
	teams, labels, connected, err := s.linear.LinearCatalog(ctx)
	out.LinearConnected = connected
	if err != nil {
		s.logger.WarnContext(ctx, "listing linear teams and labels failed", "error", err)
		out.Error = "Linear could not be asked for its teams and labels: " + err.Error()
		return connect.NewResponse(out), nil
	}
	for _, t := range teams {
		out.Teams = append(out.Teams, &agentv1.LinearTeam{Key: t.Key, Name: t.Name})
	}
	for _, l := range labels {
		out.Labels = append(out.Labels, &agentv1.LinearLabel{Name: l.Name, TeamKey: l.TeamKey})
	}
	return connect.NewResponse(out), nil
}
