package linear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// dedupeTTL is how long a delivery id and a prompt's id are remembered. Linear retries a
// delivery it got no 200 for after a minute, an hour and six hours, so an id is kept past
// the last of those.
const dedupeTTL = 7 * time.Hour

// maxRemembered bounds what dedupeTTL keeps. Two ids per delivery, so this is thousands of
// deliveries within seven hours before the oldest is forgotten early.
const maxRemembered = 10000

// acknowledgement is the thought posted the moment a session opens, before the conductor has
// chosen a playbook. It is ephemeral, so the turn's first progress replaces it.
const acknowledgement = "Starting on this."

// AppOptions configures the agent app's source.
type AppOptions struct {
	// Client is signed in as the app, with a TokenSource. Required.
	Client *Client
	// WebhookSecret is the webhook's signing secret. Required. SENSITIVE.
	WebhookSecret string
	// Listen is the host:port the webhook arrives on. Required.
	Listen string
	// Serve serves the webhook handler on Listen until ctx ends: the conductor's webhook
	// server, which may share the address with the GitHub App. Required.
	Serve func(ctx context.Context, listen, path string, h http.Handler) error
	// AppUserID and OrganizationID are the app user and workspace the app signed in as when
	// it was saved. A delivery for any other is ignored. Required.
	AppUserID      string
	OrganizationID string
	// AppName is the app user's name. A mention of it at the start of a comment is not part
	// of what was asked. Optional.
	AppName string
	// Playbook names the playbook Linear runs — the one with linear: true — exactly as for
	// the poller. Required, and must return non-empty at NewApp.
	Playbook func() string
	// TaskURL renders a link to a Podium task page. It is the fallback when an attachment
	// cannot be uploaded.
	TaskURL func(taskID string) string
	// Revoked starts the source as Linear left it: an app that was revoked, which starts
	// nothing until it is saved again.
	Revoked bool
	// OnRevoked is told when Linear revokes the app, so the saved connection can say so. It
	// runs off the request.
	OnRevoked func(ctx context.Context)
	// Metrics is optional; nil registers nothing.
	Metrics *Metrics
	Logger  *slog.Logger
	// HTTPClient carries attachment uploads, and lets a test point them at a stub.
	HTTPClient *http.Client
	// Clock is time.Now unless a test replaces it.
	Clock func() time.Time
}

// AppSource is Linear as an agent app: webhooks in, agent activities out. It is the same
// Kind as the poller, and only one of the two ever runs.
//
// Each agent session is one conversation. A delegation or an @mention opens one and starts
// a turn of the linear: true playbook; what people say in it afterwards is a follow-up.
// Everything the turn says goes back into the session as an activity, and Linear writes the
// prompts, questions and answers into the issue's comments, which is where the transcript is
// read from.
type AppSource struct {
	client *Client
	// comments is the poller's comment machinery, signed in as the app. It reads the
	// transcript, uploads attachments, and answers a turn whose ref names no session — one
	// the poller started before the app took over — with comments, as the poller would.
	comments *Source

	logger         *slog.Logger
	metrics        *Metrics
	events         chan conductor.InboundEvent
	secret         string
	listen         string
	serve          func(ctx context.Context, listen, path string, h http.Handler) error
	appUserID      string
	organizationID string
	appName        string
	playbook       func() string
	onRevoked      func(ctx context.Context)
	now            func() time.Time

	// work is the context deliveries are handled on. It outlives the request that brought
	// each one and ends when Run returns.
	work     context.Context
	stopWork context.CancelFunc
	wg       sync.WaitGroup

	mu      sync.Mutex
	stopped bool
	revoked bool
	// seen is every delivery and prompt id handled within dedupeTTL, with when.
	seen map[string]time.Time
	// queues is the work waiting in each session, in the order it arrived. A session with a
	// key here has a goroutine draining it; one with none has nothing waiting.
	queues map[string][]func(context.Context)
	// turns is the per-ref bookkeeping between the conductor's calls.
	turns map[string]*appTurn
}

// appTurn is what the source remembers about one turn between the conductor's calls. Like
// the poller's, it is in memory only.
type appTurn struct {
	// lastThought is when a progress thought was last posted, for the edit throttle.
	lastThought time.Time
	// closing is the activity type the turn ended on, response or error. An attachment that
	// arrives after it is posted as the same type, so Linear's view of how the session ended
	// does not change.
	closing string
	taskID  string
}

var _ conductor.Source = (*AppSource)(nil)

