package conductor

import (
	"context"
	"errors"
	"strings"

	"github.com/podium-ade/podium/internal/agent/store"
)

// The mirror is a read-only COPY, in chats/chat_messages, of a conversation that lives
// somewhere else. It exists so a Slack thread can be read in the Podium UI beside the web
// chats — with the names of the people in it, which the turns table has never held.
//
// Since Phase 3 of docs/agent.md it is also THE record: a turn is handed
// its transcript from here (transcript.go), not from the source, so what was said in Podium
// beside a thread is in the turn's view of it. Edits and deletes on the surface are applied
// to the rows (Phase 2), which is what keeps the record and the thread agreeing. (Migration
// 0014's "never read back into a brief" describes the mirror as it was then.)
//
// What it keeps is what the web chat keeps for a conversation it owns: the questions, the
// answers, the failures, and every progress line — including the "working on this in a task"
// announcement, whose task id is the only link a reader has from the thread to the task. In
// Slack the progress line is one message that is edited in place, so the thread shows only
// the newest; the mirror keeps each edit as its own row, as the web chat does, because the
// trail is what makes a turn readable afterwards. Only the 👀 placeholder is dropped: it is
// the conductor saying it has started, not a word from the turn.

// mirrorTitleRunes bounds a mirrored conversation's title. It matches the cap the web chat
// uses for a title taken from a first message.
const mirrorTitleRunes = 48

// MirrorWatcher is whoever streams a mirrored chat to a screen: the web chat's broadcaster.
// The mirror writes rows straight into the store — the web source must not, or it would
// double every one of its own — so the row has to be announced separately, and this is how.
type MirrorWatcher interface {
	Mirrored(chatID string, msg store.ChatMessage)
}

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
// person who asked first becomes the conversation's attribution (started_by), and a speaker
// who resolved to a Podium login becomes a participant — which is what lets them see the
// conversation — and the chat's owner when it has none yet.
//
// It reports whether this arrival is the one that recorded the message, which is the
// conductor's dedupe: the same Slack event retried, a GitHub webhook redelivered, or two
// conductors hearing one event all reach here, and only the arrival whose insert wrote the
// row goes on to start a turn. Everything that is not a definite duplicate reports true — a
// source that is not mirrored, a message with no identity of its own, and a mirror write that
// failed — because the mirror is how a turn is READ, and a database blip must not stop the
// bot answering.
func (c *Conductor) mirrorHeard(ctx context.Context, src Source, ev InboundEvent) bool {
	key, ok := c.mirrorKey(src, ev.Ref)
	if !ok {
		return true
	}
	chat, created, err := c.mirrorChat(ctx, src, key, ev)
	if err != nil {
		c.logger.WarnContext(ctx, "mirroring what was said failed", "source_key", key, "error", err)
		return true
	}
	c.recordParticipant(ctx, chat, ev)
	if ev.ChannelName != "" && chat.Channel != ev.ChannelName {
		if err := c.store.SetChatChannel(ctx, chat.ID, ev.ChannelName); err != nil {
			c.logger.WarnContext(ctx, "recording a mirrored chat's channel name failed",
				"chat_id", chat.ID, "channel", ev.ChannelName, "error", err)
		} else {
			chat.Channel = ev.ChannelName
		}
	}
	if created {
		c.seedHistory(ctx, src, chat, ev)
	}
	return c.mirrorRecord(ctx, src, chat.ID, ev, !ev.RecordOnly)
}

// mirrorChat finds the chat mirroring a conversation, or opens it. created is true only for
// the call that opened it. Two conductors opening the same conversation at once race on the
// unique source_key; the loser reads the winner's row rather than failing.
func (c *Conductor) mirrorChat(
	ctx context.Context, src Source, key string, ev InboundEvent,
) (store.Chat, bool, error) {
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return chat, false, err
	}
	chat, err = c.store.CreateMirrorChat(ctx, key, src.Kind(), ev.Author, firstLine(recordText(ev), mirrorTitleRunes))
	if err == nil {
		return chat, true, nil
	}
	if again, rerr := c.store.ChatBySourceKey(ctx, key); rerr == nil {
		return again, false, nil
	}
	return store.Chat{}, false, err
}

