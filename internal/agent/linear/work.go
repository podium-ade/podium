package linear

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/work"
)

// stateTypeStatus is the work.Status each of Linear's state types falls in. A state type not
// here — one Linear adds later — is work.StatusUnknown, as a project's is.
var stateTypeStatus = map[string]work.Status{
	"triage":    work.StatusTriage,
	"backlog":   work.StatusBacklog,
	"unstarted": work.StatusTodo,
	"started":   work.StatusInProgress,
	"completed": work.StatusDone,
	"canceled":  work.StatusCanceled,
}

// WorkStatus is the work.Status of an issue whose Linear state has type stateType.
func WorkStatus(kind, stateType string) work.Status {
	if kind == store.LinearKindProject {
		return work.StatusUnknown
	}
	return stateTypeStatus[stateType]
}

// workStore is the store's Linear view, which WorkSource adapts.
type workStore interface {
	ListLinearConversations(
		ctx context.Context, viewer string, f store.LinearConversationFilter, limit int, cursor string,
	) (store.LinearConversationPage, error)
	LinearConversationTeams(ctx context.Context, viewer string, seeAll, all bool) ([]string, error)
}

// WorkSource is Linear as a work.Source: the conversations of the Linear issues and projects
// the conductor keeps, as the webhooks last described them.
type WorkSource struct {
	store workStore
}

// NewWorkSource is Linear's work.Source over st.
func NewWorkSource(st *store.Store) *WorkSource { return &WorkSource{store: st} }

var _ work.Source = (*WorkSource)(nil)

// Provider is Kind.
func (*WorkSource) Provider() string { return Kind }

// List is a page of Linear's items for viewer.
func (w *WorkSource) List(ctx context.Context, viewer string, f work.Filter) (work.Page, error) {
	got, err := w.store.ListLinearConversations(ctx, viewer, store.LinearConversationFilter{
		SeeAll: f.SeeAll,
		All:    f.All,
		Team:   f.Team,
		State:  f.State,
	}, f.Limit, f.Cursor)
	if err != nil {
		return work.Page{}, err
	}
	teams, err := w.store.LinearConversationTeams(ctx, viewer, f.SeeAll, f.All)
	if err != nil {
		return work.Page{}, err
	}
	items := make([]work.Item, 0, len(got.Rows))
	for _, r := range got.Rows {
		items = append(items, work.Item{
			ChatID:         r.ChatID,
			Provider:       Kind,
			Kind:           r.Kind,
			Key:            r.Identifier,
			Title:          r.Title,
			URL:            r.URL,
			Team:           r.TeamKey,
			Status:         WorkStatus(r.Kind, r.StateType),
			StatusLabel:    r.StateName,
			State:          r.State,
			AskedOf:        r.AskedOf,
			LastActivityAt: r.LastActivityAt,
			LastText:       r.LastText,
			CostUSD:        r.CostUSD,
			MyRole:         r.MyRole,
			Involved:       r.Involved,
		})
	}
	return work.Page{Items: items, Next: got.Next, Total: got.Total, Teams: teams}, nil
}
