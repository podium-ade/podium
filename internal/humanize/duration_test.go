package humanize

import (
	"testing"
	"time"
)

func TestDurationSaysItsTwoLargestUnits(t *testing.T) {
	for d, want := range map[time.Duration]string{
		500 * time.Millisecond:            "0s",
		45 * time.Second:                  "45s",
		2*time.Hour + 5*time.Minute:       "2h 5m",
		3*24*time.Hour + 4*time.Hour + 30: "3d 4h",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestDurationSaysMinutesFromTheFirstWholeMinute(t *testing.T) {
	if got := Duration(time.Minute); got != "1m 0s" {
		t.Errorf("Duration(1m) = %q, want %q", got, "1m 0s")
	}
	if got := Duration(90 * time.Second); got != "1m 30s" {
		t.Errorf("Duration(90s) = %q, want %q", got, "1m 30s")
	}
}
