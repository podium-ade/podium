package linear

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/work"
)

func TestLinearStateTypesAreWorkStatuses(t *testing.T) {
	for stateType, want := range map[string]work.Status{
		"triage":    work.StatusTriage,
		"backlog":   work.StatusBacklog,
		"unstarted": work.StatusTodo,
		"started":   work.StatusInProgress,
		"completed": work.StatusDone,
		"canceled":  work.StatusCanceled,
		"":          work.StatusUnknown,
		"paused":    work.StatusUnknown,
	} {
		assert.Equal(t, want, WorkStatus(store.LinearKindIssue, stateType), stateType)
	}
	assert.Equal(t, work.StatusUnknown, WorkStatus(store.LinearKindProject, "started"), "a project has no column")
}

type fakeLinearStore struct {
	filter   store.LinearConversationFilter
	teamsAll bool
	limit    int
	cursor   string
}

func (f *fakeLinearStore) ListLinearConversations(
	_ context.Context, _ string, filter store.LinearConversationFilter, limit int, cursor string,
) (store.LinearConversationPage, error) {
	f.filter, f.limit, f.cursor = filter, limit, cursor
	return store.LinearConversationPage{
		Rows: []store.LinearConversation{
			{ChatID: "c1", Kind: store.LinearKindIssue, Identifier: "ENG-12", Title: "Fix login", TeamKey: "ENG",
				StateName: "In Review", StateType: "started", State: store.LinearStateWaiting, LastActivityAt: time.Unix(5, 0),
				Involved: true},
			{ChatID: "c2", Kind: store.LinearKindProject, Title: "Q4", StateType: "started"},
		},
		Next:  "n",
		Total: 7,
	}, nil
}

func (f *fakeLinearStore) LinearConversationTeams(_ context.Context, _ string, _, all bool) ([]string, error) {
	f.teamsAll = all
	return []string{"ENG", "OPS"}, nil
}

func TestWorkSourceListsLinearsConversations(t *testing.T) {
	st := &fakeLinearStore{}
	src := &WorkSource{store: st}
	assert.Equal(t, "linear", src.Provider())

	page, err := src.List(context.Background(), "ana@acme.com", work.Filter{
		SeeAll: true, All: true, Team: "ENG", State: work.StateAttention, Limit: 10, Cursor: "c",
	})
	require.NoError(t, err)
	assert.Equal(t, store.LinearConversationFilter{
		SeeAll: true, All: true, Team: "ENG", State: work.StateAttention,
	}, st.filter)
	assert.True(t, st.teamsAll, "the team filter lists the teams of what is listed")
	assert.Equal(t, 10, st.limit)
	assert.Equal(t, "c", st.cursor)

	require.Len(t, page.Items, 2)
	assert.Equal(t, work.Item{
		ChatID: "c1", Provider: "linear", Kind: "issue", Key: "ENG-12", Title: "Fix login", Team: "ENG",
		Status: work.StatusInProgress, StatusLabel: "In Review", State: work.StateWaiting, LastActivityAt: time.Unix(5, 0),
		Involved: true,
	}, page.Items[0])
	assert.Equal(t, work.StatusUnknown, page.Items[1].Status)
	assert.Equal(t, "n", page.Next)
	assert.Equal(t, 7, page.Total)
	assert.Equal(t, []string{"ENG", "OPS"}, page.Teams)

}
