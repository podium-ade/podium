package routines

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRefusesWhatCannotRun(t *testing.T) {
	for _, tc := range []struct{ expr, tz, want string }{
		{"", "UTC", "required"},
		{"not cron", "UTC", "not a cron expression"},
		{"0 9 * * *", "Mars/Olympus", "not a timezone"},
		{"TZ=UTC 0 9 * * *", "UTC", "set the timezone on the routine"},
		{"@every 5m", "UTC", "rather than @every"},
		{"0 0 30 2 *", "UTC", "never runs"},
	} {
		_, err := Parse(tc.expr, tc.tz)
		require.ErrorIs(t, err, ErrInvalidSchedule, tc.expr)
		assert.ErrorContains(t, err, tc.want, tc.expr)
	}
}

func TestNextReadsTheRoutinesTimezone(t *testing.T) {
	s, err := Parse("0 9 * * *", "America/Toronto")
	require.NoError(t, err)
	// 2026-11-07 13:00 UTC is 08:00 in Toronto (EST, UTC-5), so 09:00 Toronto is 14:00 UTC.
	after := time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC), s.Next(after))
}

func TestNextForAddsAStableJitterWithoutSkippingARun(t *testing.T) {
	s, err := Parse("*/30 * * * *", "UTC")
	require.NoError(t, err)
	j := Jitter("rtn_abc")
	assert.Equal(t, j, Jitter("rtn_abc"))
	assert.Less(t, j, maxJitter)

	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	next := s.NextFor("rtn_abc", at.Add(-time.Minute))
	assert.Equal(t, at.Add(j), next)
	// A tick that lands inside the jitter window, after the minute but before the run, still
	// finds the same run rather than the one half an hour later.
	assert.Equal(t, at.Add(j), s.NextFor("rtn_abc", at.Add(j/2)))
}

func TestNextAfterFiringNeverRepeatsTheMinuteJustFired(t *testing.T) {
	s, err := Parse("0 9 * * *", "UTC")
	require.NoError(t, err)
	j := Jitter("rtn_x")
	tomorrow := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC).Add(j)
	// Fired at its own jittered time.
	fired := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC).Add(j)
	assert.Equal(t, tomorrow, s.NextAfterFiring("rtn_x", fired, fired.Add(time.Second)))
	// Fired from a time worked out with another jitter (a new routine's first run): still the
	// same minute, so still tomorrow and not later today.
	assert.Equal(t, tomorrow, s.NextAfterFiring("rtn_x", fired.Truncate(time.Minute), fired.Truncate(time.Minute)))
	// Fired hours late: the next one is after now, not a backlog.
	late := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC).Add(j), s.NextAfterFiring("rtn_x", fired, late))
}

func TestCheckFloorLooksAtTheTightestGap(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		expr string
		ok   bool
	}{
		{"*/15 * * * *", true},
		{"*/5 * * * *", false},
		{"*/5 9 * * *", false}, // only one hour a day, but every five minutes in it
		{"0 9 * * 1-5", true},
		{"@hourly", true},
	} {
		s, err := Parse(tc.expr, "UTC")
		require.NoError(t, err, tc.expr)
		err = s.CheckFloor(MinPersonalInterval, now)
		if tc.ok {
			assert.NoError(t, err, tc.expr)
		} else {
			assert.ErrorContains(t, err, "at most every 15 minutes", tc.expr)
		}
	}
}

func TestDescribeSaysTheCommonShapesInWords(t *testing.T) {
	for expr, want := range map[string]string{
		"*/15 * * * *": "every 15 minutes",
		"0 * * * *":    "every hour at :00",
		"30 */2 * * *": "every 2 hours at :30",
		"0 9 * * *":    "every day at 09:00",
		"30 8 * * 1-5": "weekdays at 08:30",
		"0 17 * * 5":   "every Friday at 17:00",
		"@daily":       "every day at 00:00",
		"0 9 1 * *":    "0 9 1 * *",
	} {
		assert.Equal(t, want, Describe(expr), expr)
	}
}
