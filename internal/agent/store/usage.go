package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// MaxUsageRange is the widest window Usage will read. A year of turns is already more than
// any calendar draws, and an unbounded range is a table scan an operator can ask for by
// typing a date.
const MaxUsageRange = 366 * 24 * time.Hour

// MaxUsageConversations caps Usage.Conversations: it is a "most expensive" list, not a census.
const MaxUsageConversations = 50

// The kinds of credential a unit of spend can record (0056). A row from before it was
// recorded has neither, and reads as "".
const (
	AuthAPIKey       = "api_key"
	AuthSubscription = "subscription"
)

// The kinds of row usage_rows holds besides a turn and a delegation.
const (
	UsageKindTurn       = "turn"
	UsageKindDelegation = "delegation"
	// UsageKindSummary is a conversation's rolling summary (housekeeping_spend.purpose).
	UsageKindSummary = "summary"
)

// UsageTotals is one group's spend. CostUSD is everything reported, SubscriptionCostUSD the
// part of it a subscription covered: the harness prices those steps at list price, which is
// not money billed. Tokens are sums over the rows that reported any.
type UsageTotals struct {
	CostUSD             float64
	SubscriptionCostUSD float64
	Turns               int
	ModelTurns          int
	Unpriced            int
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
}

func (t *UsageTotals) add(o UsageTotals) {
	t.CostUSD += o.CostUSD
	t.SubscriptionCostUSD += o.SubscriptionCostUSD
	t.Turns += o.Turns
	t.ModelTurns += o.ModelTurns
	t.Unpriced += o.Unpriced
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.ReasoningTokens += o.ReasoningTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.CacheWriteTokens += o.CacheWriteTokens
}

// totals builds UsageTotals from the ten aggregate columns every usage query ends with. They
// arrive on a different sqlc row type per query, so this is the one place they are named.
func totals(cost, sub float64, turns, modelTurns, unpriced int32, in, out, reasoning, cacheRead, cacheWrite int64) UsageTotals {
	return UsageTotals{
		CostUSD: cost, SubscriptionCostUSD: sub,
		Turns: int(turns), ModelTurns: int(modelTurns), Unpriced: int(unpriced),
		InputTokens: in, OutputTokens: out, ReasoningTokens: reasoning,
		CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
	}
}

// Tokens is what one unit of spend used, summed over its model steps. A nil field is one the
// runtime did not report, which is every row from before 0056.
type Tokens struct {
	Input      *int64
	Output     *int64
	Reasoning  *int64
	CacheRead  *int64
	CacheWrite *int64
}

// Credential is what paid for a unit of spend. AuthKind is AuthAPIKey or AuthSubscription;
// Owner is the login whose own credential it was, "" for the company's; Account is who the
// provider said signed in, or the key's hint. The zero value is "not recorded".
type Credential struct {
	AuthKind string
	Owner    string
	Account  string
}

// UsageDay is one day's spend, bucketed in the caller's own time zone. Date is YYYY-MM-DD
// as that zone saw it, which is why it is a string and not a time: it names a day, not an
// instant, and turning it back into one would put it in the server's zone.
type UsageDay struct {
	Date string
	UsageTotals
}

// TurnCost is one unit of spend — a turn, a delegated task, or the conductor's housekeeping —
// with the fields that say what spent it. TaskID is empty for one that ran no task: the
// money was still spent, so it counts towards a total, but it joins to no task.
type TurnCost struct {
	TurnID     string
	Kind       string
	TaskID     string
	SessionID  string
	SourceKind string
	SourceKey  string
	Playbook   string
	Profile    string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	NumTurns   *int
	CostUSD    *float64
	// Backend is what ran it. Zero for a turn recorded before it was written down.
	Backend Backend
	// StartedBy is whose turn it was (Turn.StartedBy). A delegation reports the login of the
	// turn that delegated it.
	StartedBy  string
	Credential Credential
	Tokens     Tokens
	ChatID     string
	ChatTitle  string
	// RoutineID and RoutineName name the routine whose run this was.
	RoutineID   string
	RoutineName string
	// Origin is what the usage screen groups sources by: "routine", "housekeeping", or the
	// session's kind.
	Origin string
}

