// Package plural says a count with its noun: "1 check", "3 checks". Three packages each keep
// a copy of this; they can share this one.
package plural

import "fmt"

// Count is n with noun, made plural with an "s" unless n is exactly one.
func Count(n int, noun string) string {
	if n > 1 {
		return fmt.Sprintf("%d %ss", n, noun)
	}
	return fmt.Sprintf("%d %s", n, noun)
}
