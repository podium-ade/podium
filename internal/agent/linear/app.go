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

// dedupeTTL is how long a delivery id, and the id of a prompt that opened a session or
// stopped one, are remembered. Linear retries a delivery it got no 200 for after a minute, an
// hour and six hours, so an id is kept past the last of those.
//
// The conductor's record is what keeps a prompt from starting a turn twice — on this conductor
// or on another, since a retry may reach either — because every prompt is a row of the
// session's chat keyed by its activity id. What is remembered here is only what the record
// cannot cover: the source answers a new session itself, before the conductor sees it, and it
// handles a Stop itself, which never reaches the conductor as a prompt.
const dedupeTTL = 7 * time.Hour

// maxRemembered bounds what dedupeTTL keeps. At most two ids per delivery, so this is
// thousands of deliveries within seven hours before the oldest is forgotten early.
const maxRemembered = 10000

// acknowledgement is the thought posted the moment a session opens, before the conductor has
// chosen a playbook. It is ephemeral, so the turn's first progress replaces it.
const acknowledgement = "Starting on this."

// ActivityThrottle is the shortest gap between two ephemeral activities in one session: a
// progress line, a tool call, a piece of reasoning. Each replaces the last on screen, so the
// newest wins and one inside the window is held until it closes, superseded by anything newer
// meanwhile. A turn can make a dozen tool calls a second, and the app has 5,000 requests an
// hour for every session in the workspace; at one every ten seconds a busy session spends at
// most 360 of them.
const ActivityThrottle = 10 * time.Second

// KeepAliveAfter is how long a running turn may say nothing before the source says something
// for it. Linear marks a session stale after thirty minutes without an activity, and a turn
// running a long build or a long test suite can be silent for longer than that.
const KeepAliveAfter = 20 * time.Minute

// What the source says on its own behalf.
const (
	keepAliveText = "Still working."
	stoppedText   = "Stopped."
	idleStopText  = "Nothing was running, so there was nothing to stop."
	cantStopText  = "I couldn't stop the work. An operator should check the logs."
	// refusedText answers somebody the conductor will not run anything for. It is the same
	// whichever check they failed — not in Podium, a guest, another domain — as Slack's 🚫
	// is, so it tells nobody more about who has an account than Slack does.
	refusedText = "I can only work for people who are signed in to Podium. Sign in to Podium " +
		"with the same email address you use in Linear, then ask me again."
)

// accountTTL is how long what Linear said about a speaker's account is trusted, as Slack's is:
// whether somebody is still an active, full member of the workspace is asked again after it.
// What it said about a team's privacy is kept as long, for the same reason.
const accountTTL = 10 * time.Minute

// The labels of the links the source adds to a session.
const (
	taskLinkLabel        = "Podium task"
	pullRequestLinkLabel = "Pull request"
)

// StopReason is what the control plane records as why a task a Stop cancelled was cancelled.
const StopReason = "stopped from the Linear agent session"

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
	// Stop stops the turn answering a conversation, named by its source key, and the turns
	// queued behind it, and reports whether anything was stopped: the conductor's
	// CancelSessionTurn. A source never holds the conductor, so it is handed this. Nil stops
	// nothing, and a Stop pressed in Linear is answered with an error saying so.
	Stop func(ctx context.Context, sourceKey string) (bool, error)
	// Metrics is optional; nil registers nothing.
	Metrics *Metrics
	Logger  *slog.Logger
	// HTTPClient carries attachment uploads, and lets a test point them at a stub.
	HTTPClient *http.Client
	// Clock is time.Now unless a test replaces it.
	Clock func() time.Time
	// AfterFunc runs f once d has passed and returns what cancels it. It is time.AfterFunc
	// unless a test replaces it, with Clock, so that it can move time itself.
	AfterFunc func(d time.Duration, f func()) (cancel func() bool)
}

