package conductor

import (
	"context"
	"errors"

	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// The mirror is a read-only COPY, in chats/chat_messages, of a conversation that lives
// somewhere else. It exists so a Slack thread can be read in the Podium UI beside the web
// chats — with the names of the people in it, which the turns table has never held.
//
// It is never read back into a brief. FetchTranscript is still what a turn is handed, so
// Slack remains the one authority on what was said and the mirror cannot drift into
// disagreeing with the model's own view of the conversation. That also means the mirror is
// allowed to be lossy: it keeps what a reader of the thread sees, which is the questions and
// the answers, and drops the placeholder and the ⏳ progress the source itself treats as
// noise.

// mirrorTitleRunes bounds a mirrored conversation's title. It matches the cap the web chat
// uses for a title taken from a first message.
const mirrorTitleRunes = 48

// mirrorSource is the optional half of Source that a conversation living somewhere else
// implements. It hands back the session key a ref belongs to, which is what lets the
// conductor mirror a thread without knowing how any particular source shapes a ref.
//
// The web chat does not implement it, and must not: it already stores its own messages, and
// a second writer would double every one of them.
type mirrorSource interface {
	MirrorKey(ref string) (string, bool)
}

// mirrorKey is the key this conversation is mirrored under, and false when the source is
// not mirrored at all.
func (c *Conductor) mirrorKey(src Source, ref string) (string, bool) {
	m, ok := src.(mirrorSource)
	if !ok {
		return "", false
	}
	return m.MirrorKey(ref)
}

// mirrorHeard records one thing a human said, opening the mirror on the first message. The
// person who asked first becomes the conversation's attribution: a mirrored thread has no
// Podium login to own it, and "who started this" is the useful thing to show instead.
func (c *Conductor) mirrorHeard(ctx context.Context, src Source, ev InboundEvent) {
	key, ok := c.mirrorKey(src, ev.Ref)
	if !ok {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		chat, err = c.store.CreateMirrorChat(
			ctx, key, src.Kind(), ev.Author, firstLine(ev.Text, mirrorTitleRunes))
	}
	if err != nil {
		// A conversation that cannot be mirrored is still a conversation the bot answers:
		// the mirror is how a turn is READ afterwards, never how it runs.
		c.logger.WarnContext(ctx, "mirroring what was said failed", "source_key", key, "error", err)
		return
	}
	c.mirrorAppend(ctx, chat.ID, store.ChatMessage{
		ChatID: chat.ID,
		Role:   RoleUser,
		Author: ev.Author,
		Text:   ev.Text,
		TS:     ev.TS,
	})
}

// mirrorSaid records one thing the bot said out loud. Only what a reader of the thread
// would keep: an answer or a failure, never the placeholder or the progress edits.
func (c *Conductor) mirrorSaid(ctx context.Context, src Source, ref string, out Outbound) {
	if out.Type != OutFinal && out.Type != OutFailure {
		return
	}
	key, ok := c.mirrorKey(src, ref)
	if !ok {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if err != nil {
		// Not found is possible and not alarming: the bot can be made to say something in
		// a thread whose first inbound message never reached the mirror.
		c.logger.WarnContext(ctx, "mirroring the answer failed; no chat for the conversation",
			"source_key", key, "error", err)
		return
	}
	author := ""
	if p := c.profiles.Current(); p != nil {
		author = p.DisplayName
	}
	c.mirrorAppend(ctx, chat.ID, store.ChatMessage{
		ChatID: chat.ID,
		Role:   RoleAssistant,
		Author: author,
		Text:   out.Text,
		TaskID: out.TaskID,
	})
}

func (c *Conductor) mirrorAppend(ctx context.Context, chatID string, msg store.ChatMessage) {
	if _, err := c.store.AppendChatMessage(ctx, msg); err != nil {
		c.logger.WarnContext(ctx, "appending to a mirrored conversation failed",
			"chat_id", chatID, "role", msg.Role, "error", err)
	}
}
