package store

import (
	"context"
	"fmt"
	"time"

	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
)

// MaxUsageRange is the widest window Usage will read. A year of turns is already more than
// any calendar draws, and an unbounded range is a table scan an operator can ask for by
// typing a date.
const MaxUsageRange = 366 * 24 * time.Hour

// UsageDay is one day's spend, bucketed in the caller's own time zone. Date is YYYY-MM-DD
// as that zone saw it, which is why it is a string and not a time: it names a day, not an
// instant, and turning it back into one would put it in the server's zone.
type UsageDay struct {
	Date       string
	CostUSD    float64
	Turns      int
	ModelTurns int
	Unpriced   int
}

// TurnCost is one turn's spend with the session fields that say what spent it. TaskID is
// empty for a turn whose task was never created — the money was still spent, so it counts
// towards a total, but it joins to no task.
type TurnCost struct {
	TurnID     string
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
}

// Usage is what the conductor spent over a range: a total per day, and the individual
// turns behind it.
type Usage struct {
	Days  []UsageDay
	Costs []TurnCost
	// The totals are for the whole range. Costs is capped by limit and may be shorter.
	TotalCostUSD    float64
	TotalTurns      int
	TotalModelTurns int
	Unpriced        int
}

// UsageQuery bounds a usage read. From is inclusive and To is exclusive, both against the
// turn's start.
type UsageQuery struct {
	From time.Time
	To   time.Time
	// TZOffsetMinutes is the caller's offset from UTC, east-positive, and decides where a
	// day boundary falls.
	TZOffsetMinutes int
	// Limit caps Costs. Days always covers the whole range.
	Limit int
}

// Usage reports spend over q's range. An empty or backwards range is not an error: it
// reports nothing, because "no turns ran then" is the honest answer to it.
func (s *Store) Usage(ctx context.Context, q UsageQuery) (Usage, error) {
	from, to := q.From.UTC(), q.To.UTC()
	if !to.After(from) {
		return Usage{Days: []UsageDay{}, Costs: []TurnCost{}}, nil
	}
	// Clamped from the far end, so narrowing a too-wide range keeps the recent days the
	// caller was almost certainly asking about.
	if to.Sub(from) > MaxUsageRange {
		from = to.Add(-MaxUsageRange)
	}

	dayRows, err := s.q.UsageByDay(ctx, db.UsageByDayParams{
		TzOffsetMinutes: int32(q.TZOffsetMinutes),
		FromTime:        from,
		ToTime:          to,
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read usage by day: %w", err)
	}
	costRows, err := s.q.ListTurnCosts(ctx, db.ListTurnCostsParams{
		FromTime:  from,
		ToTime:    to,
		PageLimit: int32(clampLimit(q.Limit)),
	})
	if err != nil {
		return Usage{}, fmt.Errorf("read turn costs: %w", err)
	}

	out := Usage{
		Days:  make([]UsageDay, 0, len(dayRows)),
		Costs: make([]TurnCost, 0, len(costRows)),
	}
	// The totals are summed from the day rows rather than from Costs, which is a capped
	// page: a range with more turns than the cap would otherwise under-report its own total.
	for _, r := range dayRows {
		out.Days = append(out.Days, UsageDay{
			Date:       r.Day,
			CostUSD:    r.CostUsd,
			Turns:      int(r.Turns),
			ModelTurns: int(r.ModelTurns),
			Unpriced:   int(r.Unpriced),
		})
		out.TotalCostUSD += r.CostUsd
		out.TotalTurns += int(r.Turns)
		out.TotalModelTurns += int(r.ModelTurns)
		out.Unpriced += int(r.Unpriced)
	}
	for _, r := range costRows {
		c := TurnCost{
			TurnID:     r.ID,
			TaskID:     deref(r.TaskID),
			SessionID:  r.SessionID,
			SourceKind: r.SourceKind,
			SourceKey:  r.SourceKey,
			Playbook:   r.Playbook,
			Profile:    r.Profile,
			Status:     r.Status,
			StartedAt:  r.StartedAt.UTC(),
			FinishedAt: utcPtr(r.FinishedAt),
			CostUSD:    r.CostUsd,
		}
		if r.NumTurns != nil {
			n := int(*r.NumTurns)
			c.NumTurns = &n
		}
		out.Costs = append(out.Costs, c)
	}
	return out, nil
}
