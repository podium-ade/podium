package conductor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// previewNote is what the thread is told about a finished task whose environment is still
// up: where each port is, and until when. Empty for a task with no live preview. askable is
// a delegated task's, which the assistant can release when asked.
func previewNote(task *podiumv1.Task, askable bool) string {
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
	fmt.Fprintf(&b, "The preview is up until %s. ", p.GetExpiresAt().AsTime().UTC().Format("15:04 MST"))
	if askable {
		b.WriteString("Tell me when you're done with it and I'll take it down, or run ")
	} else {
		b.WriteString("End it sooner with ")
	}
	fmt.Fprintf(&b, "`podium task release %s`.", task.GetId())
	for _, name := range names {
		fmt.Fprintf(&b, "\n• %s: %s", name, p.GetUrls()[name])
	}
	return b.String()
}

// previewLookback and maxBriefPreviews bound what livePreviews asks the control plane about
// on every turn: recent tasks of playbooks that expose, and only so many of them.
const (
	previewLookback  = 7 * 24 * time.Hour
	maxBriefPreviews = 10
)

// previewLive is a preview that is still up, or still coming up.
func previewLive(p *podiumv1.TaskPreview) bool {
	if p == nil || p.GetReleasedAt() != nil {
		return false
	}
	return p.GetExpiresAt() == nil || p.GetExpiresAt().AsTime().After(time.Now())
}

// livePreviews is this conversation's finished tasks whose environment is still up, for the
// brief, so "I'm done with it" has something to name. A failure costs the list, not the turn.
func (c *Conductor) livePreviews(ctx context.Context, sessionID string) []BriefPreview {
	if c.store == nil || c.podium == nil || sessionID == "" {
		return nil
	}
	rows, err := c.store.FinishedDelegationsForSession(ctx, sessionID, time.Now().Add(-previewLookback), maxBriefPreviews)
	if err != nil {
		c.logger.WarnContext(ctx, "listing the conversation's finished delegations failed",
			"session_id", sessionID, "error", err)
		return nil
	}
	profile := c.profiles.Current()
	var out []BriefPreview
	for _, d := range rows {
		if p, ok := profile.Playbooks[d.Playbook]; ok && p.Expose == nil {
			continue
		}
		task, err := c.podium.GetTask(ctx, d.TaskID)
		if err != nil {
			c.logger.WarnContext(ctx, "reading a delegated task's preview failed",
				"delegation_id", d.ID, "task_id", d.TaskID, "error", err)
			continue
		}
		p := task.GetPreview()
		if !previewLive(p) {
			continue
		}
		bp := BriefPreview{DelegationID: d.ID, Playbook: d.Playbook, TaskID: d.TaskID, URLs: p.GetUrls()}
		if p.GetExpiresAt() != nil {
			bp.ExpiresAt = BriefTimestamp(p.GetExpiresAt().AsTime())
		}
		out = append(out, bp)
	}
	return out
}

// ReleaseDelegationPreview takes down the preview a finished delegated task of this
// conversation left up, which is what a person saying they are done with it means.
func (c *Conductor) ReleaseDelegationPreview(ctx context.Context, token, id string) (store.Delegation, error) {
	dlg, _, err := c.GetDelegation(ctx, token, id)
	if err != nil {
		return store.Delegation{}, err
	}
	if dlg.Status == store.TurnRunning {
		return store.Delegation{}, fmt.Errorf("%w: its task is still running; cancel it to stop it", ErrNoPreview)
	}
	if dlg.TaskID == "" {
		return store.Delegation{}, ErrNoPreview
	}
	task, err := c.podium.ReleasePreview(ctx, dlg.TaskID)
	if err != nil {
		return store.Delegation{}, fmt.Errorf("conductor: releasing the delegated task's preview failed: %w", err)
	}
	if task.GetPreview() == nil {
		return store.Delegation{}, ErrNoPreview
	}
	return dlg, nil
}