// AppSource is Linear as an agent app: webhooks in, agent activities out. It is the same
// Kind as the poller, and only one of the two ever runs.
//
// Each agent session is one conversation, and a chat in Podium (MirrorKey). A delegation or an
// @mention opens one and starts a turn of the linear: true playbook; what people say in it
// afterwards is a follow-up. Everything the turn says goes back into the session as an
// activity. Linear writes the prompts, questions and answers into the issue's comments, which
// is where the issue and what was said on it outside the session are read from.
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
	stopTurn       func(ctx context.Context, sourceKey string) (bool, error)
	now            func() time.Time
	afterFunc      func(d time.Duration, f func()) func() bool

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
	// sessions is what the source remembers about each agent session between the
	// conductor's calls, by session id.
	sessions map[string]*appSession
	// accounts is the candidate login each speaker's account checks came to, by user id,
	// for accountTTL.
	accounts map[string]speakerAccount
	// teams is whether each team is private, as a conductor.Channel* privacy, by team id, for
	// accountTTL.
	teams map[string]teamPrivacy
}

// teamPrivacy is what Linear said about one team's privacy, and when it was asked.
type teamPrivacy struct {
	privacy string
	at      time.Time
}

// speakerAccount is what the account checks made of one Linear user: the email they may be
// matched to a Podium login by, or empty, and when it was asked.
type speakerAccount struct {
	login string
	at    time.Time
}

// appSession is what the source remembers about one agent session between the conductor's
// calls. Like the poller's, it is in memory only: after a restart a resumed turn starts with
// none of it, which costs a link added twice and a keep-alive that waits for the next turn.
//
// The conductor never says "a turn started" or "a turn ended" to a source in so many words,
// so it is read off what it does say. A turn starts with the placeholder, and the conductor
// runs one turn of a session at a time, so a placeholder is never part of the turn before it.
// It ends with an answer or a failure, or the reaction after it.
//
// Every field but send is guarded by AppSource.mu.
type appSession struct {
	// send is held across posting an activity, and across the check that it may still be
	// posted. Without it a keep-alive that checked the turn was running could land just after
	// the answer, and a thought after a response reopens a session that was complete.
	send sync.Mutex

	// lastActivity is when anything was last posted into the session, for the keep-alive,
	// and lastEphemeral when an ephemeral activity was, for ActivityThrottle.
	lastActivity  time.Time
	lastEphemeral time.Time
	// held is the newest ephemeral activity the throttle kept back, and heldTask the task it
	// came from. flush posts it when the window closes; flushGen tells a flush that was
	// cancelled from the one armed after it.
	held     *ActivityContent
	heldTask string
	flush    func() bool
	flushGen int
	// running is a turn that is working: started and not yet answered, failed, stopped or
	// waiting on a question. Only a running turn is kept alive.
	running bool
	// open is a turn that has started and not ended: working, or waiting on a question. A
	// refusal said while one is open must not end the session under it.
	open         bool
	keepAlive    func() bool
	keepAliveGen int
	// closing is the activity type the turn ended on, response or error, and empty while it
	// has not. Anything said after it — an attachment, a line about one — is said as the same
	// type, so Linear's view of how the session ended does not change.
	closing string
	// taskID is the newest task the session's turns ran, for the fallback attachment link and
	// to know which task a stop is about.
	taskID string
	// linked is every URL already added to the session's links.
	linked map[string]bool
	// moveChecked is set once the turn has looked at the issue's state, so a turn that parks
	// on a question and resumes does not look again.
	moveChecked bool
	// stopped is a session somebody pressed Stop in. Nothing more is said for the turn that
	// was stopped, whatever the conductor still hands over as it unwinds, until the next turn
	// starts. stoppedTasks are the tasks of stopped turns, refused for good: a stopped
	// task's last words can arrive after a new turn has started. A Stop heard by another
	// conductor reaches these through StoppedElsewhere, and a stop this source has forgotten
	// (a restart) is still recognised in the cancellation by its reason (Post).
	stopped      bool
	stoppedTasks map[string]bool
}

var (
	_ conductor.Source         = (*AppSource)(nil)
	_ conductor.ActivityPoster = (*AppSource)(nil)
	_ conductor.StopHearer     = (*AppSource)(nil)
)

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
	stopTurn := opts.Stop
	if stopTurn == nil {
		stopTurn = func(context.Context, string) (bool, error) {
			return false, errors.New("this conductor was not given a way to stop a turn")
		}
	}
	afterFunc := opts.AfterFunc
	if afterFunc == nil {
		afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
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
			me:         User{ID: opts.AppUserID, Name: opts.AppName},
			byPosition: true,
			states:     map[string]string{},
			assigned:   map[string]bool{},
			turns:      map[string]*turnState{},
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
		stopTurn:       stopTurn,
		now:            now,
		afterFunc:      afterFunc,
		work:           work,
		stopWork:       stop,
		revoked:        opts.Revoked,
		seen:           map[string]time.Time{},
		queues:         map[string][]func(context.Context){},
		sessions:       map[string]*appSession{},
		accounts:       map[string]speakerAccount{},
		teams:          map[string]teamPrivacy{},
	}, nil
}

