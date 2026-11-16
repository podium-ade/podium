package conductor

// What the conductor keeps about a Linear issue's conversation beyond its record (0048): whether
// it waits on a person, for the Linear view's "waiting" state and its "needs attention" filter.
// The rest — the issue's title, team and state, and who touched it — the Linear source hands the
// store itself as the deliveries arrive.

import (
	"context"

	"github.com/podium-ade/podium/internal/agent/store"
)

// conversationKey is the session key of the conversation ref is in: the mirrored conversation
// a source's ref names, or, for a message from the web, the conversation its chat records.
func (c *Conductor) conversationKey(ctx context.Context, src Source, ref string) string {
	if src == nil || c.store == nil {
		return ""
	}
	if key, ok := c.mirrorKey(src, ref); ok {
		return key
	}
	if src.Kind() != SourceChat || ref == "" {
		return ""
	}
	chat, err := c.store.GetChat(ctx, ref)
	if err != nil {
		return ""
	}
	return chat.SourceKey
}

// noteLinearQuestion marks a Linear conversation as waiting on a person when something in it
// asks one, naming whose turn asked. A failure is logged: the view is a convenience.
func (c *Conductor) noteLinearQuestion(ctx context.Context, src Source, ref string) {
	key := c.conversationKey(ctx, src, ref)
	if !store.IsLinearConversation(key) {
		return
	}
	asker := ""
	if sess, err := c.store.GetSessionByKey(ctx, key); err == nil {
		c.mu.Lock()
		if st := c.sessions[sess.ID]; st != nil {
			asker = st.turnLogin
		}
		c.mu.Unlock()
	}
	if err := c.store.SetLinearIssueAsked(ctx, key, true, asker); err != nil {
		c.logger.WarnContext(ctx, "recording that a Linear conversation waits on a person failed",
			"source_key", key, "error", err)
	}
}

// clearLinearQuestion is somebody saying something in a Linear conversation: whatever it waited
// on has been answered, or the conversation has moved on.
func (c *Conductor) clearLinearQuestion(ctx context.Context, _ Source, ev InboundEvent) {
	if ev.Routine != nil || c.store == nil {
		return
	}
	key := ev.SourceKey
	if !store.IsLinearConversation(key) {
		return
	}
	if err := c.store.SetLinearIssueAsked(ctx, key, false, ""); err != nil {
		c.logger.WarnContext(ctx, "clearing a Linear conversation's question failed",
			"source_key", key, "error", err)
	}
}
