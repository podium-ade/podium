// Package slack holds the bot's Slack identity: a Socket Mode connection, mentions turned
// into inbound events, and the four ways the conductor talks back. It implements
// conductor.Source and knows nothing about sessions, tasks or briefs.
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"golang.org/x/time/rate"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// Kind is the source kind Slack sessions are recorded under.
const Kind = "slack"

// MaxMessageChars is Slack's own per-message limit. Longer text is split at the last
// newline before the limit and posted as consecutive messages.
const MaxMessageChars = 4000

// repliesPageLimit is how many thread replies one conversations.replies call asks for.
// Slack caps it at 200 and recommends less.
const repliesPageLimit = 200

// dedupeTTL is how long a (channel, ts) pair is remembered. Slack delivers a mention in a
// channel as both an app_mention and a message event, so one message must not start two
// turns; a few minutes is far longer than the gap between the two deliveries.
const dedupeTTL = 5 * time.Minute

// writeRate is the ceiling on calls that CHANGE the conversation: posting, editing,
// reacting and uploading. chat.postMessage is Slack's special-tier method at roughly one
// per second per channel, and it is the one this bot leans on hardest, so the write path
// stays at one per second.
const writeRate = 1

// readRate is the ceiling on calls that only READ: conversations.replies and users.info.
// Slack's tiers put both far above the write path — an internal app reads a thread at 50+
// requests a minute — and one limiter across everything made a turn's transcript fetch and
// its author lookups queue behind the placeholder for a second each. Well under the tier,
// because the point is to stop reads waiting on writes, not to run at the ceiling.
const readRate = 10

// The reaction names the three turn states show as. Since Slack posts no placeholder, the
// running one is the ONLY thing that says work has started, so it says it in the shape a
// reader already knows from the progress prefix: ⏳ while it runs, ✅ when it worked, ❌
// when it did not.
const (
	emojiWorking = "hourglass_flowing_sand"
	emojiDone    = "white_check_mark"
	emojiFailed  = "x"
)

// Options configures the source.
type Options struct {
	// AppToken is the xapp-… app-level token with connections:write. SENSITIVE.
	AppToken string
	// BotToken is the xoxb-… bot token. SENSITIVE.
	BotToken string
	Logger   *slog.Logger

	// apiURL points the Web API at somewhere other than Slack. It is unexported because
	// the only caller that has any business setting it is this package's own test: every
	// method below that talks to Slack was, until it existed, covered by nothing at all.
	// Must end in a slash — the library concatenates the method name onto it.
	apiURL string
}

// Source is the Slack integration.
type Source struct {
	api    *slack.Client
	sm     *socketmode.Client
	logger *slog.Logger
	events chan conductor.InboundEvent
	// writes and reads are separate because Slack's limits are per method and the two
	// paths are an order of magnitude apart. See writeRate and readRate.
	writes *rate.Limiter
	reads  *rate.Limiter

	// botUserID and botID identify the bot's own messages, so they are never relayed back
	// to it as input and are marked as the assistant in a transcript.
	botUserID string
	botID     string
	mentionRE *regexp.Regexp
	// archiveBase is the workspace's https://team.slack.com, used to build permalinks
	// without an extra API call per turn.
	archiveBase string

	mu    sync.Mutex
	users map[string]string
	seen  map[string]time.Time
}

var _ conductor.Source = (*Source)(nil)