// Kind implements conductor.Source.
func (s *AppSource) Kind() string { return Kind }

// Events implements conductor.Source. The channel closes when Run returns.
func (s *AppSource) Events() <-chan conductor.InboundEvent { return s.events }

// MirrorKey makes the agent app a mirrored source: each agent session is a chat in Podium, as
// a Slack thread and a pull request are, with the people who spoke in it as its participants.
// The key is the session's, out of the ref's fourth part, never the issue's: a second session
// on the same issue is a conversation of its own.
//
// A ref the poller wrote names no session, and is not mirrored. A polled ticket has no person
// behind it to own the chat or to be its participant, so a chat of it would be one nobody but
// the dev token could ever open.
func (s *AppSource) MirrorKey(ref string) (string, bool) {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil || sessionID == "" {
		return "", false
	}
	return SessionKey(sessionID), true
}

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

// stop ends the work in flight and waits for it, so nothing is emitted on a closed channel
// and no timer posts into a session after the source is gone.
func (s *AppSource) stop() {
	s.mu.Lock()
	s.stopped = true
	for _, st := range s.sessions {
		s.disarmLocked(st)
	}
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

// inbound is the event for one prompt. It names the speaker's account, the person who wrote
// this prompt — never the session's creator, since anybody who can see the issue may reply —
// and always names the provider, even with no account, because a Linear event that names none
// is the poller's and runs as nobody. The candidate Login is not set here: it takes a call to
// Linear, which is made off the webhook's request (candidate).
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
	ref := SessionRef(iss.ID, teamID, iss.Identifier, sess.ID)
	if sess.delegated() {
		ref = DelegatedSessionRef(iss.ID, teamID, iss.Identifier, sess.ID)
	}
	ev := conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  SessionKey(sess.ID),
		Ref:        ref,
		Author:     author,
		// The prompt's own id, which a retried delivery keeps: the session's chat records one
		// row per id, and only the arrival that wrote it starts a turn.
		ExternalID: "activity:" + act.ID,
		// A prompt has no page of its own. Linear writes it into the issue's comments, so its
		// link is the issue, and the session — the conversation the chat is a record of — is
		// the chat's own link.
		ExternalURL: iss.URL,
		ThreadURL:   sess.URL,
		URL:         iss.URL,
		// The chat is named after the issue, and renamed with it on the next prompt: Linear
		// tells an agent app nothing when an issue is retitled.
		Title:     title,
		TS:        ts,
		Mentioned: true,
		Text:      text,
		BriefKind: conductor.SourceLinear,
		// The playbook is named here rather than routed, as the poller names it.
		Playbook:        s.playbook(),
		SpeakerProvider: conductor.SpeakerLinear,
	}
	if act.User != nil && act.User.ID != "" {
		ev.SpeakerID = p.OrganizationID + "/" + act.User.ID
	}
	if text != said {
		ev.RecordText = said
	}
	return ev
}

// candidate is the email a prompt's speaker may be matched to a Podium login by, or empty. It
// is Slack's verifiedEmail for Linear, and every check is about who could be pretending: a
// guest or an app can have an email on the company's domain, and a match would run a turn with
// a real person's secrets. So the speaker must be:
//
//   - a user of the app's own workspace: a delivery for any other was already ignored, and the
//     app's token reads only its own;
//   - not a guest, not suspended, not an app user (an agent or an integration), and not this
//     app itself;
//   - holding an email. Linear has no "confirmed" flag to match Slack's: an account's email is
//     what its owner signs in to Linear with.
//
// What Linear says is asked with the app's token, because the payload carries only the id, the
// name and the email, and is remembered for accountTTL. A failure to ask is no candidate, and
// is not remembered: the speaker is refused this once, as Slack's is when users.info fails.
// The conductor then checks the domain and that the email is a login that has signed in.
func (s *AppSource) candidate(ctx context.Context, u *payloadUser) string {
	if u == nil || u.ID == "" || u.ID == s.appUserID {
		return ""
	}
	now := s.now()
	s.mu.Lock()
	acct, ok := s.accounts[u.ID]
	s.mu.Unlock()
	if ok && now.Sub(acct.at) < accountTTL {
		return acct.login
	}
	info, err := s.client.UserAccount(ctx, u.ID)
	if err != nil {
		s.logger.DebugContext(ctx, "linear: checking a speaker's account failed", "user", u.ID, "error", err)
		return ""
	}
	login := ""
	if info.Active && !info.Guest && !info.App {
		login = podiumLogin(info.Email)
	}
	s.mu.Lock()
	for id, a := range s.accounts {
		if now.Sub(a.at) >= accountTTL {
			delete(s.accounts, id)
		}
	}
	s.accounts[u.ID] = speakerAccount{login: login, at: now}
	s.mu.Unlock()
	return login
}