// UsagePerson is what one person's turns cost over the range. StartedBy is a login,
// TurnStartedByApp, a routine's TurnStartedByRoutine, or empty for turns with nobody
// recorded behind them.
type UsagePerson struct {
	StartedBy string
	UsageTotals
}

// UsageBackend is one (provider, agent, model, effort) and what it cost over the range.
// Every field may be empty together, which is the bucket for turns that predate the
// columns; the API reports that as unrecorded rather than as a model with no name.
type UsageBackend struct {
	Backend
	UsageTotals
}

// UsageSource is the range's spend from one origin.
type UsageSource struct {
	Origin string
	UsageTotals
}

// UsageCredential is the range's spend by what paid for it. Provider and Credential are all
// empty for rows from before 0056.
type UsageCredential struct {
	Provider string
	Credential
	UsageTotals
}

// Key is the credential's filter value: UsageFilter.Credential matches it.
func (c UsageCredential) Key() string {
	return CredentialKey(c.AuthKind, c.Provider, c.Owner)
}

// CredentialKey is the filter value of one credential group.
func CredentialKey(authKind, provider, owner string) string {
	return authKind + "|" + provider + "|" + owner
}

// UsageConversation is one conversation's spend.
type UsageConversation struct {
	SessionID string
	ChatID    string
	ChatTitle string
	Origin    string
	SourceKey string
	UsageTotals
}

// UsageRoutine is one routine's spend.
type UsageRoutine struct {
	RoutineID string
	Name      string
	UsageTotals
}

// Usage is what the conductor spent over a range: a total per day, the groupings the usage
// screen breaks it down by, and the first page of the individual rows behind it.
type Usage struct {
	Days          []UsageDay
	Costs         []TurnCost
	Backends      []UsageBackend
	People        []UsagePerson
	Sources       []UsageSource
	Credentials   []UsageCredential
	Conversations []UsageConversation
	Routines      []UsageRoutine
	// Totals is the whole range. Costs is capped by limit and may be shorter.
	Totals UsageTotals
	// The older names for Totals' fields, kept for the API's total_* fields.
	TotalCostUSD    float64
	TotalTurns      int
	TotalModelTurns int
	Unpriced        int
}

// UsageFilter narrows a usage read. Every field is optional and the set ones are ANDed.
// StartedBy and AuthKind take "-" for "none recorded", because "" means "any".
type UsageFilter struct {
	Origin     string
	StartedBy  string
	Provider   string
	Model      string
	AuthKind   string
	Credential string
	RoutineID  string
	ChatID     string
	Kind       string
}

// UsageQuery bounds a usage read. From is inclusive and To is exclusive, both against the
// row's start.
type UsageQuery struct {
	From time.Time
	To   time.Time
	// CompareFrom widens the day rows, and nothing else, back to an earlier instant. Zero or
	// later than From means the days start at From like the rest of the answer.
	CompareFrom time.Time
	// TZOffsetMinutes is the caller's offset from UTC, east-positive, and decides where a
	// day boundary falls.
	TZOffsetMinutes int
	// Limit caps Costs. Days always covers the whole range.
	Limit  int
	Filter UsageFilter
}

// UsageRowsQuery is one page of a range's rows.
type UsageRowsQuery struct {
	From, To time.Time
	Filter   UsageFilter
	// Cursor is the previous page's next cursor; empty is the first page.
	Cursor string
	Limit  int
}

// ErrBadCursor is a usage cursor this store did not hand out.
var ErrBadCursor = errors.New("the cursor is not one this server returned")

// clampRange applies MaxUsageRange from the far end, so narrowing a too-wide range keeps the
// recent days the caller was almost certainly asking about.
func clampRange(from, to time.Time) (time.Time, time.Time) {
	from, to = from.UTC(), to.UTC()
	if to.Sub(from) > MaxUsageRange {
		from = to.Add(-MaxUsageRange)
	}
	return from, to
}