// New builds the source. Nothing is dialled until Run.
func New(opts Options) (*Source, error) {
	if opts.AppToken == "" || opts.BotToken == "" {
		return nil, errors.New("slack: both the app-level token and the bot token are required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// OptionAppLevelToken lives in the main slack package, not socketmode.
	options := []slack.Option{slack.OptionAppLevelToken(opts.AppToken)}
	if opts.apiURL != "" {
		options = append(options, slack.OptionAPIURL(opts.apiURL))
	}
	api := slack.New(opts.BotToken, options...)
	return &Source{
		api:    api,
		sm:     socketmode.New(api),
		logger: logger,
		events: make(chan conductor.InboundEvent, 32),
		writes: rate.NewLimiter(writeRate, 1),
		reads:  rate.NewLimiter(readRate, 1),
		users:  map[string]string{},
		seen:   map[string]time.Time{},
	}, nil
}

// Kind implements conductor.Source.
func (s *Source) Kind() string { return Kind }

// Events implements conductor.Source. The channel closes when Run returns.
func (s *Source) Events() <-chan conductor.InboundEvent { return s.events }

// Run learns the bot's own identity and then holds the Socket Mode connection until ctx is
// cancelled. Reconnects are the library's; this only logs and counts them.
func (s *Source) Run(ctx context.Context) error {
	defer close(s.events)

	auth, err := s.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack: auth.test failed (is the bot token right?): %w", err)
	}
	s.botUserID = auth.UserID
	s.botID = auth.BotID
	s.mentionRE = regexp.MustCompile(`<@` + regexp.QuoteMeta(auth.UserID) + `(\|[^>]*)?>`)
	s.archiveBase = strings.TrimSuffix(auth.URL, "/")
	s.logger.InfoContext(ctx, "slack source connected",
		"team", auth.Team, "bot_user_id", auth.UserID, "bot_id", auth.BotID)

	done := make(chan error, 1)
	go func() { done <- s.sm.RunContext(ctx) }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				return fmt.Errorf("slack: socket mode ended: %w", err)
			}
			return nil
		case evt := <-s.sm.Events:
			s.handle(ctx, evt)
		}
	}
}

// handle dispatches one Socket Mode event. Every Events API request is acked before any
// work starts: the library requires an ack within three seconds, and an ack never carries
// content — Slack rejects a response of 20 KiB or more and the error surfaces here.
func (s *Source) handle(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		if evt.Request != nil {
			if err := s.sm.Ack(*evt.Request); err != nil {
				s.logger.WarnContext(ctx, "acking a slack event failed", "error", err)
			}
		}
		api, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		s.inner(ctx, api)
	case socketmode.EventTypeConnected:
		s.logger.InfoContext(ctx, "slack socket mode connected")
	case socketmode.EventTypeDisconnect:
		s.logger.WarnContext(ctx, "slack socket mode disconnected; the library will reconnect")
	case socketmode.EventTypeInvalidAuth:
		s.logger.ErrorContext(ctx, "slack rejected the app-level token; no events will arrive")
	case socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError:
		s.logger.WarnContext(ctx, "slack socket mode error", "event", string(evt.Type), "data", fmt.Sprint(evt.Data))
	default:
	}
}

// inner turns an app_mention or a message into an inbound event. The concrete inner types
// are POINTERS: slackevents builds them with reflect.New, so a value-typed case compiles
// and never matches.
func (s *Source) inner(ctx context.Context, api slackevents.EventsAPIEvent) {
	switch ev := api.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if ev.BotID != "" || ev.User == s.botUserID {
			return
		}
		s.emit(ctx, ev.Channel, ev.TimeStamp, ev.ThreadTimeStamp, ev.User, ev.Text)
	case *slackevents.MessageEvent:
		// Anything with a subtype is an edit, a deletion, a join or a file share notice,
		// not somebody talking. Anything from a bot — this one included — is never input.
		if ev.SubType != "" || ev.BotID != "" || ev.User == "" || ev.User == s.botUserID {
			return
		}
		// A DM is always input. A channel message that mentions the bot arrives as
		// app_mention above; a reply in a thread is not enough on its own.
		if ev.ChannelType != "im" {
			return
		}
		s.emit(ctx, ev.Channel, ev.TimeStamp, ev.ThreadTimeStamp, ev.User, ev.Text)
	default:
	}
}

// emit normalises one message and offers it to the conductor. A mention in a channel
// arrives twice — as app_mention and as message — so (channel, ts) is deduplicated here.
func (s *Source) emit(ctx context.Context, channel, ts, threadTS, user, text string) {
	if channel == "" || ts == "" {
		return
	}
	if !s.firstSighting(channel, ts) {
		return
	}
	thread := threadOf(threadTS, ts)
	ev := conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  sourceKey(channel, thread),
		Ref:        Ref(channel, thread, ts),
		Channel:    channel,
		Author:     s.displayName(ctx, user),
		Text:       s.stripMention(text),
		TS:         parseTS(ts),
		URL:        s.permalink(channel, ts),
		BriefKind:  conductor.SourceSlack,
	}
	select {
	case s.events <- ev:
	case <-ctx.Done():
	}
}

// firstSighting reports whether (channel, ts) has not been seen, and records it. The map is
// pruned on the way in, so it cannot grow without bound.
func (s *Source) firstSighting(channel, ts string) bool {
	key := channel + "/" + ts
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, at := range s.seen {
		if now.Sub(at) > dedupeTTL {
			delete(s.seen, k)
		}
	}
	if _, ok := s.seen[key]; ok {
		return false
	}
	s.seen[key] = now
	return true
}

