package plural

import "testing"

func TestCountSaysOneInTheSingularAndEveryOtherNumberInThePlural(t *testing.T) {
	for n, want := range map[int]string{0: "0 checks", 1: "1 check", 2: "2 checks", 40: "40 checks"} {
		if got := Count(n, "check"); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}