// NewApp builds the agent app's source. Nothing is served until Run.
func NewApp(opts AppOptions) (*AppSource, error) {
	switch {
	case opts.Playbook == nil || opts.Playbook() == "":
		return nil, ErrNoPlaybook("or remove the Linear app under Settings → Connections")
	case opts.Client == nil:
		return nil, errors.New("linear: the app needs a client signed in as it")
	case opts.WebhookSecret == "":
		return nil, errors.New("linear: the app needs its webhook signing secret")
	case opts.Listen == "" || opts.Serve == nil:
		return nil, errors.New("linear: the app needs a webhook listen address to be served on")
	case opts.AppUserID == "" || opts.OrganizationID == "":
		return nil, errors.New("linear: the saved app does not say which app user and workspace it " +
			"signed in as. Save it again under Settings → Connections")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = NewMetrics(nil)
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	taskURL := opts.TaskURL
	if taskURL == nil {
		taskURL = func(string) string { return "" }
	}
	upload := opts.HTTPClient
	if upload == nil {
		upload = &http.Client{Timeout: DefaultTimeout}
	}
	onRevoked := opts.OnRevoked
	if onRevoked == nil {
		onRevoked = func(context.Context) {}
	}
	work, stop := context.WithCancel(context.Background())
	return &AppSource{
		client: opts.Client,
		comments: &Source{
			client:   opts.Client,
			logger:   logger,
			metrics:  metrics,
			playbook: opts.Playbook,
			taskURL:  taskURL,
			now:      now,
			upload:   upload,
			// The app's own comments are its answers, not anybody's question.
			me:       User{ID: opts.AppUserID, Name: opts.AppName},
			states:   map[string]string{},
			assigned: map[string]bool{},
			turns:    map[string]*turnState{},
		},
		logger:         logger,
		metrics:        metrics,
		events:         make(chan conductor.InboundEvent, 32),
		secret:         opts.WebhookSecret,
		listen:         opts.Listen,
		serve:          opts.Serve,
		appUserID:      opts.AppUserID,
		organizationID: opts.OrganizationID,
		appName:        opts.AppName,
		playbook:       opts.Playbook,
		onRevoked:      onRevoked,
		now:            now,
		work:           work,
		stopWork:       stop,
		revoked:        opts.Revoked,
		seen:           map[string]time.Time{},
		queues:         map[string][]func(context.Context){},
		turns:          map[string]*appTurn{},
	}, nil
}

// Kind implements conductor.Source.
func (s *AppSource) Kind() string { return Kind }

// Events implements conductor.Source. The channel closes when Run returns.
func (s *AppSource) Events() <-chan conductor.InboundEvent { return s.events }

// Run serves the webhook until ctx is cancelled. It runs on every conductor, with no leader:
// a delivery reaches whichever one the tunnel sends it to, and only that one hears it.
//
// Nothing is asked of Linear here. The credentials were tried when they were saved, and a
// webhook that answers while Linear's token endpoint is having a bad minute loses nothing.
func (s *AppSource) Run(ctx context.Context) error {
	defer close(s.events)
	defer s.stop()
	s.logger.InfoContext(ctx, "linear app source listening", "listen", s.listen, "path", WebhookPath,
		"app_user_id", s.appUserID, "organization_id", s.organizationID, "playbook", s.playbook(),
		"revoked", s.Revoked())
	if err := s.serve(ctx, s.listen, WebhookPath, s.Handler()); err != nil {
		return fmt.Errorf("linear: serve webhooks: %w", err)
	}
	return nil
}

// stop ends the work in flight and waits for it, so nothing is emitted on a closed channel.
func (s *AppSource) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.stopWork()
	s.wg.Wait()
}

// Revoked reports whether Linear has revoked the app since this source started, or had
// before it did.
func (s *AppSource) Revoked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked
}

// revoke stops the source starting anything, and has the saved connection say why.
func (s *AppSource) revoke() {
	s.mu.Lock()
	s.revoked = true
	s.mu.Unlock()
	s.enqueue("", func(work context.Context) { s.onRevoked(work) })
}

// enqueue queues fn behind whatever the session is already doing, so two prompts in one
// session reach the conductor in the order Linear sent them, while different sessions do
// not wait for each other.
func (s *AppSource) enqueue(sessionID string, fn func(context.Context)) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	q, draining := s.queues[sessionID]
	s.queues[sessionID] = append(q, fn)
	if draining {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go s.drain(sessionID)
}

func (s *AppSource) drain(sessionID string) {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		q := s.queues[sessionID]
		if len(q) == 0 {
			delete(s.queues, sessionID)
			s.mu.Unlock()
			return
		}
		fn := q[0]
		s.queues[sessionID] = q[1:]
		s.mu.Unlock()
		fn(s.work)
	}
}

