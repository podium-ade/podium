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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/podium"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

type sink struct {
	c   *Conductor
	src Source
	ref string
	// taskID is the task this output came from. It is empty for a host turn, which has no
	// task: a source uses it only to say where an answer came from.
	taskID string
	// speaker is the playbook this output came from, on everything it says. The zero
	// Speaker for a host turn: the assistant runs no playbook.
	speaker Speaker
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
	// stored is every artifact the task's stream said it stored, by name, with the seq of
	// that event. A file the answer names is attached as soon as both are known, whichever
	// comes first, rather than when the task exits: a warm task answers and then waits
	// minutes for a follow-up, and its screenshots belong with its answer.
	stored map[string]storedArtifact
	// attached is every name this run has put into the conversation, or found already put.
	attached map[string]bool
}

// storedArtifact is one artifact event: what was stored, and where in the stream.
type storedArtifact struct {
	art *podiumv1.Artifact
	seq uint64
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
		if slices.Contains(skip, name) || s.attached[name] {
			continue
		}
		if _, ok := s.stored[name]; ok {
			s.attachStored(ctx, name)
			continue
		}
		art, ok := byName[name]
		if !ok {
			// Normal, not an error: a name that matches nothing is how the runtime says
			// "I mentioned a file I did not produce".
			s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Speaker: s.speaker, Text: fmt.Sprintf(
				"no artifact named `%s` was produced", name)})
			continue
		}
		s.attach(ctx, art)
	}
}

// onArtifact notes one artifact event, and attaches the file now if the answer already
// asked for it.
func (s *sink) onArtifact(ctx context.Context, e *podiumv1.TaskEvent) {
	if !s.noteArtifact(e) {
		return
	}
	if name := e.GetArtifact().GetName(); slices.Contains(s.attachments, name) {
		s.attachStored(ctx, name)
	}
}

// noteArtifact records an artifact event in stored, and says whether it was one.
func (s *sink) noteArtifact(e *podiumv1.TaskEvent) bool {
	ref := e.GetArtifact()
	if e.GetKind() != podiumv1.TaskEventKind_TASK_EVENT_KIND_ARTIFACT || ref.GetName() == "" {
		return false
	}
	if s.stored == nil {
		s.stored = map[string]storedArtifact{}
	}
	s.stored[ref.GetName()] = storedArtifact{seq: e.GetSeq(), art: &podiumv1.Artifact{
		Id:          ref.GetArtifactId(),
		TaskId:      s.taskID,
		Name:        ref.GetName(),
		ObjectKey:   ref.GetObjectKey(),
		SizeBytes:   ref.GetSizeBytes(),
		ContentType: ref.GetContentType(),
	}}
	return true
}

