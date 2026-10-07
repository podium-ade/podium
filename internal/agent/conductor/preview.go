package conductor

import (
	"fmt"
	"sort"
	"strings"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// previewNote is what the thread is told about a finished task whose environment is still
// up: where each port is, and until when. Empty for a task with no live preview.
func previewNote(task *podiumv1.Task) string {
	p := task.GetPreview()
	if p == nil || p.GetReleasedAt() != nil || p.GetExpiresAt() == nil || len(p.GetUrls()) == 0 {
		return ""
	}
	names := make([]string, 0, len(p.GetUrls()))
	for name := range p.GetUrls() {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "The preview is up until %s. End it sooner with `podium task release %s`.",
		p.GetExpiresAt().AsTime().UTC().Format("15:04 MST"), task.GetId())
	for _, name := range names {
		fmt.Fprintf(&b, "\n• %s: %s", name, p.GetUrls()[name])
	}
	return b.String()
}