// firstSighting reports whether key is new within dedupeTTL, and remembers it.
func (s *AppSource) firstSighting(key string) bool {
	now := s.now()
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
	if len(s.seen) >= maxRemembered {
		oldest, at := "", now
		for k, t := range s.seen {
			if t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(s.seen, oldest)
	}
	s.seen[key] = now
	return true
}

// acknowledge posts the thought that tells Linear the app heard. A failure is logged and
// the turn starts anyway: its own first progress is the next chance to say something.
func (s *AppSource) acknowledge(ctx context.Context, sessionID string) {
	_, err := s.client.CreateAgentActivity(ctx, sessionID,
		ActivityContent{Type: ActivityThought, Body: acknowledgement}, true)
	if err != nil {
		s.logger.WarnContext(ctx, "linear: acknowledging a new session failed", "session", sessionID, "error", err)
	}
}

func (s *AppSource) emit(ctx context.Context, ev conductor.InboundEvent) {
	select {
	case s.events <- ev:
	case <-ctx.Done():
	}
}

// inbound is the event for one prompt. Login and the speaker's account are not set: Linear
// has no accounts in Podium yet, and its turns run unauthenticated, as the poller's do.
func (s *AppSource) inbound(p *webhookPayload) conductor.InboundEvent {
	sess, act, iss := p.AgentSession, p.AgentActivity, p.AgentSession.Issue
	teamID := iss.TeamID
	if teamID == "" && iss.Team != nil {
		teamID = iss.Team.ID
	}
	description := iss.Description
	issue := Issue{ID: iss.ID, Identifier: iss.Identifier, Title: iss.Title, Description: &description}

	said := strings.TrimSpace(act.Content.Body)
	text, kind := said, EventFollowup
	if p.Action == ActionCreated {
		if sess.delegated() {
			// A delegation is the ticket itself, worded exactly as the poller words an
			// assignment, so a linear: true playbook reads the same first instruction
			// whichever of the two started it. Linear's own "Work on EME-5" is the record.
			text, kind = issue.brief(), EventDelegation
		} else {
			text, kind = s.stripMention(said), EventMention
			if text == "" {
				// A bare @mention asks for nothing more than the ticket does.
				text = issue.brief()
			}
		}
	}
	s.metrics.Events.WithLabelValues(kind).Inc()

	ts := act.CreatedAt
	if ts.IsZero() {
		ts = p.CreatedAt
	}
	author := "linear"
	if act.User != nil {
		author = firstNonEmpty(act.User.Name, act.User.Email, author)
	}
	title := strings.TrimSpace(iss.Identifier + " " + iss.Title)
	ev := conductor.InboundEvent{
		SourceKind:  Kind,
		SourceKey:   SessionKey(sess.ID),
		Ref:         SessionRef(iss.ID, teamID, iss.Identifier, sess.ID),
		Author:      author,
		ExternalID:  "activity:" + act.ID,
		ExternalURL: sess.URL,
		URL:         iss.URL,
		Title:       title,
		TS:          ts,
		Mentioned:   true,
		Text:        text,
		BriefKind:   conductor.SourceLinear,
		// The playbook is named here rather than routed, as the poller names it.
		Playbook: s.playbook(),
	}
	if text != said {
		ev.RecordText = said
	}
	return ev
}

// stripMention takes an @mention of the app off the front of a comment. Linear puts the
// handle in the body as typed; a mention of anybody else is part of what was asked.
func (s *AppSource) stripMention(text string) string {
	if s.appName == "" || !strings.HasPrefix(text, "@") {
		return text
	}
	handle, rest, _ := strings.Cut(text[1:], " ")
	name := strings.ReplaceAll(s.appName, " ", "")
	if !strings.EqualFold(handle, name) {
		return text
	}
	return strings.TrimSpace(rest)
}

// ---------------------------------------------------------------------------
// conductor.Source: what a turn says goes into the session
// ---------------------------------------------------------------------------

// FetchTranscript reads the issue and its comments, as the poller does. Linear writes every
// prompt, question and answer of a session into the comments, so a session's conversation is
// there with every earlier session's on the same issue.
func (s *AppSource) FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error) {
	return s.comments.FetchTranscript(ctx, ref)
}

// Post says something new in the session, as an activity, and returns its id:
//
//   - progress is an ephemeral thought, which the next activity replaces;
//   - an answer is a response, which completes the session;
//   - a failure is an error;
//   - a question is an elicitation, which waits for the person.
//
// A ref with no session is a turn the poller started, and is answered with a comment.
func (s *AppSource) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	issueID, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return "", err
	}
	if sessionID == "" {
		return s.comments.Post(ctx, ref, out)
	}
	content, ephemeral := activityFor(out)
	if strings.TrimSpace(content.Body) == "" {
		return "", nil
	}
	id, err := s.client.CreateAgentActivity(ctx, sessionID, content, ephemeral)
	if err != nil {
		return "", fmt.Errorf("linear: post a %s in session %s on %s: %w", content.Type, sessionID, issueID, err)
	}
	st := s.turn(ref)
	s.mu.Lock()
	switch content.Type {
	case ActivityThought:
		st.lastThought = s.now()
	case ActivityResponse, ActivityError:
		st.closing = content.Type
	}
	if out.TaskID != "" {
		st.taskID = out.TaskID
	}
	s.mu.Unlock()
	return id, nil
}