// privacy is whether a team is private, as the mirror records it (conductor.ChannelPrivate or
// conductor.ChannelPublic), or empty when it could not be asked. The payload does not say, and
// a private team's issue is one the share dialog must warn about, so it is asked once per
// team for accountTTL. Unknown is left unknown rather than guessed: the dialog warns about it
// as possibly private.
func (s *AppSource) privacy(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	now := s.now()
	s.mu.Lock()
	known, ok := s.teams[teamID]
	s.mu.Unlock()
	if ok && now.Sub(known.at) < accountTTL {
		return known.privacy
	}
	private, err := s.client.TeamPrivate(ctx, teamID)
	if err != nil {
		s.logger.DebugContext(ctx, "linear: reading whether a team is private failed", "team", teamID, "error", err)
		return ""
	}
	privacy := conductor.ChannelPublic
	if private {
		privacy = conductor.ChannelPrivate
	}
	s.mu.Lock()
	for id, t := range s.teams {
		if now.Sub(t.at) >= accountTTL {
			delete(s.teams, id)
		}
	}
	s.teams[teamID] = teamPrivacy{privacy: privacy, at: now}
	s.mu.Unlock()
	return privacy
}

// podiumLogin is an email Podium can match to a Google sign-in: one @, something either side
// of it, lowercased. Anything else is no login.
func podiumLogin(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.IndexByte(email, '@')
	if at <= 0 || at != strings.LastIndexByte(email, '@') || at == len(email)-1 {
		return ""
	}
	return email
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
// The placeholder starts a turn: the keep-alive with it. Progress said after the turn has
// answered — a file that was not produced, or one too large to relay — belongs to the answer
// and is said as the same type, because a thought after a response reopens the session.
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
	st := s.session(sessionID)
	if out.CancelReason == StopReason {
		// The turn's own "This was cancelled", for a task a Stop in this session cancelled. The
		// session was closed with "Stopped." by whichever conductor heard the Stop, and Linear
		// forbids saying anything after it. The control plane recorded why the task ended, so
		// this is known on every conductor, and on an outbox retry after a restart.
		s.mu.Lock()
		if out.TaskID != "" {
			st.stoppedTasks[out.TaskID] = true
		}
		s.mu.Unlock()
		s.logger.DebugContext(ctx, "not posting a stopped turn's cancellation into its linear session",
			"session", sessionID, "task_id", out.TaskID)
		return "", nil
	}
	if out.Type == conductor.OutProgress && out.Text == conductor.Placeholder {
		s.turnStarted(sessionID, st)
	}
	s.linkTask(ctx, sessionID, st, out.TaskID)
	content, ephemeral := activityFor(out)
	s.mu.Lock()
	if closing := st.closing; out.Type == conductor.OutProgress && closing != "" {
		content, ephemeral = ActivityContent{Type: closing, Body: content.Body}, false
	}
	s.mu.Unlock()
	if strings.TrimSpace(content.Body) == "" {
		return "", nil
	}
	id, err := s.send(ctx, sessionID, st, content, ephemeral, out.TaskID, false)
	if err != nil {
		return "", fmt.Errorf("linear: post a %s in session %s on %s: %w", content.Type, sessionID, issueID, err)
	}
	return id, nil
}

// Edit replaces a progress post with a newer one. An activity cannot be edited, but an
// ephemeral thought is replaced by the next activity, which is the same thing on screen. It
// shares ActivityThrottle with the turn's tool calls, newest first.
func (s *AppSource) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return s.comments.Edit(ctx, ref, msgID, out)
	}
	st := s.session(sessionID)
	s.linkTask(ctx, sessionID, st, out.TaskID)
	body := thoughtText(out.Text)
	if body == "" {
		return nil
	}
	return s.throttled(ctx, sessionID, st, ActivityContent{Type: ActivityThought, Body: body}, out.TaskID)
}