// stripMention removes every <@BOTID> from the text, which is what a human meant by
// "@Podium do this".
func (s *Source) stripMention(text string) string {
	if s.mentionRE == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(s.mentionRE.ReplaceAllString(text, ""))
}

// FetchTranscript reads the thread, oldest first. The bot's own placeholder and progress
// messages are left out — they are noise, not conversation — and its finals are kept.
func (s *Source) FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error) {
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	var out []conductor.BriefEntry
	cursor := ""
	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channel,
			// The thread's PARENT ts. A mention that starts a thread has an empty
			// thread_ts, so the parent is its own ts, which is what Ref carries.
			Timestamp: thread,
			Cursor:    cursor,
			Limit:     repliesPageLimit,
			Inclusive: true,
		}
		msgs, hasMore, next, err := s.readPage(ctx, func() ([]slack.Message, bool, string, error) {
			return s.api.GetConversationRepliesContext(ctx, params)
		})
		if err != nil {
			return nil, fmt.Errorf("slack: read thread %s/%s: %w", channel, thread, err)
		}
		for i := range msgs {
			if entry, ok := s.entry(ctx, msgs[i]); ok {
				out = append(out, entry)
			}
		}
		if !hasMore || next == "" {
			return out, nil
		}
		cursor = next
	}
}

// entry turns one Slack message into a transcript entry, or reports that it is not part of
// the conversation.
func (s *Source) entry(ctx context.Context, msg slack.Message) (conductor.BriefEntry, bool) {
	text := s.stripMention(msg.Text)
	if text == "" || msg.SubType == "channel_join" || msg.SubType == "channel_leave" {
		return conductor.BriefEntry{}, false
	}
	mine := (s.botID != "" && msg.BotID == s.botID) || (s.botUserID != "" && msg.User == s.botUserID)
	if mine && isNoise(text) {
		return conductor.BriefEntry{}, false
	}
	role := conductor.RoleUser
	author := s.displayName(ctx, msg.User)
	if mine {
		role = conductor.RoleAssistant
		if msg.Username != "" {
			author = msg.Username
		}
	}
	return conductor.BriefEntry{
		Role:   role,
		Author: author,
		TS:     conductor.BriefTimestamp(parseTS(msg.Timestamp)),
		Text:   text,
	}, true
}

// isNoise reports whether one of the bot's own messages is scaffolding rather than
// something it said.
func isNoise(text string) bool {
	return text == conductor.Placeholder || strings.HasPrefix(text, "⏳")
}

// Post says something new. Text over Slack's per-message limit is split at the last newline
// that fits and posted as consecutive messages; the id returned is the first one's, because
// that is the message progress edits address.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	// The working acknowledgement is the ⏳ reaction, not a chat message. The conductor
	// still offers the placeholder (every source sees the same Post); Slack drops it so
	// the thread never shows "working…", and returns no id so later progress cannot edit
	// a message that was never posted.
	if out.Text == conductor.Placeholder {
		return "", nil
	}
	first := ""
	for _, part := range Split(out.Text, MaxMessageChars) {
		ts, err := s.write(ctx, func() (string, error) {
			_, ts, err := s.api.PostMessageContext(ctx, channel,
				slack.MsgOptionTS(thread), slack.MsgOptionText(part, false))
			return ts, err
		})
		if err != nil {
			return first, fmt.Errorf("slack: post to %s/%s: %w", channel, thread, err)
		}
		if first == "" {
			first = ts
		}
	}
	return first, nil
}

// Edit replaces a message this source posted. Only the first part of a split message is
// ever edited, which is what a progress line is.
func (s *Source) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
	channel, _, _, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if msgID == "" {
		return errors.New("slack: no message to edit")
	}
	text := out.Text
	if parts := Split(text, MaxMessageChars); len(parts) > 0 {
		text = parts[0]
	}
	_, err = s.write(ctx, func() (string, error) {
		_, ts, _, err := s.api.UpdateMessageContext(ctx, channel, msgID, slack.MsgOptionText(text, false))
		return ts, err
	})
	if err != nil {
		return fmt.Errorf("slack: edit %s in %s: %w", msgID, channel, err)
	}
	return nil
}

