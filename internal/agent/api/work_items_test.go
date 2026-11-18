package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/work"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// fakeWork is a provider that remembers what it was asked and answers one item.
type fakeWork struct {
	provider string
	asked    []work.Filter
}

func (f *fakeWork) Provider() string { return f.provider }

func (f *fakeWork) List(_ context.Context, _ string, filter work.Filter) (work.Page, error) {
	f.asked = append(f.asked, filter)
	return work.Page{
		Items: []work.Item{{
			ChatID: "c-" + f.provider, Provider: f.provider, Kind: "issue", Key: "ENG-1",
			Status: work.StatusInProgress, StatusLabel: "In Progress", State: work.StateWorking,
			LastActivityAt: time.Unix(1, 0),
		}},
		Total: 1,
		Teams: []string{"ENG"},
	}, nil
}

func workService(sources ...work.Source) *AgentService {
	return NewAgentService(AgentServiceOptions{
		Chat: struct{ ChatSource }{}, Logger: quietLogger(), WorkSources: sources,
	})
}

func listWork(ctx context.Context, s *AgentService, msg *agentv1.ListWorkItemsRequest) (*agentv1.ListWorkItemsResponse, error) {
	res, err := s.ListWorkItems(ctx, connect.NewRequest(msg))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// TestTheBoardAsksForAColumnThereIs refuses a column that is no status before a source is asked.
func TestTheBoardAsksForAColumnThereIs(t *testing.T) {
	lin := &fakeWork{provider: "linear"}
	s := workService(lin)

	for _, bad := range []string{"started", "in progress", "completed", "in_progress;drop", "all"} {
		_, err := listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{Status: bad})
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), bad)
	}
	assert.Empty(t, lin.asked)
	_, err := listWork(context.Background(), s, &agentv1.ListWorkItemsRequest{Status: "todo"})
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"nobody signed in learns nothing, not even which columns there are")
}

func TestOneProviderIsEveryProvider(t *testing.T) {
	lin := &fakeWork{provider: "linear"}
	s := workService(lin)

	got, err := listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{
		Status: " In_Progress ", Team: "ENG", State: "attention", Mine: true,
		Page: &agentv1.Page{Limit: 10, Cursor: "x"},
	})
	require.NoError(t, err)
	require.Len(t, lin.asked, 1)
	assert.Equal(t, work.Filter{
		Mine: true, Team: "ENG", State: "attention", Status: work.StatusInProgress, Limit: 10, Cursor: "x",
	}, lin.asked[0])
	require.Len(t, got.GetItems(), 1)
	item := got.GetItems()[0]
	assert.Equal(t, "linear", item.GetProvider())
	assert.Equal(t, "in_progress", item.GetStatus())
	assert.Equal(t, "In Progress", item.GetStatusLabel())
	assert.Equal(t, []string{"linear"}, got.GetProviders())
	require.Len(t, got.GetTeams(), 1)
	assert.Equal(t, "linear", got.GetTeams()[0].GetProvider())
	assert.Equal(t, "ENG", got.GetTeams()[0].GetKey())
	assert.EqualValues(t, 1, got.GetTotal())

	_, err = listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{Provider: "Linear"})
	require.NoError(t, err, "a provider is named in any case")
	_, err = listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{Provider: "jira"})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a provider Podium does not list")
}

func TestSeveralProvidersAreListedOneAtATime(t *testing.T) {
	lin, gh := &fakeWork{provider: "linear"}, &fakeWork{provider: "github"}
	s := workService(lin, gh)

	_, err := listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{})
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err), "merging providers' pages is not built")

	got, err := listWork(loginCtx("ana@acme.com"), s, &agentv1.ListWorkItemsRequest{Provider: "github"})
	require.NoError(t, err)
	assert.Empty(t, lin.asked)
	assert.Len(t, gh.asked, 1)
	assert.Equal(t, "c-github", got.GetItems()[0].GetChatId())
	assert.Equal(t, []string{"linear", "github"}, got.GetProviders())
}

func TestNoProviderListsNothing(t *testing.T) {
	got, err := listWork(loginCtx("ana@acme.com"), workService(), &agentv1.ListWorkItemsRequest{})
	require.NoError(t, err)
	assert.Empty(t, got.GetItems())
	assert.Empty(t, got.GetProviders())
}