// mirrorRecordOnly records a message nobody put to Podium, into a conversation Podium already
// holds. A conversation it does not hold is not opened for it: Podium keeps the whole of a
// thread it is part of and nothing of one it is not, which is also what keeps a busy
// channel's chatter out of the database.
func (c *Conductor) mirrorRecordOnly(ctx context.Context, src Source, ev InboundEvent) {
	key, ok := c.mirrorKey(src, ev.Ref)
	if !ok {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		c.metric(func(m *Metrics) { m.MirrorRecords.WithLabelValues(src.Kind(), "no_chat").Inc() })
		return
	}
	if err != nil {
		c.logger.WarnContext(ctx, "finding the chat for an unaddressed message failed",
			"source_key", key, "error", err)
		return
	}
	c.recordParticipant(ctx, chat, ev)
	c.mirrorRecord(ctx, src, chat.ID, ev, false)
}

// mirrorChanged applies an edit or a delete made on the message's own surface. It never
// starts a turn: an edit that adds a mention is still an edit (decision 34). A change to a
// message Podium never recorded is nothing to do.
func (c *Conductor) mirrorChanged(ctx context.Context, src Source, ev InboundEvent) {
	key, ok := c.mirrorKey(src, ev.Ref)
	if !ok || ev.ExternalID == "" {
		return
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if err != nil {
		return
	}
	at := ev.TS
	if at.IsZero() {
		at = c.now()
	}
	via := viaOf(src, ev)
	var row store.ChatMessage
	switch ev.Change {
	case ChangeEdited:
		row, err = c.store.EditChatMessage(ctx, chat.ID, via, ev.ExternalID, recordText(ev), at.UTC())
	case ChangeDeleted:
		row, err = c.store.DeleteChatMessage(ctx, chat.ID, via, ev.ExternalID, at.UTC())
	default:
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		c.metric(func(m *Metrics) { m.MirrorRecords.WithLabelValues(src.Kind(), "change_unknown").Inc() })
		return
	}
	if err != nil {
		c.logger.WarnContext(ctx, "applying a change to a mirrored message failed",
			"chat_id", chat.ID, "change", ev.Change, "external_id", ev.ExternalID, "error", err)
		return
	}
	c.metric(func(m *Metrics) { m.MirrorRecords.WithLabelValues(src.Kind(), ev.Change).Inc() })
	if c.mirror != nil {
		c.mirror.Mirrored(chat.ID, row)
	}
}

// historySource is the optional half of a mirrored Source that can read what was said in a
// conversation before Podium was in it. Slack implements it with conversations.replies: a
// mention in a thread that already had replies would otherwise start the record half way.
// What it returns is in order, oldest first, every event RecordOnly, and never the message
// that brought Podium in.
type historySource interface {
	History(ctx context.Context, ref string) ([]InboundEvent, error)
}

// seedHistory records what the conversation said before Podium's chat for it existed. It runs
// once, for the call that opened the chat, so the earlier replies take the seqs before the
// message that brought Podium in. A failure leaves the record starting where Podium came in,
// which is what it did before there was a history at all.
func (c *Conductor) seedHistory(ctx context.Context, src Source, chat store.Chat, trigger InboundEvent) {
	h, ok := src.(historySource)
	if !ok {
		return
	}
	earlier, err := h.History(ctx, trigger.Ref)
	if err != nil {
		c.logger.WarnContext(ctx, "reading a conversation's earlier messages failed",
			"chat_id", chat.ID, "error", err)
		return
	}
	for _, ev := range earlier {
		if ev.ExternalID != "" && ev.ExternalID == trigger.ExternalID {
			continue
		}
		ev.RecordOnly = true
		c.resolveSpeaker(ctx, src, &ev)
		c.recordParticipant(ctx, chat, ev)
		c.mirrorRecord(ctx, src, chat.ID, ev, false)
	}
}

// mirrorRecord writes one human message into a mirrored chat and announces it. It reports
// false only for a message that was already there.
func (c *Conductor) mirrorRecord(ctx context.Context, src Source, chatID string, ev InboundEvent, addressed bool) bool {
	msg := store.ChatMessage{
		ChatID:       chatID,
		Role:         RoleUser,
		Author:       ev.Author,
		Text:         recordText(ev),
		TS:           ev.TS,
		AuthorLogin:  ev.Login,
		AuthorHandle: ev.SpeakerHandle,
		AuthorAvatar: ev.SpeakerAvatar,
		Via:          viaOf(src, ev),
		ExternalID:   ev.ExternalID,
		ExternalURL:  ev.ExternalURL,
		Addressed:    addressed,
	}
	row, fresh, err := c.store.RecordChatMessage(ctx, msg)
	if err != nil {
		c.logger.WarnContext(ctx, "appending to a mirrored conversation failed",
			"chat_id", chatID, "role", msg.Role, "error", err)
		return true
	}
	if !fresh {
		c.metric(func(m *Metrics) { m.MirrorRecords.WithLabelValues(src.Kind(), "duplicate").Inc() })
		return false
	}
	outcome := "addressed"
	if !addressed {
		outcome = "unaddressed"
	}
	c.metric(func(m *Metrics) { m.MirrorRecords.WithLabelValues(src.Kind(), outcome).Inc() })
	if c.mirror != nil {
		c.mirror.Mirrored(chatID, row)
	}
	return true
}

// recordText is what the record keeps of a human message: their words, not an instruction
// a source built around them.
func recordText(ev InboundEvent) string {
	if ev.RecordText != "" {
		return ev.RecordText
	}
	return ev.Text
}

// viaOf is the surface a human message came in through. It is the speaker's account when
// the source named one, not the source's kind: a Slack mention that names a pull request is
// carried by the GitHub source and was still said in Slack.
func viaOf(src Source, ev InboundEvent) string {
	switch ev.SpeakerProvider {
	case store.ProviderSlack:
		return store.ViaSlack
	case store.ProviderGitHub:
		return store.ViaGitHub
	}
	switch src.Kind() {
	case SourceSlack:
		return store.ViaSlack
	case SourceGitHub:
		return store.ViaGitHub
	}
	return src.Kind()
}

// mirrorSaid records one thing the bot said out loud, or edited into what it had said. An
// answer or a failure is an assistant row; a progress line is a progress row with its ⏳
// stripped, which is exactly what the web chat stores for its own — so the two kinds of
// conversation read the same way in the UI.
func (c *Conductor) mirrorSaid(ctx context.Context, src Source, ref string, out Outbound) {
	c.mirrorSaidRow(ctx, src, ref, out)
}

// mirrorSaidRow is mirrorSaid returning the row it wrote, which the delivery outbox hangs its
// rows on. False is nothing written: a source that is not mirrored, a placeholder, or a
// conversation with no chat yet.
func (c *Conductor) mirrorSaidRow(ctx context.Context, src Source, ref string, out Outbound) (store.ChatMessage, bool) {
	role := RoleAssistant
	text := out.Text
	if out.Type == OutActivity {
		role = store.RoleActivity
	}
	if out.Type == OutProgress {
		if text == Placeholder {
			return store.ChatMessage{}, false
		}
		role = store.RoleProgress
		text = strings.TrimPrefix(text, ProgressPrefix)
		if strings.TrimSpace(text) == "" {
			return store.ChatMessage{}, false
		}
	}
	key, ok := c.mirrorKey(src, ref)
	if !ok {
		return store.ChatMessage{}, false
	}
	chat, err := c.store.ChatBySourceKey(ctx, key)
	if err != nil {
		// Not found is possible and not alarming: the bot can be made to say something in
		// a thread whose first inbound message never reached the mirror.
		c.logger.WarnContext(ctx, "mirroring the answer failed; no chat for the conversation",
			"source_key", key, "error", err)
		return store.ChatMessage{}, false
	}
	author := ""
	if p := c.profiles.Current(); p != nil {
		author = p.DisplayName
	}
	return c.mirrorAppend(ctx, chat.ID, store.ChatMessage{
		ChatID: chat.ID,
		Role:   role,
		Author: author,
		Text:   text,
		TaskID: out.TaskID,
		Via:    store.ViaBot,
	})
}

func (c *Conductor) mirrorAppend(ctx context.Context, chatID string, msg store.ChatMessage) (store.ChatMessage, bool) {
	row, err := c.store.AppendChatMessage(ctx, msg)
	if err != nil {
		c.logger.WarnContext(ctx, "appending to a mirrored conversation failed",
			"chat_id", chatID, "role", msg.Role, "error", err)
		return store.ChatMessage{}, false
	}
	if c.mirror != nil {
		c.mirror.Mirrored(chatID, row)
	}
	return row, true
}

// metric applies f to the conductor's metrics, which a test may leave unset.
func (c *Conductor) metric(f func(*Metrics)) {
	if c.metrics != nil {
		f(c.metrics)
	}
}
