// Package routines is the conductor's scheduler for routines (docs/agent.md, "Routines"): a
// recurring job an agent does in one chat of its own. The leading conductor ticks; each due
// routine is fired as a turn in its chat, and each run is settled once its turn has ended.
package routines

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MinPersonalInterval is the shortest gap a personal routine's schedule may leave between two
// runs. A run is a model turn, and a person's routine that fires every minute is a bill, not a
// job.
const MinPersonalInterval = 15 * time.Minute

// maxJitter bounds the fixed offset each routine runs at past its scheduled minute, so routines
// that all say "9:00" do not all start in the same second.
const maxJitter = 60 * time.Second

// ErrInvalidSchedule is a schedule that cannot be used. The message says why.
var ErrInvalidSchedule = errors.New("invalid schedule")

// parser reads standard five-field cron (minute hour day-of-month month day-of-week) and the
// @hourly/@daily/@weekly/@monthly descriptors. Seconds are not a field: the scheduler ticks in
// tens of seconds and the floor is minutes.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule is a parsed cron expression in a timezone.
type Schedule struct {
	Expr     string
	Location *time.Location
	spec     cron.Schedule
}

// Parse reads expr in the IANA timezone tz.
func Parse(expr, tz string) (Schedule, error) {
	expr = strings.Join(strings.Fields(expr), " ")
	if expr == "" {
		return Schedule{}, fmt.Errorf("%w: a schedule is required", ErrInvalidSchedule)
	}
	if strings.HasPrefix(strings.ToUpper(expr), "TZ=") || strings.HasPrefix(strings.ToUpper(expr), "CRON_TZ=") {
		return Schedule{}, fmt.Errorf("%w: set the timezone on the routine, not in the expression", ErrInvalidSchedule)
	}
	if strings.HasPrefix(expr, "@every") {
		return Schedule{}, fmt.Errorf("%w: use a cron expression such as \"*/30 * * * *\" rather than @every", ErrInvalidSchedule)
	}
	tz = strings.TrimSpace(tz)
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Schedule{}, fmt.Errorf("%w: %q is not a timezone (use an IANA name such as America/Toronto)", ErrInvalidSchedule, tz)
	}
	spec, err := parser.Parse(expr)
	if err != nil {
		return Schedule{}, fmt.Errorf("%w: %q is not a cron expression: %v", ErrInvalidSchedule, expr, err)
	}
	s := Schedule{Expr: expr, Location: loc, spec: spec}
	if s.next(time.Now()).IsZero() {
		return Schedule{}, fmt.Errorf("%w: %q never runs", ErrInvalidSchedule, expr)
	}
	return s, nil
}

func (s Schedule) next(after time.Time) time.Time {
	return s.spec.Next(after.In(s.Location))
}

// Next is the first time after `after` the schedule names, in UTC.
func (s Schedule) Next(after time.Time) time.Time {
	return s.next(after).UTC()
}

// NextFor is a routine's next run after `after`: the schedule's next time plus the routine's
// own fixed jitter. A tick a few seconds late does not skip a run, because the jitter is taken
// off before the schedule is asked.
func (s Schedule) NextFor(routineID string, after time.Time) time.Time {
	j := Jitter(routineID)
	return s.Next(after.Add(-j)).Add(j)
}

// NextAfterFiring is a routine's next run once the run due at fired has been fired at now:
// the first scheduled minute after fired's own minute, plus the routine's jitter, and never at
// or before now. fired may carry a different jitter from the routine's — a routine's first
// run is worked out before it has an id — so its minute, not its exact time, is what counts.
func (s Schedule) NextAfterFiring(routineID string, fired, now time.Time) time.Time {
	j := Jitter(routineID)
	next := s.Next(fired.Truncate(time.Minute)).Add(j)
	for i := 0; !next.After(now) && i < 100000; i++ {
		next = s.Next(next.Add(-j)).Add(j)
	}
	return next
}

// MinGap is the shortest gap between two consecutive runs over the next week, or the next
// 2000 runs, whichever comes first. It is what the floor is checked against: "*/5 9 * * *"
// runs only in one hour a day, and every five minutes inside it.
func (s Schedule) MinGap(from time.Time) time.Duration {
	horizon := from.Add(8 * 24 * time.Hour)
	prev := s.next(from)
	if prev.IsZero() {
		return 0
	}
	minGap := time.Duration(0)
	for i := 0; i < 2000 && prev.Before(horizon); i++ {
		next := s.next(prev)
		if next.IsZero() {
			break
		}
		if gap := next.Sub(prev); minGap == 0 || gap < minGap {
			minGap = gap
		}
		prev = next
	}
	return minGap
}

// CheckFloor refuses a schedule that runs more often than floor.
func (s Schedule) CheckFloor(floor time.Duration, now time.Time) error {
	if gap := s.MinGap(now); gap > 0 && gap < floor {
		return fmt.Errorf("%w: %q runs every %s at its most frequent; a routine may run at most every %s",
			ErrInvalidSchedule, s.Expr, words(gap), words(floor))
	}
	return nil
}

// Jitter is a routine's fixed offset past its scheduled minute, from its id.
func Jitter(routineID string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(routineID))
	return time.Duration(h.Sum32()%uint32(maxJitter/time.Second)) * time.Second
}

func words(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/time.Minute), "minute")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe says a schedule in words for the common shapes, and falls back to the expression.
func Describe(expr string) string {
	expr = strings.Join(strings.Fields(expr), " ")
	switch expr {
	case "@hourly":
		return "every hour"
	case "@daily", "@midnight":
		return "every day at 00:00"
	case "@weekly":
		return "every Sunday at 00:00"
	case "@monthly":
		return "on the 1st of every month at 00:00"
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return expr
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	if dom != "*" || month != "*" {
		return expr
	}
	if n, ok := step(minute); ok && hour == "*" && dow == "*" {
		return "every " + plural(n, "minute")
	}
	m, mok := number(minute, 59)
	if hour == "*" && mok && dow == "*" {
		return fmt.Sprintf("every hour at :%02d", m)
	}
	if n, ok := step(hour); ok && mok && dow == "*" {
		return fmt.Sprintf("every %s at :%02d", plural(n, "hour"), m)
	}
	h, hok := number(hour, 23)
	if !mok || !hok {
		return expr
	}
	at := fmt.Sprintf("%02d:%02d", h, m)
	switch dow {
	case "*":
		return "every day at " + at
	case "1-5", "MON-FRI", "mon-fri":
		return "weekdays at " + at
	case "0,6", "6,0", "SAT,SUN", "sat,sun":
		return "weekends at " + at
	}
	if d, ok := number(dow, 7); ok {
		return "every " + weekdays[d%7] + " at " + at
	}
	return expr
}

func step(field string) (int, bool) {
	rest, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

func number(field string, highest int) (int, bool) {
	n, err := strconv.Atoi(field)
	return n, err == nil && n >= 0 && n <= highest
}