// Attach uploads one file into the thread.
//
// The modern upload flow declares the length before the bytes move, so FileSize and
// Filename are both required and a zero source uploads nothing at all — silently. Both are
// checked here rather than trusted to the library.
func (s *Source) Attach(ctx context.Context, ref string, file conductor.Attachment) error {
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return err
	}
	switch {
	case file.Name == "":
		return errors.New("slack: an attachment needs a file name")
	case file.Size <= 0:
		return fmt.Errorf("slack: attachment %q has no length; slack has to declare it up front", file.Name)
	case file.Body == nil:
		return fmt.Errorf("slack: attachment %q has no body", file.Name)
	}
	_, err = s.write(ctx, func() (string, error) {
		// Blocks is deliberately unset: the library drops it whenever InitialComment is
		// non-empty, and a comment is what a human reads.
		summary, err := s.api.UploadFileContext(ctx, slack.UploadFileParameters{
			Reader:          file.Body,
			Filename:        file.Name,
			FileSize:        int(file.Size),
			Title:           file.Name,
			Channel:         channel,
			ThreadTimestamp: thread,
		})
		if err != nil {
			return "", err
		}
		return summary.ID, nil
	})
	if err != nil {
		return fmt.Errorf("slack: upload %s to %s/%s: %w", file.Name, channel, thread, err)
	}
	return nil
}

// React shows a turn's state on the message that started it, so a thread shows one state
// rather than a history of them. "Already reacted" and "no reaction" are both fine
// outcomes and not errors.
//
// Only the running mark is ever removed, and only by the outcome that replaces it. A turn's
// reactions go on the message that TRIGGERED it — a message a human has just sent, which is
// a different message every turn — so nothing of this bot's can already be on it, and the state machine
// is only ever working → done | failed. Removing the two emoji it was not setting on every
// call meant three requests a turn that Slack answered "no_reaction" to, two of them ahead
// of the working mark, where somebody is waiting to see that they were heard.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	channel, _, trigger, err := ParseRef(ref)
	if err != nil {
		return err
	}
	item := slack.NewRefToMessage(channel, trigger)
	var add string
	switch kind {
	case conductor.ReactionWorking:
		add = emojiWorking
	case conductor.ReactionDone:
		add = emojiDone
	case conductor.ReactionFailed:
		add = emojiFailed
	case conductor.ReactionAwaiting:
		// The question is in the thread; keep the working emoji so the mention still
		// looks in-flight rather than done.
		return nil
	default:
		return fmt.Errorf("slack: %q is not a reaction", kind)
	}
	if kind != conductor.ReactionWorking {
		if _, err := s.write(ctx, func() (string, error) {
			return "", s.api.RemoveReactionContext(ctx, emojiWorking, item)
		}); err != nil && !isBenignReactionError(err) {
			s.logger.DebugContext(ctx, "removing the working reaction failed",
				"emoji", emojiWorking, "error", err)
		}
	}
	if _, err := s.write(ctx, func() (string, error) {
		return "", s.api.AddReactionContext(ctx, add, item)
	}); err != nil && !isBenignReactionError(err) {
		return fmt.Errorf("slack: react %s on %s: %w", add, trigger, err)
	}
	return nil
}

// isBenignReactionError reports whether Slack refused a reaction change because the world
// is already the way we asked for.
func isBenignReactionError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already_reacted") ||
		strings.Contains(msg, "no_reaction") ||
		strings.Contains(msg, "message_not_found")
}

// displayName resolves a user ID to something a human wrote, cached for the process's life.
// A failure is not worth failing a turn over: the ID is a usable name.
func (s *Source) displayName(ctx context.Context, user string) string {
	if user == "" {
		return "unknown"
	}
	s.mu.Lock()
	name, ok := s.users[user]
	s.mu.Unlock()
	if ok {
		return name
	}
	info, err := s.readUser(ctx, func() (*slack.User, error) { return s.api.GetUserInfoContext(ctx, user) })
	if err != nil {
		s.logger.DebugContext(ctx, "resolving a slack display name failed", "user", user, "error", err)
		return user
	}
	name = firstNonEmpty(info.Profile.DisplayName, info.RealName, info.Name, user)
	s.mu.Lock()
	s.users[user] = name
	s.mu.Unlock()
	return name
}