// Usage reports spend over q's range. An empty or backwards range is not an error: it
// reports nothing, because "no turns ran then" is the honest answer to it.
func (s *Store) Usage(ctx context.Context, q UsageQuery) (Usage, error) {
	from, to := clampRange(q.From, q.To)
	if !to.After(from) {
		return Usage{
			Days: []UsageDay{}, Costs: []TurnCost{}, Backends: []UsageBackend{}, People: []UsagePerson{},
			Sources: []UsageSource{}, Credentials: []UsageCredential{},
			Conversations: []UsageConversation{}, Routines: []UsageRoutine{},
		}, nil
	}

	// Only the day rows reach back over the comparison window. Everything else is about the
	// range the caller actually asked for; widening it here is how a week's spend quietly
	// becomes a fortnight's.
	dayFrom := from
	if !q.CompareFrom.IsZero() && q.CompareFrom.UTC().Before(dayFrom) {
		dayFrom, _ = clampRange(q.CompareFrom, to)
	}
	f := q.Filter

	dayRows, err := s.q.UsageByDay(ctx, db.UsageByDayParams{
		TzOffsetMinutes: int32(q.TZOffsetMinutes), FromTime: dayFrom, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by day: %w", err)
	}
	costs, _, err := s.UsageRows(ctx, UsageRowsQuery{From: from, To: to, Filter: f, Limit: q.Limit})
	if err != nil {
		return Usage{}, err
	}
	backendRows, err := s.q.UsageByBackend(ctx, db.UsageByBackendParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by backend: %w", err)
	}
	personRows, err := s.q.UsageByPerson(ctx, db.UsageByPersonParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by person: %w", err)
	}
	sourceRows, err := s.q.UsageBySource(ctx, db.UsageBySourceParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by source: %w", err)
	}
	credRows, err := s.q.UsageByCredential(ctx, db.UsageByCredentialParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by credential: %w", err)
	}
	convRows, err := s.q.UsageByConversation(ctx, db.UsageByConversationParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind, PageLimit: MaxUsageConversations,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by conversation: %w", err)
	}
	routineRows, err := s.q.UsageByRoutine(ctx, db.UsageByRoutineParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by routine: %w", err)
	}

	out := Usage{
		Days:          make([]UsageDay, 0, len(dayRows)),
		Costs:         costs,
		Backends:      make([]UsageBackend, 0, len(backendRows)),
		People:        make([]UsagePerson, 0, len(personRows)),
		Sources:       make([]UsageSource, 0, len(sourceRows)),
		Credentials:   make([]UsageCredential, 0, len(credRows)),
		Conversations: make([]UsageConversation, 0, len(convRows)),
		Routines:      make([]UsageRoutine, 0, len(routineRows)),
	}
	for _, r := range dayRows {
		out.Days = append(out.Days, UsageDay{Date: r.Day, UsageTotals: totals(
			r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens)})
	}
	for _, r := range backendRows {
		out.Backends = append(out.Backends, UsageBackend{
			Backend: Backend{Agent: r.Agent, Model: r.Model, Effort: r.Effort, Provider: r.Provider},
			UsageTotals: totals(r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
				r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens),
		})
	}
	for _, r := range personRows {
		out.People = append(out.People, UsagePerson{StartedBy: r.StartedBy, UsageTotals: totals(
			r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens)})
	}
	for _, r := range sourceRows {
		out.Sources = append(out.Sources, UsageSource{Origin: r.Origin, UsageTotals: totals(
			r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens)})
	}
	for _, r := range credRows {
		out.Credentials = append(out.Credentials, UsageCredential{
			Provider:   r.Provider,
			Credential: Credential{AuthKind: r.AuthKind, Owner: r.CredentialOwner, Account: r.CredentialAccount},
			UsageTotals: totals(r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
				r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens),
		})
	}
	for _, r := range convRows {
		out.Conversations = append(out.Conversations, UsageConversation{
			SessionID: r.SessionID, ChatID: r.ChatID, ChatTitle: r.ChatTitle,
			Origin: r.Origin, SourceKey: r.SourceKey,
			UsageTotals: totals(r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
				r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens),
		})
	}
	for _, r := range routineRows {
		out.Routines = append(out.Routines, UsageRoutine{RoutineID: r.RoutineID, Name: r.RoutineName,
			UsageTotals: totals(r.CostUsd, r.SubscriptionCostUsd, r.Turns, r.ModelTurns, r.Unpriced,
				r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens, r.CacheWriteTokens)})
	}
	// The totals are summed from the source grouping rather than from the day rows, which may
	// reach back over a comparison window, and rather than from Costs, which is a capped page.
	// Every row has exactly one origin, so the grouping is complete and exactly the range.
	for _, src := range out.Sources {
		out.Totals.add(src.UsageTotals)
	}
	out.TotalCostUSD = out.Totals.CostUSD
	out.TotalTurns = out.Totals.Turns
	out.TotalModelTurns = out.Totals.ModelTurns
	out.Unpriced = out.Totals.Unpriced
	return out, nil
}

// UsageRows is one page of q's rows, newest first, and the cursor of the next page — empty on
// the last.
func (s *Store) UsageRows(ctx context.Context, q UsageRowsQuery) ([]TurnCost, string, error) {
	from, to := clampRange(q.From, q.To)
	if !to.After(from) {
		return []TurnCost{}, "", nil
	}
	cursorAt, cursorID, err := parseUsageCursor(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := clampLimit(q.Limit)
	f := q.Filter
	rows, err := s.q.UsageRows(ctx, db.UsageRowsParams{
		FromTime: from, ToTime: to,
		FOrigin: f.Origin, FStartedBy: f.StartedBy, FProvider: f.Provider, FModel: f.Model,
		FAuthKind: f.AuthKind, FCredential: f.Credential, FRoutineID: f.RoutineID,
		FChatID: f.ChatID, FKind: f.Kind,
		CursorAt: cursorAt, CursorID: cursorID,
		PageLimit: int32(limit),
	})
	if err != nil {
		return nil, "", fmt.Errorf("read usage rows: %w", err)
	}
	out := make([]TurnCost, 0, len(rows))
	for _, r := range rows {
		c := TurnCost{
			TurnID:     r.ID,
			Kind:       r.Kind,
			TaskID:     r.TaskID,
			SessionID:  r.SessionID,
			SourceKind: r.SourceKind,
			SourceKey:  r.SourceKey,
			Playbook:   r.Playbook,
			Profile:    r.Profile,
			Status:     r.Status,
			StartedAt:  r.StartedAt.UTC(),
			FinishedAt: utcPtr(r.FinishedAt),
			CostUSD:    r.CostUsd,
			StartedBy:  r.StartedBy,
			Backend:    Backend{Agent: r.Agent, Model: r.Model, Effort: r.Effort, Provider: r.Provider},
			Credential: Credential{AuthKind: r.AuthKind, Owner: r.CredentialOwner, Account: r.CredentialAccount},
			Tokens: Tokens{
				Input: r.InputTokens, Output: r.OutputTokens, Reasoning: r.ReasoningTokens,
				CacheRead: r.CacheReadTokens, CacheWrite: r.CacheWriteTokens,
			},
			ChatID:      r.ChatID,
			ChatTitle:   r.ChatTitle,
			RoutineID:   r.RoutineID,
			RoutineName: r.RoutineName,
			Origin:      r.Origin,
		}
		if r.NumTurns != nil {
			n := int(*r.NumTurns)
			c.NumTurns = &n
		}
		out = append(out, c)
	}
	next := ""
	if len(out) == limit {
		last := out[len(out)-1]
		next = usageCursor(last.StartedAt, last.TurnID)
	}
	return out, next, nil
}

// usageCursor is a page boundary: the last row's start, to the nanosecond, and its id.
func usageCursor(at time.Time, id string) string {
	return strconv.FormatInt(at.UnixNano(), 10) + "." + id
}

func parseUsageCursor(c string) (*time.Time, string, error) {
	if c == "" {
		return nil, "", nil
	}
	ns, id, ok := strings.Cut(c, ".")
	if !ok || id == "" {
		return nil, "", ErrBadCursor
	}
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil {
		return nil, "", ErrBadCursor
	}
	at := time.Unix(0, n).UTC()
	return &at, id, nil
}