// PostActivity implements conductor.ActivityPoster: one tool call as an ephemeral action, or
// one piece of reasoning as an ephemeral thought. They replace each other on screen, so a
// session shows what the turn is doing now rather than a trail of it — which is why Linear,
// unlike a comment thread, can be told. A turn the poller started is told nothing.
func (s *AppSource) PostActivity(ctx context.Context, ref string, out conductor.Outbound) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil || sessionID == "" {
		return err
	}
	content, ok := actionFor(out.Text)
	if !ok {
		return nil
	}
	st := s.session(sessionID)
	s.linkTask(ctx, sessionID, st, out.TaskID)
	return s.throttled(ctx, sessionID, st, content, out.TaskID)
}

// React shows a turn's state where Linear has somewhere to show it. The session's own state is
// Linear's to derive from the last activity, so the answer or the error already says how a
// turn went; what a reaction moves here is the source's own bookkeeping.
//
//   - working is a turn working, so it is kept alive. In a session opened by delegating the
//     issue, the first time in a turn it also moves the issue to the team's first started
//     state, unless it is already started, completed or cancelled. A mention does not: a
//     question asked in a comment is not a request to move the ticket.
//   - awaiting is a question waiting for a person, which is not kept alive: a thought would
//     tell Linear the agent is working when it is waiting.
//   - done and failed end the turn.
//   - refused is somebody the conductor will not run anything for, told so in the session
//     (refuseSpeaker). Nothing ran, so nothing moved and nothing is kept alive.
//
// A ref with no session reacts as the poller does.
func (s *AppSource) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	issueID, teamID, _, sessionID, err := ParseSessionRef(ref)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return s.comments.React(ctx, ref, kind)
	}
	switch kind {
	case conductor.ReactionWorking:
		st := s.session(sessionID)
		s.mu.Lock()
		if st.stopped {
			// The stopped turn, unparking as it unwinds. It is not working.
			s.mu.Unlock()
			return nil
		}
		st.running = true
		s.armKeepAliveLocked(sessionID, st, KeepAliveAfter)
		move := delegatedRef(ref) && !st.moveChecked
		st.moveChecked = true
		s.mu.Unlock()
		if !move {
			return nil
		}
		return s.comments.startUnlessUnderway(ctx, issueID, teamID)
	case conductor.ReactionAwaiting:
		st := s.session(sessionID)
		s.mu.Lock()
		st.running = false
		s.disarmLocked(st)
		s.mu.Unlock()
		return nil
	case conductor.ReactionDone, conductor.ReactionFailed:
		st := s.session(sessionID)
		s.mu.Lock()
		s.endTurnLocked(st)
		s.mu.Unlock()
		return nil
	case conductor.ReactionRefused:
		return s.refuseSpeaker(ctx, sessionID)
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
	st := s.session(sessionID)
	s.mu.Lock()
	if s.refusedLocked(st, file.TaskID) {
		// Nothing is uploaded for a turn that was stopped.
		s.mu.Unlock()
		return nil
	}
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

// LinkPullRequests implements the conductor's optional pull-request half of Source: each
// pull request a turn's answer named is added to the session's links, beside the task. Linear
// shows them by the session, so whoever delegated the issue finds the work without reading
// the answer for a URL.
func (s *AppSource) LinkPullRequests(ctx context.Context, ref string, prs []conductor.PullRequest) error {
	_, _, _, sessionID, err := ParseSessionRef(ref)
	if err != nil || sessionID == "" {
		return err
	}
	st := s.session(sessionID)
	var links []ExternalURL
	s.mu.Lock()
	if st.stopped {
		s.mu.Unlock()
		return nil
	}
	for _, pr := range prs {
		if pr.URL == "" || st.linked[pr.URL] {
			continue
		}
		st.linked[pr.URL] = true
		links = append(links, ExternalURL{Label: pullRequestLinkLabel, URL: pr.URL})
	}
	s.mu.Unlock()
	if err := s.client.AddSessionLinks(ctx, sessionID, links...); err != nil {
		return fmt.Errorf("linear: link pull requests to session %s: %w", sessionID, err)
	}
	return nil
}

// refuseSpeaker tells somebody who is not in Podium, in the session they spoke in, that nothing
// will run for them. Slack's 🚫 needs no words because a reaction is all Slack has; a session
// has no reactions, and a refusal with no words reads as a fault.
//
// It is an error, which ends the session as Linear draws it: nothing is working in it, and
// the person should not be left looking at "Starting on this." — unless somebody else's turn
// is open in the same session, which an error would end under them. The refusal is then a
// thought that stays, and the session goes on showing that turn.
//
// It is said even in a session somebody stopped: it is the source's own word about a new
// prompt, not the stopped turn's.
func (s *AppSource) refuseSpeaker(ctx context.Context, sessionID string) error {
	st := s.session(sessionID)
	s.mu.Lock()
	open := st.open
	s.mu.Unlock()
	content := ActivityContent{Type: ActivityError, Body: refusedText}
	if open {
		content.Type = ActivityThought
	}
	if _, err := s.send(ctx, sessionID, st, content, false, "", true); err != nil {
		return fmt.Errorf("linear: refuse a speaker in session %s: %w", sessionID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// one session's bookkeeping
// ---------------------------------------------------------------------------

// session returns the bookkeeping for a session, creating it on first use.
func (s *AppSource) session(sessionID string) *appSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[sessionID]
	if st == nil {
		st = &appSession{linked: map[string]bool{}, stoppedTasks: map[string]bool{}}
		s.sessions[sessionID] = st
	}
	return st
}

// turnStarted is the placeholder of a new turn: whatever the last one ended on, this one has
// not ended, and it has not looked at the issue's state. It is also the end of a stop: the
// conductor runs a session's turns one at a time, so the stopped turn has finished by now.
func (s *AppSource) turnStarted(sessionID string, st *appSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.stopped = false
	st.closing = ""
	st.moveChecked = false
	st.running = true
	st.open = true
	st.lastActivity = s.now()
	s.armKeepAliveLocked(sessionID, st, KeepAliveAfter)
}

// endTurnLocked is a turn that is not working any more: answered, failed or stopped. Nothing
// it held back is said, and it is not kept alive. The caller holds s.mu.
func (s *AppSource) endTurnLocked(st *appSession) {
	st.running = false
	st.open = false
	s.disarmLocked(st)
}

// disarmLocked cancels both of a session's timers and forgets what the throttle held. The
// caller holds s.mu.
func (s *AppSource) disarmLocked(st *appSession) {
	st.held, st.heldTask = nil, ""
	if st.flush != nil {
		st.flush()
		st.flush = nil
	}
	st.flushGen++
	if st.keepAlive != nil {
		st.keepAlive()
		st.keepAlive = nil
	}
	st.keepAliveGen++
}

// refusedLocked reports whether something from taskID may not be said in the session: it was
// stopped, and this is the stopped turn, or a task a stop already named. A task heard of while
// the session is stopped is the stopped turn's, and is remembered as stopped. The caller holds
// s.mu.
func (s *AppSource) refusedLocked(st *appSession, taskID string) bool {
	if taskID != "" && st.stoppedTasks[taskID] {
		return true
	}
	if !st.stopped {
		return false
	}
	if taskID != "" {
		st.stoppedTasks[taskID] = true
	}
	return true
}

// send posts one activity into a session and records what it did to the session: an answer
// or a failure ends the turn, a question parks it. It is the one place activities are posted
// from, so the checks that may stop one — a stopped turn, an ephemeral one after the answer —
// are made here, under st.send, against the session as it is at that moment. closing is the
// source's own last word after a stop, and is the one thing a stopped session takes.
func (s *AppSource) send(
	ctx context.Context, sessionID string, st *appSession, content ActivityContent, ephemeral bool,
	taskID string, closing bool,
) (string, error) {
	st.send.Lock()
	defer st.send.Unlock()
	s.mu.Lock()
	refused := !closing && s.refusedLocked(st, taskID)
	late := ephemeral && st.closing != ""
	s.mu.Unlock()
	if refused || late {
		s.logger.DebugContext(ctx, "not posting into a linear session that was stopped or has answered",
			"session", sessionID, "type", content.Type, "stopped", refused)
		return "", nil
	}
	if taskID != "" {
		s.mu.Lock()
		st.taskID = taskID
		s.mu.Unlock()
	}
	id, err := s.client.CreateAgentActivity(ctx, sessionID, content, ephemeral)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	st.lastActivity = now
	if ephemeral {
		st.lastEphemeral = now
	}
	switch content.Type {
	case ActivityResponse, ActivityError:
		st.closing = content.Type
		s.endTurnLocked(st)
	case ActivityElicitation:
		// Waiting for a person. Whatever was held is older than the question, and the turn
		// is not working until somebody answers.
		st.running = false
		s.disarmLocked(st)
	}
	return id, nil
}

// throttled posts an ephemeral activity, or holds it when the session had one inside
// ActivityThrottle. Only the newest held one is posted, when the window closes; an answer,
// a question or a stop before then drops it.
func (s *AppSource) throttled(
	ctx context.Context, sessionID string, st *appSession, content ActivityContent, taskID string,
) error {
	s.mu.Lock()
	if s.refusedLocked(st, taskID) || st.closing != "" {
		s.mu.Unlock()
		return nil
	}
	if wait := ActivityThrottle - s.now().Sub(st.lastEphemeral); wait > 0 {
		st.held, st.heldTask = &content, taskID
		s.armFlushLocked(sessionID, st, wait)
		s.mu.Unlock()
		return nil
	}
	st.held, st.heldTask = nil, ""
	s.mu.Unlock()
	_, err := s.send(ctx, sessionID, st, content, true, taskID, false)
	if err != nil {
		return fmt.Errorf("linear: post a %s in session %s: %w", content.Type, sessionID, err)
	}
	return nil
}

// armFlushLocked has the held activity posted in d, unless a flush is already due. The
// caller holds s.mu.
func (s *AppSource) armFlushLocked(sessionID string, st *appSession, d time.Duration) {
	if st.flush != nil || s.stopped {
		return
	}
	st.flushGen++
	gen := st.flushGen
	st.flush = s.afterFunc(d, func() { s.flushHeld(sessionID, st, gen) })
}

// flushHeld posts what the throttle held, once its window has closed.
func (s *AppSource) flushHeld(sessionID string, st *appSession, gen int) {
	s.mu.Lock()
	if gen != st.flushGen {
		// Cancelled, and something has been armed since.
		s.mu.Unlock()
		return
	}
	st.flush = nil
	held, taskID := st.held, st.heldTask
	if held == nil || s.stopped {
		s.mu.Unlock()
		return
	}
	if wait := ActivityThrottle - s.now().Sub(st.lastEphemeral); wait > 0 {
		// Something ephemeral went out directly since this was armed.
		s.armFlushLocked(sessionID, st, wait)
		s.mu.Unlock()
		return
	}
	st.held, st.heldTask = nil, ""
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if _, err := s.send(s.work, sessionID, st, *held, true, taskID, false); err != nil {
		s.logger.WarnContext(s.work, "linear: posting a held activity failed", "session", sessionID, "error", err)
	}
}

// armKeepAliveLocked has the session checked for silence in d, unless a check is already
// due. The caller holds s.mu.
func (s *AppSource) armKeepAliveLocked(sessionID string, st *appSession, d time.Duration) {
	if st.keepAlive != nil || s.stopped {
		return
	}
	st.keepAliveGen++
	gen := st.keepAliveGen
	st.keepAlive = s.afterFunc(d, func() { s.keepAliveFired(sessionID, st, gen) })
}

// keepAliveFired says something in a running turn that has said nothing for KeepAliveAfter,
// so Linear does not mark the session stale, and checks again later. A turn that said
// something since is checked again KeepAliveAfter after that. A turn that is not running —
// answered, failed, waiting on a question, stopped — is not checked again until it runs.
func (s *AppSource) keepAliveFired(sessionID string, st *appSession, gen int) {
	s.mu.Lock()
	if gen != st.keepAliveGen {
		s.mu.Unlock()
		return
	}
	st.keepAlive = nil
	if s.stopped || !st.running || st.stopped {
		s.mu.Unlock()
		return
	}
	if idle := s.now().Sub(st.lastActivity); idle < KeepAliveAfter {
		s.armKeepAliveLocked(sessionID, st, KeepAliveAfter-idle)
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if _, err := s.send(s.work, sessionID, st,
		ActivityContent{Type: ActivityThought, Body: keepAliveText}, true, "", false); err != nil {
		s.logger.WarnContext(s.work, "linear: keeping a session alive failed", "session", sessionID, "error", err)
	}
	s.mu.Lock()
	if st.running && !st.stopped {
		s.armKeepAliveLocked(sessionID, st, KeepAliveAfter)
	}
	s.mu.Unlock()
}

// linkTask adds the Podium task to the session's links the first time the session hears of
// it, which is the first thing the turn says once its task exists: the conductor posts the
// placeholder before there is one.
func (s *AppSource) linkTask(ctx context.Context, sessionID string, st *appSession, taskID string) {
	if taskID == "" {
		return
	}
	url := s.comments.taskURL(taskID)
	s.mu.Lock()
	if url == "" || st.linked[url] || s.refusedLocked(st, taskID) {
		s.mu.Unlock()
		return
	}
	st.linked[url] = true
	s.mu.Unlock()
	if err := s.client.AddSessionLinks(ctx, sessionID, ExternalURL{Label: taskLinkLabel, URL: url}); err != nil {
		// The answer names the task too, so a link that did not land costs a click.
		s.logger.WarnContext(ctx, "linear: linking the task to the session failed",
			"session", sessionID, "task_id", taskID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

// stopping marks a session stopped the moment a Stop arrives, before the conductor has been
// asked to do anything: from here on nothing the turn says is posted. Prompts of the session
// still waiting to reach the conductor are dropped, since they were sent before the Stop.
func (s *AppSource) stopping(sessionID string) {
	st := s.session(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.stopped = true
	if st.taskID != "" {
		st.stoppedTasks[st.taskID] = true
	}
	s.endTurnLocked(st)
	if _, draining := s.queues[sessionID]; draining {
		s.queues[sessionID] = nil
	}
}

// closeStopped asks the conductor to stop the session's work and closes the session with one
// response, as Linear requires of an agent after a Stop: "Stopped.", or that nothing was
// running. A stop that failed is closed with an error instead, and the session stays stopped:
// the person asked for nothing more to happen in it.
func (s *AppSource) closeStopped(ctx context.Context, sessionID string) {
	stopped, err := s.stopTurn(ctx, SessionKey(sessionID))
	content := ActivityContent{Type: ActivityResponse, Body: stoppedText}
	switch {
	case err != nil:
		s.logger.ErrorContext(ctx, "linear: stopping a session's turn failed", "session", sessionID, "error", err)
		content = ActivityContent{Type: ActivityError, Body: cantStopText}
	case !stopped:
		content.Body = idleStopText
	}
	st := s.session(sessionID)
	s.mu.Lock()
	// Marked again rather than left as stopping marked it: a turn that was starting as the
	// Stop arrived may have posted its placeholder since, which ends a stop, and it is that
	// turn the conductor just cancelled.
	st.stopped = err != nil || stopped
	if st.stopped && st.taskID != "" {
		st.stoppedTasks[st.taskID] = true
	}
	s.endTurnLocked(st)
	s.mu.Unlock()
	if _, err := s.send(ctx, sessionID, st, content, false, "", true); err != nil {
		s.logger.WarnContext(ctx, "linear: closing a stopped session failed", "session", sessionID, "error", err)
	}
}

// StoppedElsewhere implements conductor.StopHearer: a conductor is cancelling the turn of one
// of this app's sessions because somebody pressed Stop — another conductor, whose source heard
// the Stop, or this one, which then learns the task it may not have known. The turn may be
// running here. Its task is refused from now on, as if the Stop had arrived here, so nothing
// it says as it dies lands after "Stopped." — which would show the session working again. A
// host turn has no task, and is refused as the session's open turn, when there is one here.
//
// Unlike a Stop heard here, it neither closes the session (the source that heard it does) nor
// drops this conductor's queued prompts: those may have arrived after the Stop.
func (s *AppSource) StoppedElsewhere(sourceKey, taskID string) {
	sessionID, ok := strings.CutPrefix(sourceKey, SessionKey(""))
	if !ok || sessionID == "" {
		return
	}
	st := s.session(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case taskID != "":
		st.stoppedTasks[taskID] = true
		if st.taskID == taskID {
			s.endTurnLocked(st)
		}
	case st.open:
		st.stopped = true
		if st.taskID != "" {
			st.stoppedTasks[st.taskID] = true
		}
		s.endTurnLocked(st)
	}
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