// permalink builds the archive URL of one message without spending an API call on it.
func (s *Source) permalink(channel, ts string) string {
	if s.archiveBase == "" {
		return ""
	}
	return fmt.Sprintf("%s/archives/%s/p%s", s.archiveBase, channel, strings.ReplaceAll(ts, ".", ""))
}

// ---------------------------------------------------------------------------
// refs, text and rate limiting
// ---------------------------------------------------------------------------

// Ref is what the conductor carries around for one turn: the channel, the thread it posts
// into, and the message it reacts on. Three parts rather than two because a reaction goes
// on the triggering message and a reply goes into the thread, and both have to survive a
// restart (the ref is persisted as turns.trigger_ref).
func Ref(channel, thread, trigger string) string {
	return channel + "/" + thread + "/" + trigger
}

// ParseRef splits a Ref.
func ParseRef(ref string) (channel, thread, trigger string, err error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("slack: %q is not a channel/thread/message ref", ref)
	}
	return parts[0], parts[1], parts[2], nil
}

// sourceKey is a session's identity: the thread, not the message.
func sourceKey(channel, thread string) string { return Kind + ":" + channel + ":" + thread }

// threadOf is the thread a message belongs to. A message that starts a thread has an empty
// thread_ts and is its own parent.
func threadOf(threadTS, ts string) string {
	if threadTS != "" {
		return threadTS
	}
	return ts
}

// parseTS turns a Slack "1725000000.000100" into a time. An unparseable one becomes now,
// which is only ever used for ordering and display.
func parseTS(ts string) time.Time {
	secs, frac, _ := strings.Cut(ts, ".")
	var sec, micro int64
	if _, err := fmt.Sscanf(secs, "%d", &sec); err != nil {
		return time.Now().UTC()
	}
	if frac != "" {
		_, _ = fmt.Sscanf(frac, "%d", &micro)
	}
	return time.Unix(sec, micro*1000).UTC()
}

// Split breaks text into pieces of at most limit characters, preferring the last newline
// that fits so a paragraph is not cut mid-sentence. It counts runes, not bytes: Slack's
// limit is characters and a multi-byte one must never be cut in half.
func Split(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var out []string
	for len(runes) > limit {
		cut := limit
		if i := lastIndexRune(runes[:limit], '\n'); i > 0 {
			cut = i
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

func lastIndexRune(runes []rune, want rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == want {
			return i
		}
	}
	return -1
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// call runs one Web API call through the limiter and honours a 429's Retry-After exactly
// once. A second 429 is a real failure rather than something to keep sleeping on.
func call[T any](ctx context.Context, limit *rate.Limiter, fn func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		if err := limit.Wait(ctx); err != nil {
			return zero, err
		}
		out, err := fn()
		if err == nil {
			return out, nil
		}
		var limited *slack.RateLimitedError
		if attempt > 0 || !errors.As(err, &limited) {
			return zero, err
		}
		select {
		case <-time.After(limited.RetryAfter):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
}

// write is call on the write limiter: everything that changes the conversation. The three
// helpers are named for the limiter they use and not for their arity, because which
// limiter a call belongs on is the only thing about them worth knowing at the call site.
func (s *Source) write(ctx context.Context, fn func() (string, error)) (string, error) {
	return call(ctx, s.writes, fn)
}

// readUser is call on the read limiter, for users.info.
func (s *Source) readUser(ctx context.Context, fn func() (*slack.User, error)) (*slack.User, error) {
	return call(ctx, s.reads, fn)
}

// readPage is call on the read limiter, for the one method that returns four values.
func (s *Source) readPage(
	ctx context.Context, fn func() ([]slack.Message, bool, string, error),
) ([]slack.Message, bool, string, error) {
	type page struct {
		msgs   []slack.Message
		more   bool
		cursor string
	}
	p, err := call(ctx, s.reads, func() (page, error) {
		msgs, more, cursor, err := fn()
		return page{msgs: msgs, more: more, cursor: cursor}, err
	})
	return p.msgs, p.more, p.cursor, err
}

// MirrorKey is the session key a ref belongs to, so the conductor can keep a readable copy
// of this thread in the Podium UI. It is the same key emit builds, out of the same two
// parts of the ref — the channel and the thread, never the triggering message, because the
// copy is of the conversation and not of one turn of it.
func (s *Source) MirrorKey(ref string) (string, bool) {
	channel, thread, _, err := ParseRef(ref)
	if err != nil {
		return "", false
	}
	return sourceKey(channel, thread), true
}
