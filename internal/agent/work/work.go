// Package work is the Issues view's model of work management, whatever issue tracker it lives
// in: an Item is one issue's (or project's) conversation, a Source lists a provider's items, and
// Status is the column of the board a provider's own state falls in.
//
// A provider is one adapter: a Source whose List maps the provider's rows onto Item and its
// state onto Status. Linear's is internal/agent/linear's WorkSource. The mapping from a
// provider's states to Status lives with the provider, not here.
package work

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Status is the category of a provider's state for an item: the board's columns.
type Status string

// The statuses, in the board's order. StatusUnknown is a state the provider named none of these
// for, or none at all (a project).
const (
	StatusTriage     Status = "triage"
	StatusBacklog    Status = "backlog"
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusCanceled   Status = "canceled"
	StatusUnknown    Status = ""
)

// Statuses are the known statuses, in the board's order.
var Statuses = []Status{StatusTriage, StatusBacklog, StatusTodo, StatusInProgress, StatusDone, StatusCanceled}

// StatusNone filters to the items with no known status: the board's last column.
const StatusNone Status = "none"

// ParseStatusFilter is s as a Filter.Status — "", one of Statuses, or StatusNone — and whether
// it is one.
func ParseStatusFilter(s string) (Status, bool) {
	st := Status(strings.ToLower(strings.TrimSpace(s)))
	if st == StatusUnknown || st == StatusNone || slices.Contains(Statuses, st) {
		return st, true
	}
	return "", false
}

// Where Podium is with an item, whatever its provider.
const (
	StateWaiting = "waiting"
	StateWorking = "working"
	StateFailed  = "failed"
	StateDone    = "done"
	StateNew     = "new"
	// StateAttention is a filter, not a state: waiting and failed together.
	StateAttention = "attention"
)

// Item is one issue's or project's conversation.
type Item struct {
	ChatID string
	// Provider is the Source's Provider().
	Provider string
	// Kind is "issue" or "project".
	Kind string
	// Key is the provider's identifier ("ENG-12"), "" when it has none.
	Key   string
	Title string
	URL   string
	// Team is the provider's grouping key, "" for none.
	Team string
	// Status is the provider's state as a category; StatusLabel is the provider's own name for it.
	Status      Status
	StatusLabel string
	// State is where Podium is with it: StateWaiting, …Working, …Failed, …Done or …New.
	State          string
	AskedOf        string
	LastActivityAt time.Time
	LastText       string
	CostUSD        float64
	// MyRole is the viewer's role in the chat, "" for an admin who reads it as an admin.
	MyRole string
	// Mine is an item the viewer touched, spoke in, owns or was shared.
	Mine bool
}

// Filter narrows a Source's list.
type Filter struct {
	// SeeAll is an admin, an owner or the dev token, who list every item.
	SeeAll bool
	// Mine lists only what the viewer touched, spoke in, owns or was shared.
	Mine bool
	// Team is a team key of the provider, "" for every team.
	Team string
	// State is "", a state, or StateAttention.
	State string
	// Status is "", one of Statuses, or StatusNone.
	Status Status
	// Limit is the most items a page holds; Cursor is the Page.Next of the page before, "" for
	// the first.
	Limit  int
	Cursor string
}

// Page is one page of a Source's items, most recently active first.
type Page struct {
	Items []Item
	// Next is the cursor of the next page, "" for none.
	Next string
	// Total is how many items the filter matches, counted on the first page only.
	Total int
	// Teams are the teams of every item the viewer may list, whatever the filter.
	Teams []string
}

// Source lists one provider's items for a viewer.
type Source interface {
	// Provider names the issue tracker: "linear".
	Provider() string
	List(ctx context.Context, viewer string, f Filter) (Page, error)
}
