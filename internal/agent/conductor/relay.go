package conductor

// Relay: what is said in a Linear issue's conversation from the web reaches the issue
// (docs/linear-agent-plan.md, "Ticket chats").
//
// A web chat's turn is answered in the web chat only (outbox.go). A Linear issue's
// conversation is the issue's, though, and people who never open Podium follow it in Linear,
// so two things are owed there when somebody writes in it from the web:
//
//   - their message, as a comment on the issue posted by the app under their name and picture
//     (Linear's createAsUser and displayIconUrl, which an app acting as itself may set);
//   - the answer the turn it started gives — and its failure or question, and the answers of
//     the tasks it delegated — as a comment from the app, the way the app answers a ticket it
//     was not prompted on in an agent session. A web turn has no agent session of its own, and
//     posting into one a Linear person opened earlier would reopen a session that had finished
//     and put the answer far from the message it answers.
//
// Both go through the delivery outbox under the web chat's row, so they are retried, and the
// web chat says when one did not arrive. A routine's run in the conversation is not relayed:
// what a routine says on a ticket is up to its instruction and its tools.
//
// The comments are the app's own. The webhooks they cause are not prompts, and not events for
// routines: the Linear source drops a delivery the app's own user is behind, and remembers the
// comments it posted (linear/app.go, ownComment).

import (
	"context"
	"strconv"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// PersonPoster is a source that can post a person's words where a conversation lives, as
// theirs: the Linear agent app. PersonRef is the ref an OutRelay and a relayed answer are
// posted with for a conversation's key, false for a key it does not relay to.
type PersonPoster interface {
	PersonRef(sourceKey string) (string, bool)
}

// Relayer is what the web chat hands a person's message to. The conductor implements it.
type Relayer interface {
	// RelayMessage posts msg, a person's message from the web in chatID, where the chat's
	// conversation lives, when it lives somewhere that takes one. It reports whether it was
	// owed there.
	RelayMessage(ctx context.Context, chatID string, msg store.ChatMessage) bool
}

// relaySetter is the optional half of Source the conductor hands itself to.
type relaySetter interface {
	SetRelayer(Relayer)
}

// relayTarget is the source and ref a chat's words are relayed to, false for a chat whose
// conversation takes none: anything but a Linear issue's, or a conductor without the app.
func (c *Conductor) relayTarget(chat store.Chat) (Source, string, bool) {
	if kind, _ := store.LinearKind(chat.SourceKey); kind != store.LinearKindIssue {
		return nil, "", false
	}
	src := c.sourceOfKind(SourceLinear)
	pp, ok := src.(PersonPoster)
	if !ok {
		return nil, "", false
	}
	ref, ok := pp.PersonRef(chat.SourceKey)
	if !ok {
		return nil, "", false
	}
	return src, ref, true
}

// RelayMessage implements Relayer.
func (c *Conductor) RelayMessage(ctx context.Context, chatID string, msg store.ChatMessage) bool {
	if c.store == nil || msg.Role != store.RoleUser || msg.AuthorLogin == "" {
		return false
	}
	chat, err := c.store.GetChat(ctx, chatID)
	if err != nil {
		return false
	}
	src, ref, ok := c.relayTarget(chat)
	if !ok {
		return false
	}
	name, avatar, err := c.store.MemberProfile(ctx, msg.AuthorLogin)
	if err != nil || name == "" {
		name = msg.AuthorLogin
	}
	out := Outbound{Type: OutRelay, Text: msg.Text, Author: name, AuthorAvatar: avatar}
	return c.owe(ctx, src, ref, chatID, msg.Seq, out)
}

// relayAnswer owes the issue the answer a web chat's turn in its conversation gave. id is what
// the chat source's Post returned: the answer's seq.
func (c *Conductor) relayAnswer(ctx context.Context, src Source, chatID, id string, out Outbound) {
	if c.store == nil || src == nil || src.Kind() != SourceChat {
		return
	}
	seq, err := strconv.ParseUint(id, 10, 64)
	if err != nil || seq == 0 {
		return
	}
	chat, err := c.store.GetChat(ctx, chatID)
	if err != nil {
		return
	}
	target, ref, ok := c.relayTarget(chat)
	if !ok {
		return
	}
	// Only a person's turn: a routine's run, or a turn that ran as nobody, says nothing on the
	// issue but what its tools say. The task's turn when there is a task, the running one when
	// there is not.
	turns, err := c.store.ChatTurnRunners(ctx, chatID)
	if err != nil {
		c.logger.WarnContext(ctx, "reading whose turn answered a Linear conversation failed; not posting it to the issue",
			"chat_id", chatID, "error", err)
		return
	}
	t := store.NewTurnIndex(turns).TurnOf(out.TaskID, time.Now().UTC())
	if !relayable(t) {
		return
	}
	c.owe(ctx, target, ref, chatID, seq, out)
}

// relayable is whether what a turn said is posted to its issue: a person's turn, not a
// routine's run.
func relayable(t *store.TurnRunner) bool {
	return t != nil && t.Login != "" && t.Routine == ""
}

// owe records one delivery of the chat row chatID/seq to src at ref and makes its first
// attempt, as a fanned-out copy is (DeliverTo).
func (c *Conductor) owe(ctx context.Context, src Source, ref, chatID string, seq uint64, out Outbound) bool {
	it, err := c.store.EnqueueOutbox(ctx, chatID, seq, src.Kind(), ref, payloadOf(out), time.Now().Add(outboxGrace))
	if err != nil {
		c.logger.WarnContext(ctx, "recording a delivery to a Linear issue failed; it is not posted",
			"chat_id", chatID, "seq", seq, "type", out.Type, "error", err)
		return false
	}
	_, _ = c.attemptDelivery(stripDelivery(ctx), src, it, out, true)
	// The row it hangs on was published before it was owed: tell the chat where it went.
	c.announceDelivery(ctx, chatID, seq)
	return true
}
