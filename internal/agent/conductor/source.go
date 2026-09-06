package conductor

import (
	"context"
	"io"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
)

// Reaction is the three states a source can show on the message that started a turn.
type Reaction string

// The reactions. Slack maps them onto eyes / white_check_mark / x.
const (
	ReactionWorking Reaction = "working"
	ReactionDone    Reaction = "done"
	ReactionFailed  Reaction = "failed"
)

// Outbound types. Type is what the source may use to style a post; nothing branches on it
// in the conductor except to decide whether a post replaces the placeholder.
const (
	OutProgress = "progress"
	OutFinal    = "final"
	OutFailure  = "failure"
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
	// Channel is the routing key a skill's slack_channels list is matched against. Empty
	// when the source has no notion of a channel.
	Channel string
	// Author is the display name of the human who spoke.
	Author string
	// Text is what they said, with the bot mention already stripped.
	Text string
	// TS is when they said it.
	TS time.Time
	// URL is a human link to the conversation, copied into brief.source.url. Empty when the
	// source has none.
	URL string
	// Skill is a skill the source KNOWS is right (Linear's linear: true skill, the chat's
	// skill chip). It bypasses every routing rule, a typed /skill included. Empty means
	// "let the profile's rules decide". Slack always leaves it empty.
	Skill string
	// DefaultSkill is what this source falls back to when nothing more specific picks one
	// (the chat's chat_default_skill). Unlike Skill it is only a preference: a human typing
	// /skill overrides it, and it beats profile.default_skill.
	DefaultSkill string
	// Override is a per-turn choice of backend, model and effort, from a source whose human
	// can make one — the web chat's picker. Empty everywhere else: Slack and Linear have no
	// surface to choose on, so their turns run on what the skill says.
	//
	// It is the reason a skill's model is a default rather than a fixture. Without it the
	// only way to ask one skill on another model is a second skill differing by one field.
	Override profiles.Override
	// BriefKind is the source.kind the runtime's schema must see, which is not always
	// SourceKind: the schema allows only slack, linear and chat, and the test-only dev
	// source presents itself as chat.
	BriefKind string
	// Env is extra task-spec environment the source asks for. It exists for the dev
	// source's dry-run knobs and is empty for every real source; the conductor refuses to
	// honour it for any source but "dev".
	Env map[string]string
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
