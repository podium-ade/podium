// Package humanize writes durations the way a person reads them in a status line.
package humanize

import (
	"fmt"
	"time"
)

// Duration says d in its two largest units, rounded down: "45s", "1m 30s", "2h 5m", "3d 4h".
// Anything under a second is "0s".
func Duration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	minutes := int(d/time.Minute) % 60
	seconds := int(d/time.Second) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 1:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
