// Package work is the Issues view's model of work management, whatever issue tracker it lives
// in: an Item is one issue's (or project's) conversation, a Source lists a provider's items, and
// Status is the category a provider's own state falls in. The board's columns are Podium's
// State, not Status: what Podium is doing about each issue.
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

// Status is the category of a provider's state for an item.
type Status string

// The statuses, in a tracker's usual order. StatusUnknown is a state the provider named none of these
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

// Statuses are the known statuses, in a tracker's usual order.
var Statuses = []Status{StatusTriage, StatusBacklog, StatusTodo, StatusInProgress, StatusDone, StatusCanceled}

// Where Podium is with an item, whatever its provider: the board's columns.
const (
	StateWaiting = "waiting"
	StateWorking = "working"
	StateFailed  = "failed"
	StateDone    = "done"
	StateNew     = "new"
	// StateAttention is a filter, not a state: waiting and failed together.
	StateAttention = "attention"
)

// States are the states, in the board's order.
var States = []string{StateNew, StateWorking, StateWaiting, StateFailed, StateDone}

// ParseStateFilter is s as a Filter.State — "", one of States, or StateAttention — and whether
// it is one.
func ParseStateFilter(s string) (string, bool) {
	st := strings.ToLower(strings.TrimSpace(s))
	if st == "" || st == StateAttention || slices.Contains(States, st) {
		return st, true
	}
	return "", false
}

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
	// Involved is an item the viewer is involved in, by the provider's rule (Linear's: assigned
	// or created it, asked Podium about it, spoke in its conversation, or was @mentioned in it).
	Involved bool
}

// Filter narrows a Source's list.
type Filter struct {
	// SeeAll is an admin, an owner or the dev token, who may read every item.
	SeeAll bool
	// All lists every item the viewer may read, not only those they are involved in. Only
	// SeeAll may ask for it; the API refuses anybody else.
	All bool
	// Team is a team key of the provider, "" for every team.
	Team string
	// State is "", one of States, or StateAttention.
	State string
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
	// Teams are the teams of every item the viewer lists (All or involved), whatever the other
	// filters.
	Teams []string
}

// Source lists one provider's items for a viewer.
type Source interface {
	// Provider names the issue tracker: "linear".
	Provider() string
	List(ctx context.Context, viewer string, f Filter) (Page, error)
}