// Edit replaces a progress post with a newer one. An activity cannot be edited, but an
// ephemeral thought is replaced by the next activity, which is the same thing on screen. An
// edit inside EditThrottle of the last thought is dropped: the next one, or the answer,
// supersedes it, as an edit of the poller's working comment is.
func (s *AppSource) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return s.comments.Edit(ctx, ref, msgID, out)
	}
	st := s.turn(ref)
	s.mu.Lock()
	if s.now().Sub(st.lastThought) < EditThrottle {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	out.Type = conductor.OutProgress
	_, err = s.Post(ctx, ref, out)
	return err
}

// React has nothing to send into a session: Linear derives the session's state from its last
// activity, so the answer or the error already said how the turn went. A ref with no session
// reacts as the poller does.
func (s *AppSource) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return s.comments.React(ctx, ref, kind)
	}
	switch kind {
	case conductor.ReactionWorking, conductor.ReactionDone, conductor.ReactionFailed,
		conductor.ReactionAwaiting, conductor.ReactionRefused:
		return nil
	default:
		return fmt.Errorf("linear: %q is not a reaction", kind)
	}
}

// Attach uploads a file to Linear and posts the link into the session, the way the poller
// appends one to its answer. An activity cannot be edited, so each file is an activity of its
// own, of the type the turn ended on: a response after a response leaves the session complete.
// A file that cannot be uploaded is a link to the Podium task instead, as with the poller.
func (s *AppSource) Attach(ctx context.Context, ref string, file conductor.Attachment) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return s.comments.Attach(ctx, ref, file)
	}
	switch {
	case file.Name == "":
		return errors.New("linear: an attachment needs a file name")
	case file.Body == nil:
		return fmt.Errorf("linear: attachment %q has no body", file.Name)
	}
	st := s.turn(ref)
	s.mu.Lock()
	taskID, closing := firstNonEmpty(file.TaskID, st.taskID), st.closing
	s.mu.Unlock()

	var line string
	if file.Size > MaxUploadBytes {
		s.logger.InfoContext(ctx, "an attachment is larger than linear takes; linking to the task instead",
			"name", file.Name, "size_bytes", file.Size)
		line = s.comments.fallbackLine(file.Name, taskID)
	} else if line, err = s.comments.uploadFile(ctx, file); err != nil {
		s.logger.WarnContext(ctx, "uploading an attachment to linear failed; linking to the task instead",
			"name", file.Name, "error", err)
		line = s.comments.fallbackLine(file.Name, taskID)
	}
	kind := conductor.OutFinal
	if closing == ActivityError {
		kind = conductor.OutFailure
	}
	_, err = s.Post(ctx, ref, conductor.Outbound{Type: kind, Text: line})
	return err
}

// turn returns the bookkeeping for a ref, creating it on first use.
func (s *AppSource) turn(ref string) *appTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.turns[ref]
	if st == nil {
		st = &appTurn{}
		s.turns[ref] = st
	}
	return st
}

// activityFor is the activity an Outbound becomes, and whether it is ephemeral. A final and
// a failure carry the task footer, as the poller's comments do.
func activityFor(out conductor.Outbound) (ActivityContent, bool) {
	switch out.Type {
	case conductor.OutFinal:
		return ActivityContent{Type: ActivityResponse, Body: withFooter(out.Text, out.TaskID)}, false
	case conductor.OutFailure:
		return ActivityContent{Type: ActivityError, Body: withFooter(out.Text, out.TaskID)}, false
	case conductor.OutQuestion:
		return ActivityContent{Type: ActivityElicitation, Body: out.Text}, false
	default:
		return ActivityContent{Type: ActivityThought, Body: thoughtText(out.Text)}, true
	}
}

// thoughtText is a progress line as a thought. Linear draws a thought as a status already,
// so the conductor's emoji markers are dropped, and its placeholder says so in words.
func thoughtText(text string) string {
	if text == conductor.Placeholder {
		return "Working on it."
	}
	return strings.TrimSpace(strings.TrimPrefix(text, conductor.ProgressPrefix))
}

// ErrNoPlaybook is the refusal of a Linear source with no linear: true playbook to run, in
// the poller's words, with what to do afterwards.
func ErrNoPlaybook(then string) error { return errors.New(noPlaybook + then) }
