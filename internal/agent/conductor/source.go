package conductor

import (
	"context"
	"io"
	"time"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// Reaction is the three states a source can show on the message that started a turn.
type Reaction string

// The reactions. Slack maps them onto eyes / white_check_mark / x.
const (
	ReactionWorking  Reaction = "working"
	ReactionDone     Reaction = "done"
	ReactionFailed   Reaction = "failed"
	ReactionAwaiting Reaction = "awaiting"
	// ReactionRefused is "I heard you, and I will not act on it": somebody who is not in
	// Podium asked for something. Slack shows 🚫, GitHub a confused reaction. Nothing runs,
	// so no other reaction follows it.
	ReactionRefused Reaction = "refused"
)

// The changes an InboundEvent can carry instead of a new message.
const (
	ChangeEdited  = "edited"
	ChangeDeleted = "deleted"
)

// The account providers a source can name in InboundEvent.SpeakerProvider. They are the
// store's identity providers, spelled here so a source never imports the store.
const (
	SpeakerSlack  = "slack"
	SpeakerGitHub = "github"
)

// The privacies a source can name in InboundEvent.ChannelPrivacy: the store's, spelled here
// for the same reason.
const (
	ChannelPublic  = "public"
	ChannelPrivate = "private"
	ChannelIM      = "im"
	ChannelMPIM    = "mpim"
)

// Outbound types. Type is what the source may use to style a post; nothing branches on it
// in the conductor except to decide whether a post replaces the placeholder.
const (
	OutProgress = "progress"
	OutFinal    = "final"
	OutFailure  = "failure"
	OutQuestion = "question"
	OutActivity = "activity"
)

// Outbound is one thing the conductor wants said. Text is untrusted content when it came
// out of a task: the conductor posts it and interprets none of it.
type Outbound struct {
	Type string
	Text string
	// TaskID is the Podium task the turn is running, empty when the turn never got one.
	// A source uses it to say where an answer came from — Linear's comments carry a
	// footer naming it, and its fallback attachment link points at the task's page. It is
	// NOT part of Text: whether it is shown at all is the source's decision.
	TaskID string
	// Speaker is who said it. A source that posts everything the same way ignores it.
	Speaker
}

// Speaker is the playbook behind one Outbound. The zero Speaker is the assistant, or the
// conductor speaking on its own behalf.
type Speaker struct {
	// Playbook is the playbook whose task said this.
	Playbook string
	// SourcePlaybook is true when Playbook is the one this source runs on its own: the
	// github: true playbook on a pull request. A source posts that playbook's answer as the
	// work it exists for — a review — and anybody else's as a reply.
	SourcePlaybook bool
}

// InboundEvent is one thing a human said, normalised. A source produces these and nothing
// else; it never touches the store or the Podium API.
type InboundEvent struct {
	// SourceKind is the source's Kind(): "slack", "dev", later "linear" and "chat".
	SourceKind string
	// SourceKey is the session's identity, stable for the life of the conversation
	// (for Slack, slack:<channel>:<thread_ts>). The conductor treats it as opaque.
	SourceKey string
	// Ref is whatever the source needs to post back, edit, attach to and react on. It is
	// opaque to the conductor, persisted as turns.trigger_ref, and copied into
	// brief.source.ref. For Slack it is <channel>/<thread_ts>/<trigger_ts>, because a
	// reaction goes on the triggering message and a post goes into the thread.
	Ref string
	// Channel is the routing key a playbook's slack_channels list is matched against. Empty
	// when the source has no notion of a channel. For Slack it is the channel id (C…).
	Channel string
	// ChannelName is the human name of Channel, without a leading #. Empty when the source
	// has no name, or when Slack has not resolved one yet. Display and briefing only —
	// routing still uses Channel.
	ChannelName string
	// ThreadURL is a link to the whole conversation where it lives, when the source can make
	// one and URL is a link to this message instead: a Slack thread's permalink. The mirror
	// records it so the chat's surface chip opens the thread.
	ThreadURL string
	// Title is the name the source gives the whole conversation — a pull request's number
	// and title — when it has a better one than the first thing said in it. The
	// mirror names a new chat with it, and renames one it named itself when it changes (a
	// pull request retitled). A shorter name the chat's title already starts with does not
	// replace it, so a source that knows only part of the name cannot undo a fuller one.
	// Empty leaves the chat named from its first message.
	Title string
	// ChannelPrivacy is whether Channel is public, as the source knows it: store.ChannelPublic,
	// ChannelPrivate, ChannelIM or ChannelMPIM. Empty is not known. The mirror records it so
	// sharing a chat of a private conversation is warned about with the source's own word.
	ChannelPrivacy string
	// Author is the display name of the human who spoke.
	Author string
	// Login is the Podium login of the speaker. Who may set it differs, and the conductor
	// is the last word on all of them (resolveSpeaker):
	//
	//   - the web chat sets the signed-in caller, which podium-server already proved;
	//   - Slack sets a CANDIDATE, the workspace email of a user who passed the source's
	//     account checks, which the conductor keeps only if that login has signed in to
	//     Podium;
	//   - GitHub sets nothing: the conductor looks the speaker's numeric id up in the
	//     identities a GitHub connect wrote.
	//
	// After resolveSpeaker it is either a person who is in Podium or empty. A turn runs
	// with this person's credentials, and it is what makes them a participant of the chat.
	Login string
	// SpeakerProvider and SpeakerID name the account that spoke, as its source proved it:
	// store.ProviderSlack and "<team>/<user>", or store.ProviderGitHub and the numeric user
	// id. Empty for a source that has no accounts (Linear, the dev source) and for the web
	// chat, whose Login is already the proof.
	SpeakerProvider string
	SpeakerID       string
	// SpeakerAvatar is the account's picture, when the source had it for free.
	SpeakerAvatar string
	// Mentioned is true when the speaker addressed the bot explicitly — an @mention, a DM,
	// a review command — rather than a reply the source decided to treat as one. Only a
	// mentioned message is answered with ReactionRefused: reacting to a reply nobody
	// addressed to the bot would be noise.
	Mentioned bool
	// ReviewRequest is set on a GitHub review command (`@podium review [note]`, `/review
	// [note]`): the instruction to run INSTEAD of Text when the speaker is not in Podium. It
	// is built from nothing the speaker controls but a sanitized, injection-checked note, so
	// it is safe to hand the review playbook on the App's credentials. Empty for anything
	// else, and ignored for a speaker who is in Podium, who gets the full comment.
	ReviewRequest string
	// ExternalID is the message's identity on its own surface — a Slack ts, or a GitHub
	// comment, review or delivery id with its kind in front — and ExternalURL is the link to
	// it. The mirror records one row per ExternalID however many times the event arrives, and
	// only the arrival that wrote the row goes on to start a turn. Empty means the source
	// cannot name the message, and then nothing dedupes it here.
	ExternalID  string
	ExternalURL string
	// SpeakerHandle is the account's handle where the surface has one distinct from its
	// display name: a GitHub login. Empty for Slack.
	SpeakerHandle string
	// RecordOnly is a message nobody put to Podium — a reply in a thread it is already in,
	// a pull-request comment that does not mention it. It is recorded in the conversation's
	// chat and NEVER starts a turn. It is recorded only into a chat that already exists:
	// Podium keeps the whole of a conversation it is part of, and nothing of one it is not.
	RecordOnly bool
	// Change, when set, makes this event an edit (ChangeEdited) or a delete (ChangeDeleted)
	// of the message named by ExternalID, made on its own surface. Text is the new text of
	// an edit. A change updates the record and never starts a turn — not even an edit that
	// adds a mention (decision 34).
	Change string
	// MirrorKey, when set, is the conversation a RecordOnly event or a Change belongs to when
	// that is not the one its Ref names: a reply in a Slack thread bound to a pull-request
	// review is part of the PR's conversation, not of a Slack thread of its own. The conductor
	// honours it for those two kinds only, so it can move a record and never a turn.
	MirrorKey string
	// ChatID and ChatSeq are the message in a chat's record that this event is, once it is
	// recorded: the web chat sets them as it stores the message, the mirror as it records
	// one. The turn queue keeps them so the person who asked can take their turn back, and
	// so the chat can show whose turns are waiting.
	ChatID  string `json:",omitempty"`
	ChatSeq uint64 `json:",omitempty"`
	// Held is a message that is not in its chat's record yet: the web chat leaves a message
	// sent while Podium is answering out of the transcript, so it is not read in the middle
	// of the answer it waited for, and the conductor records it (HeldRecorder) as its turn
	// starts — or as it goes into a turn that is up. ChatSeq is set then.
	Held bool `json:",omitempty"`
	// Routine is set on a run the routine scheduler started (routine.go). Its Text is the
	// routine's instruction, which is not in the chat's record and never will be.
	Routine *RoutineRef `json:",omitempty"`
	// Placed, when set, is told where the message went once the conductor has decided: the
	// number of turns that run before it (0: it is being answered now — it started a turn,
	// or went into the turn already answering this person). The web chat sets it so a send
	// can say how far back in the queue it is, exactly rather than by guessing. It is not
	// called for a message nothing will answer, and it is never part of a queued event.
	Placed func(ahead int) `json:"-"`
	// RecordText is what the person actually wrote, when Text is an instruction the source
	// built around it ("alice commented on #12 …"). The record keeps the person's words;
	// the turn is handed the instruction. Empty means Text is both.
	RecordText string
	// Text is what they said, with the bot mention already stripped.
	Text string
	// TS is when they said it.
	TS time.Time
	// URL is a human link to the conversation, copied into brief.source.url. Empty when the
	// source has none.
	URL string
	// Playbook is a playbook the source KNOWS is right — Linear's linear: true playbook. It
	// bypasses every routing rule, a typed /playbook included. Empty means "let the profile's
	// rules decide", which is what Slack always leaves it as.
	//
	// The web chat leaves it empty too, and cannot do otherwise: a conversation is answered
	// by the assistant and runs no playbook at all.
	Playbook string
	// Role is the speaker's role on what the conversation is about, when the source knows it:
	// their access to a pull request's repository — read, triage, write, maintain or admin.
	// A pull request offers a playbook only to a speaker whose role its github_requires
	// allows. Empty is no role, and offers none of them.
	Role string
	// Automatic is an event nobody said: the source saw something happen — a pull request
	// opened, or marked ready — and wants its own playbook run on it. The conductor delegates
	// that playbook directly, with no assistant turn, because there is nothing to decide.
	Automatic bool
	// Override is a per-turn choice of backend, model and effort, from a source whose human
	// can make one — the web chat's picker, which moves what ANSWERS the conversation. Empty
	// everywhere else: Slack and Linear have no surface to choose on, so their turns run on
	// what the playbook says.
	//
	// It is the reason a playbook's model is a default rather than a fixture. Without it the
	// only way to ask one playbook on another model is a second playbook differing by one field.
	Override profiles.Override
	// BriefKind is the source.kind the runtime's schema must see, which is not always
	// SourceKind: the schema allows only slack, linear, chat and github, and the test-only dev
	// source presents itself as chat.
	BriefKind string
	// Env is extra task-spec environment the source asks for. It exists for the dev
	// source's dry-run knobs and is empty for every real source; the conductor refuses to
	// honour it for any source but "dev".
	Env map[string]string
	// PersonalityName is the personal assistant this chat is bound to. Empty means
	// Podium. It labels a new session. The brief's profile name stays Podium's, and
	// Slack, GitHub, and every other source leave this empty.
	PersonalityName string
	// PersonalityPrompt is that assistant's instructions. The conductor appends them
	// to a copy of Podium's system prompt for this brief only. The stored prompt is
	// not changed.
	PersonalityPrompt string
}

// Attachment is one file the conductor wants put into the conversation. Size is part of
// the contract because Slack's modern upload flow (getUploadURLExternal) has to declare the
// length before the bytes move, so a source cannot discover it from the reader.
type Attachment struct {
	Name        string
	ContentType string
	Size        int64
	Body        io.Reader
	// TaskID is the task the artifact belongs to, so a source that cannot upload the file
	// can link to it where it actually lives.
	TaskID string
	// ArtifactID is the artifact this file is, in the control plane. A source whose reader
	// is on the same origin as the control plane — the web chat — records the id and lets
	// the browser fetch GET /artifacts/{id} itself rather than relaying the bytes. Slack
	// and Linear ignore it and upload Body.
	ArtifactID string
}

// ActivityPoster is a Source that draws what a turn is doing — its tool calls and its
// reasoning — beside what it says. The web chat keeps every one as a row of the chat. A
// Linear agent session shows each as an ephemeral activity that the next replaces, so it
// shows what the turn is doing now rather than a trail of it. A Slack thread or a GitHub pull
// request would be buried in them, since everything said there stays, so a source that is not
// one is told none of it.
type ActivityPoster interface {
	PostActivity(ctx context.Context, ref string, out Outbound) error
}

// Source is one place conversations happen. Steps 20 and 21 implement this same interface
// for Linear and the web chat.
type Source interface {
	// Kind names the source: "slack", "dev", later "linear" and "chat".
	Kind() string
	// Events yields inbound messages. It is closed when the source stops.
	Events() <-chan InboundEvent
	// FetchTranscript returns the conversation so far, oldest first, including the
	// triggering message. The bot's own placeholder and progress messages are excluded;
	// its finals are included.
	FetchTranscript(ctx context.Context, ref string) ([]BriefEntry, error)
	// Post says something new and returns an id Edit can address.
	Post(ctx context.Context, ref string, out Outbound) (string, error)
	// Edit replaces a message this source posted.
	Edit(ctx context.Context, ref, msgID string, out Outbound) error
	// Attach uploads one file into the conversation.
	Attach(ctx context.Context, ref string, file Attachment) error
	// React shows a turn's state on the message that started it.
	React(ctx context.Context, ref string, kind Reaction) error
}

// HeldRecorder is a source that can leave a message out of its record until it is answered
// (InboundEvent.Held): the web chat. RecordHeld writes it now, sets ev.ChatSeq and clears
// ev.Held. The conductor calls it as the message's turn starts, or as the message goes into a
// turn that is up.
type HeldRecorder interface {
	RecordHeld(ctx context.Context, ev *InboundEvent) error
}

// recordHeld writes a held message into its chat before anything answers it. A source that
// cannot is left alone, and a failure is logged: the turn still runs on the message, which
// is in its brief either way.
func (c *Conductor) recordHeld(ctx context.Context, src Source, ev *InboundEvent) {
	if !ev.Held {
		return
	}
	r, ok := src.(HeldRecorder)
	if !ok {
		return
	}
	if err := r.RecordHeld(ctx, ev); err != nil {
		c.logger.WarnContext(ctx, "recording a queued message as its turn starts failed",
			"chat_id", ev.ChatID, "error", err)
	}
}
