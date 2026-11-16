package conductor

// Threads within one conversation (docs/agent.md, "Linear"). A Linear issue is one conversation
// — one session, one chat, one record — however many agent sessions are opened on it, and a
// routine may run in it too. Linear draws each agent session on its own, so what a turn says goes
// to the agent session that prompted it, which its ref names, and three things that are per
// conversation elsewhere are per thread here:
//
//   - a prompt from one agent session is never put into another's turn, or into a task
//     another's turn delegated, which would answer it in the wrong session (sameAsker);
//   - Stop in one agent session stops what that session started, not another's work or a
//     routine's run on the same issue (CancelThreadTurn);
//   - a routine's run is a thread of its own, which nothing is put into and which is put into
//     nothing.
//
// Turns still run one at a time per conversation, which is what lets a later turn see the
// earlier ones, and the tasks they started, and decide whether it is the same work.

import (
	"context"
	"strings"
)

// ThreadKeyer is the optional half of a Source whose conversations hold threads of their own:
// the Linear app, where each agent session on an issue is one. ThreadKey is the thread a ref
// answers into, "" for a ref that names none.
type ThreadKeyer interface {
	ThreadKey(ref string) string
}

// routineThreadPrefix is a routine run's thread: its own, whatever conversation it runs in.
const routineThreadPrefix = "routine:"

// threadOf is the thread ev belongs to within its conversation: its routine run's, or the one
// its source names for its ref, or "". A nil src asks every source, as a queued event carries
// its ref and not its source.
func (c *Conductor) threadOf(src Source, ev InboundEvent) string {
	if ev.Routine != nil && ev.Routine.RunID != "" {
		return routineThreadPrefix + ev.Routine.RunID
	}
	if src != nil {
		if tk, ok := src.(ThreadKeyer); ok {
			return tk.ThreadKey(ev.Ref)
		}
		return ""
	}
	return c.refThread(ev.Ref)
}

// refThread is the thread a turn's or a delegation's ref answers into, asked of every source
// that has threads: the ref does not say which source wrote it, and a conversation's turns may
// come from several (a Linear issue's from Linear, from the web and from routines). Only the
// source that wrote a ref recognises it.
func (c *Conductor) refThread(ref string) string {
	if ref == "" {
		return ""
	}
	for _, src := range c.sourceList() {
		if tk, ok := src.(ThreadKeyer); ok {
			if t := tk.ThreadKey(ref); t != "" {
				return t
			}
		}
	}
	return ""
}

// isRoutineThread reports whether a thread is a routine run's.
func isRoutineThread(thread string) bool { return strings.HasPrefix(thread, routineThreadPrefix) }

// sourceForRef is the source that wrote ref, for work resumed after a restart: the session's own
// source, unless the ref is a chat's — a turn or a delegation started from the web, or a
// routine's run, in a conversation that lives elsewhere (a Linear issue's, a pull request's),
// which answered in the chat and must be resumed there, not through the surface's source.
func (c *Conductor) sourceForRef(ctx context.Context, kind, ref string) Source {
	src := c.sourceOf(kind)
	if src == nil || src.Kind() == SourceChat || c.store == nil {
		return src
	}
	if _, ok := c.mirrorKey(src, ref); ok {
		return src
	}
	web := c.sourceOf(SourceChat)
	if web == nil {
		return src
	}
	if _, err := c.store.GetChat(ctx, ref); err == nil {
		return web
	}
	return src
}