// attachStored attaches a file the stream said it stored, exactly once: the attached ledger
// is what keeps a conductor that restarted between the answer and the task's exit from
// attaching it a second time.
func (s *sink) attachStored(ctx context.Context, name string) {
	st, ok := s.stored[name]
	if !ok || s.attached[name] {
		return
	}
	if s.attached == nil {
		s.attached = map[string]bool{}
	}
	s.attached[name] = true
	first, err := s.c.store.MarkAttached(ctx, s.taskID, st.seq)
	if err != nil {
		s.c.logger.ErrorContext(ctx, "claiming an artifact for attaching failed; not attaching it",
			"task_id", s.taskID, "seq", st.seq, "name", name, "error", err)
		return
	}
	if first {
		s.attach(ctx, st.art)
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
	if msgType == MsgHeartbeat {
		// Liveness for the watchdog (watchdog.go), never said to anybody.
		return
	}
	s.c.metrics.RelayedMessages.WithLabelValues(msgType).Inc()

	if msgType == OutQuestion || msgType == MsgQuestion {
		// A question is a durable post, not progress: the human has to see it, and the
		// next progress line must not replace it. It is not a final either — the turn
		// is still running, waiting for a reply.
		s.flushProgress(ctx)
		if strings.TrimSpace(text) != "" {
			s.c.post(ctx, s.src, s.ref, Outbound{Type: OutQuestion, TaskID: s.taskID, Speaker: s.speaker, Text: text})
		}
		return
	}
	if msgType == MsgActivity {
		s.activity(ctx, text)
		return
	}
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
	s.noteFinal(text, attachments)
	if text != "" {
		s.c.post(ctx, s.src, s.ref, Outbound{Type: OutFinal, TaskID: s.taskID, Speaker: s.speaker, Text: text})
	}
	// After the text, so the files sit under the answer that asked for them. One stored
	// before the answer arrived is attached now; one stored after, when its event does.
	for _, name := range attachments {
		s.attachStored(ctx, name)
	}
}

// noteFinal records one final's text and the files it asked for.
func (s *sink) noteFinal(text string, attachments []string) {
	s.finals = append(s.finals, text)
	for _, name := range attachments {
		if !slices.Contains(s.attachments, name) {
			s.attachments = append(s.attachments, name)
		}
	}
}

// recall reads back what a task said up to and including seq upTo, which this conductor
// relayed before it restarted: the answer, the files it asked for and the files stored. It
// says none of it again. A resumed follow starts after upTo, so without this a task that
// answered before the restart and exited after it is recorded with no answer, and the
// files its answer named are never attached.
func (s *sink) recall(ctx context.Context, tasks podiumv1connect.TaskServiceClient, upTo uint64) error {
	if upTo == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, recallTimeout)
	defer cancel()
	stream, err := tasks.StreamTaskEvents(ctx, connect.NewRequest(&podiumv1.StreamTaskEventsRequest{TaskId: s.taskID}))
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		e := stream.Msg()
		if e.GetSeq() > upTo {
			return nil
		}
		if !s.noteArtifact(e) {
			if msg := e.GetMessage(); e.GetKind() == podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE &&
				msg.GetType() == OutFinal {
				s.noteFinal(msg.GetText(), msg.GetAttachments())
			}
		}
		if e.GetSeq() == upTo {
			return nil
		}
	}
	return stream.Err()
}

// recallTimeout bounds recall. Everything it reads is already stored, so it is fast or the
// control plane is not answering, and a follow that cannot start is worse than a recall
// that gave up.
const recallTimeout = 30 * time.Second

// activity hands one activity document to a source that draws it, and always to the mirrored
// copy of the conversation, when there is one. A Linear agent session does both: it shows the
// latest tool call in the session and its chat keeps the trail, as a Slack thread's does. The
// text is the task's and untrusted; it is only checked to be JSON, because a reader parses it.
func (s *sink) activity(ctx context.Context, text string) {
	if !json.Valid([]byte(text)) {
		s.c.logger.DebugContext(ctx, "dropping an activity message that is not JSON", "task_id", s.taskID)
		return
	}
	out := Outbound{Type: OutActivity, TaskID: s.taskID, Speaker: s.speaker, Text: text}
	ap, ok := s.src.(ActivityPoster)
	if !ok {
		s.c.mirrorSaid(ctx, s.src, s.ref, out)
		return
	}
	// What the turn said before this came before it, so it is shown first.
	s.flushProgress(ctx)
	if err := ap.PostActivity(ctx, s.ref, out); err != nil {
		s.c.logger.WarnContext(ctx, "posting activity to the conversation failed", "ref", s.ref, "error", err)
	}
	// The web chat stores its own and is not mirrored, so this records nothing for it.
	s.c.mirrorSaid(ctx, s.src, s.ref, out)
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
	out := Outbound{Type: OutProgress, TaskID: s.taskID, Speaker: s.speaker, Text: ProgressPrefix + text}
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
		s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Speaker: s.speaker, Text: fmt.Sprintf(
			"`%s` is %d MB, which is more than I will relay. It is on the task: `podium artifact get %s`.",
			art.GetName(), art.GetSizeBytes()>>20, art.GetId())})
		return
	}
	body, contentType, err := s.c.podium.Artifact(ctx, art.GetId())
	if err != nil {
		if errors.Is(err, podium.ErrTooLarge) {
			s.c.post(ctx, s.src, s.ref, Outbound{Type: OutProgress, TaskID: s.taskID, Speaker: s.speaker, Text: fmt.Sprintf(
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
