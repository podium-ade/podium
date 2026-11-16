package conductor

// What the conductor keeps about a Linear issue's conversation beyond its record (0048): whether
// it waits on a person, for the Linear view's "waiting" state and its "needs attention" filter,
// and the question in that person's inbox until it is answered (0051).
// The rest — the issue's title, team and state, and who touched it — the Linear source hands the
// store itself as the deliveries arrive.

import (
	"context"
	"time"

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
// asks one, naming whose turn asked, and puts the question in that person's inbox. A failure is
// logged: the view and the inbox are conveniences, and the question is in the conversation.
func (c *Conductor) noteLinearQuestion(ctx context.Context, src Source, ref string, out Outbound) {
	key := c.conversationKey(ctx, src, ref)
	if !store.IsLinearConversation(key) {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if err != nil {
		chat = store.Chat{}
	}
	asker := c.questionAsker(ctx, key, chat.ID, out.TaskID)
	if err := c.store.SetLinearIssueAsked(ctx, key, true, asker); err != nil {
		c.logger.WarnContext(ctx, "recording that a Linear conversation waits on a person failed",
			"source_key", key, "error", err)
	}
	if asker == "" || chat.ID == "" {
		return
	}
	if err := c.store.NotifyQuestion(ctx, asker, chat.ID, 0, "", out.Text); err != nil {
		c.logger.WarnContext(ctx, "putting a Linear question in the inbox failed",
			"source_key", key, "login", asker, "error", err)
	}
}

// questionAsker is who a question in a Linear conversation is asked of: the person whose turn
// asked it — who prompted it, or a routine's owner for its run — or "" for a turn that ran as
// nobody (a system routine's). The turn is the one that ran the task that asked, when the
// question came from a task, which is right even after the turn that delegated it has ended
// and another has started; otherwise it is the turn this conductor is running there.
func (c *Conductor) questionAsker(ctx context.Context, key, chatID, taskID string) string {
	if chatID != "" {
		if turns, err := c.store.ChatTurnRunners(ctx, chatID); err == nil {
			if t := store.NewTurnIndex(turns).TurnOf(taskID, time.Now().UTC()); t != nil {
				return t.Login
			}
		}
	}
	sess, err := c.store.GetSessionByKey(ctx, key)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.sessions[sess.ID]; st != nil && !isRoutineThread(st.turnThread) {
		return st.turnLogin
	}
	return ""
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
	// Answered — in Linear, in the chat or from the inbox, which is the chat: the item in the
	// asked person's inbox goes with it.
	if _, err := c.store.ResolveQuestions(ctx, key); err != nil {
		c.logger.WarnContext(ctx, "closing a Linear conversation's question in the inbox failed",
			"source_key", key, "error", err)
	}
}
