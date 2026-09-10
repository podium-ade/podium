package conductor

// A sink is one conversation, seen from the side that has something to say into it: the
// message a progress edit replaces, the answer, and the files the answer asked for.
//
// It exists because three different things now say those things — a task's event stream
// (turn.go), a turn running on this host over its own socket (host.go), and a task a host
// turn delegated (delegate.go) — and the rules about *how* they are said are subtle enough
// that a second implementation of them would drift within a week: hold progress and replace
// it rather than adding a message, flush what is held before an answer lands so a stale
// "working on it" never sits above the final, join an answer the runner split at 32 KiB,
// deduplicate the names the answer asked to attach, and refuse to relay a file too big for
// the conversation instead of proxying it.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/podium"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

type sink struct {
	c   *Conductor
	src Source
	ref string
	// taskID is the task this output came from. It is empty for a host turn, which has no
	// task: a source uses it only to say where an answer came from.
	taskID string
	// placeholder is the "working…" message progress edits replace. It is empty for a
	// resumed turn and for a delegated task, whose progress is posted as new messages
	// instead — there is nothing of theirs on screen to replace.
	placeholder string
	// heldProgress is a progress text waiting for the throttle to let it through.
	heldProgress string
	haveHeld     bool
	lastEdit     time.Time
	// finals is every final text this run relayed, in order. The runtime splits an answer
	// over 32 KiB into several consecutive finals; joined with a blank line they are the
	// whole answer again.
	finals []string
	// attachments are artifact names the finals asked for, in order, deduplicated.
	attachments []string
}

// answer is every final joined back into the one thing the task said.
func (s *sink) answer() string { return strings.Join(s.finals, "\n\n") }

// said reports whether anything was relayed at all, which is what separates "it failed and
// explained itself" from "it failed silently".
func (s *sink) said() bool { return len(s.finals) > 0 }

// attachAll resolves the names the answer asked for against what the task actually stored,
// and puts each one into the conversation. skip is for a file the caller owns and does not
// want attached — the chat title is runtime-owned and is never a download.
func (s *sink) attachAll(ctx context.Context, byName map[string]*podiumv1.Artifact, skip ...string) {
	for _, name := range s.attachments {
		if slices.Contains(skip, name) {
			continue
		}
		art, ok := byName[name]
		if !ok {
			// Normal, not an error: a name that matches nothing is how the runtime says
			// "I mentioned a file I did not produce".
			s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Text: fmt.Sprintf(
				"no artifact named `%s` was produced", name)})
			continue
		}
		s.attach(ctx, art)
	}
}

// relay says one message, exactly once. MarkRelayed is the ledger: the insert either takes
// (task_id, seq) or finds it taken, and only the run that took it speaks.
func (s *sink) relay(ctx context.Context, e *podiumv1.TaskEvent) {
	first, err := s.c.store.MarkRelayed(ctx, s.taskID, e.GetSeq())
	if err != nil {
		// Saying nothing is recoverable — the next start resumes from the last seq that
		// *was* recorded — and saying something twice is not.
		s.c.logger.ErrorContext(ctx, "claiming a message for relay failed; not posting it",
			"task_id", s.taskID, "seq", e.GetSeq(), "error", err)
		return
	}
	if !first {
		return
	}
	msg := e.GetMessage()
	if msg == nil {
		return
	}
	s.deliver(ctx, msg.GetType(), msg.GetText(), msg.GetAttachments())
}

// deliver says one message. It is the half of the relay that does not care where the
// message came from: a task's event stream (relay) or the socket of a turn running on this
// host (host.go). Everything about progress throttling, joining an answer split over 32 KiB
// and remembering what it asked to attach lives here, once.
func (s *sink) deliver(ctx context.Context, msgType, text string, attachments []string) {
	s.c.metrics.RelayedMessages.WithLabelValues(msgType).Inc()

	if msgType != OutFinal {
		// Anything that is not a final is progress, whatever it called itself.
		s.heldProgress = text
		s.haveHeld = true
		s.maybeEdit(ctx)
		return
	}
	// A held progress edit is flushed before the answer lands, so the thread never shows
	// a stale "working on it" above the final.
	s.flushProgress(ctx)
	s.finals = append(s.finals, text)
	for _, name := range attachments {
		if !slices.Contains(s.attachments, name) {
			s.attachments = append(s.attachments, name)
		}
	}
	if text == "" {
		return
	}
	s.c.post(ctx, s.src, s.ref, Outbound{Type: OutFinal, TaskID: s.taskID, Text: text})
}

// maybeEdit shows the held progress if the throttle allows it. There is no timer: the next
// progress or the final flushes whatever is held, and the runtime coalesces progress to at
// most one every five seconds anyway (step 16), so the throttle almost never engages.
func (s *sink) maybeEdit(ctx context.Context) {
	if time.Since(s.lastEdit) < progressThrottle {
		return
	}
	s.flushProgress(ctx)
}

// flushProgress edits the placeholder to the newest progress text, or posts it as a new
// message when there is no placeholder to edit (a resumed turn).
func (s *sink) flushProgress(ctx context.Context) {
	if !s.haveHeld {
		return
	}
	text := s.heldProgress
	s.haveHeld = false
	s.heldProgress = ""
	s.lastEdit = time.Now()
	if strings.TrimSpace(text) == "" {
		return
	}
	out := Outbound{Type: OutProgress, TaskID: s.taskID, Text: ProgressPrefix + text}
	if s.placeholder == "" {
		s.placeholder = s.c.post(ctx, s.src, s.ref, out)
		return
	}
	if err := s.src.Edit(ctx, s.ref, s.placeholder, out); err != nil {
		s.c.logger.WarnContext(ctx, "editing the progress message failed",
			"ref", s.ref, "error", err)
		return
	}
	// An edit is said out loud too, and it is the only progress a Slack thread ever shows
	// after the first line — so it goes into the copy exactly as a post does.
	s.c.mirrorSaid(ctx, s.src, s.ref, out)
}

// attach streams one artifact into the conversation. Nothing is buffered: an artifact may
// be hundreds of megabytes, and one over the relay limit is refused with a line in the
// thread rather than proxied.
func (s *sink) attach(ctx context.Context, art *podiumv1.Artifact) {
	if art.GetSizeBytes() > podium.MaxAttachmentBytes {
		s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Text: fmt.Sprintf(
			"`%s` is %d MB, which is more than I will relay. It is on the task: `podium artifact get %s`.",
			art.GetName(), art.GetSizeBytes()>>20, art.GetId())})
		return
	}
	body, contentType, err := s.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		if errors.Is(err, podium.ErrTooLarge) {
			s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Text: fmt.Sprintf(
				"`%s` is too large for me to relay. It is on the task: `podium artifact get %s`.",
				art.GetName(), art.GetId())})
			return
		}
		s.c.logger.WarnContext(ctx, "downloading an attachment failed",
			"artifact_id", art.GetId(), "name", art.GetName(), "error", err)
		return
	}
	defer func() { _ = body.Close() }()
	if err := s.src.Attach(ctx, s.ref, Attachment{
		Name:        art.GetName(),
		ContentType: contentType,
		Size:        art.GetSizeBytes(),
		Body:        body,
		TaskID:      s.taskID,
		ArtifactID:  art.GetId(),
	}); err != nil {
		s.c.logger.WarnContext(ctx, "attaching a file to the conversation failed",
			"artifact_id", art.GetId(), "name", art.GetName(), "error", err)
	}
}
