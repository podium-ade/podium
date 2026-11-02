package conductor

// Telling a person their delegated task finished, when they are no longer in the
// conversation to see it.
//
// A task takes minutes or hours, and somebody who asked for one in the web chat has usually
// gone by the time it answers. Its answer is posted in the chat regardless; this is the
// nudge that it is there. It goes out only for a conversation whose person has said nothing
// for NotifyAwayAfter, and only from a source with nobody of its own to notify them — a Slack
// thread, a pull request and a Linear issue already notify the people in them.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// Notifier tells one person something, outside any conversation: a Slack direct message, an
// email. login is the person's sign-in, an email address.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, login, subject, body string) error
}

// DefaultNotifyAwayAfter is Options.NotifyAwayAfter's default.
const DefaultNotifyAwayAfter = 10 * time.Minute

// notifyAway tells the person who asked for dlg how it ended, if they have been away from its
// conversation since. The first notifier that delivers is the only one used.
func (c *Conductor) notifyAway(ctx context.Context, src Source, ref string, dlg store.Delegation, status, answer string) {
	if len(c.notifiers) == 0 || c.store == nil {
		return
	}
	sess, err := c.store.GetSession(ctx, dlg.SessionID)
	if err != nil {
		return
	}
	login := c.asker(ctx, src, sess)
	if login == "" {
		return
	}
	if !c.awayFrom(ctx, src, ref) {
		return
	}
	title, link := "a conversation", ""
	if chat, err := c.store.ChatBySourceKey(ctx, sess.SourceKey); err == nil {
		if chat.Title != "" {
			title = "“" + chat.Title + "”"
		}
		if c.publicURL != "" {
			link = strings.TrimSuffix(c.publicURL, "/") + "/agent/chat/" + chat.ID
		}
	}
	outcome := map[string]string{
		store.TurnSucceeded: "finished", store.TurnFailed: "failed", store.TurnCancelled: "was cancelled",
		store.TurnTimeout: "ran out of time", store.TurnLost: "was lost",
	}[status]
	if outcome == "" {
		outcome = "ended (" + status + ")"
	}
	subject := fmt.Sprintf("Your %s task %s", dlg.Playbook, outcome)
	var b strings.Builder
	fmt.Fprintf(&b, "The `%s` task you asked for in %s %s.", dlg.Playbook, title, outcome)
	if first := firstLine(answer, 280); first != "" {
		fmt.Fprintf(&b, "\n\n%s", first)
	}
	if link != "" {
		fmt.Fprintf(&b, "\n\n%s", link)
	}
	for _, n := range c.notifiers {
		err := n.Notify(ctx, login, subject, b.String())
		if err == nil {
			c.metrics.Notifications.WithLabelValues(n.Name()).Inc()
			c.logger.InfoContext(ctx, "told the person their task ended", "delegation_id", dlg.ID, "via", n.Name())
			return
		}
		c.logger.WarnContext(ctx, "notifying a person failed; trying the next way", "via", n.Name(), "error", err)
	}
}

// awayFrom is whether the person has said nothing in the conversation at ref for
// NotifyAwayAfter. A conversation whose history cannot be read counts as away: a nudge too
// many is cheaper than an answer nobody finds.
func (c *Conductor) awayFrom(ctx context.Context, src Source, ref string) bool {
	entries, err := src.FetchTranscript(ctx, ref)
	if err != nil {
		return true
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role != RoleUser {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, entries[i].TS)
		if err != nil {
			return true
		}
		return time.Since(ts) >= c.notifyAwayAfter
	}
	return true
}
