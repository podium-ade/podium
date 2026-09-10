package linear

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// Kind is the source kind Linear sessions are recorded under.
const Kind = "linear"

// FirstRunLookback is how far the first tick of a database with no cursor looks. An
// assignment made while the conductor was down for a day is still picked up; nothing older
// is, because a ticket assigned last month and never started is not news.
const FirstRunLookback = 24 * time.Hour

// MaxBackoff caps the rate-limit backoff. Beyond five minutes a human has noticed.
const MaxBackoff = 5 * time.Minute

// EditThrottle is the shortest gap between two edits of one turn's working comment. The
// newest text wins and an edit inside the window is superseded rather than queued: the
// outcome edit at the end of the turn overwrites the comment anyway.
const EditThrottle = 2 * time.Second

// MaxUploadBytes is the largest attachment this source uploads into Linear. Above it the
// comment gets a link to the Podium task page instead.
const MaxUploadBytes = 50 << 20

// The three things the working comment can say. It is created by the first progress post
// and rewritten exactly once more, when the turn ends.
const (
	doneText   = "✅ Done — see below"
	failedText = "❌ Failed"
)

// SessionLookup reports whether a source key already has a session and, if so, when its
// last turn started. It is injected rather than read from the store because a source never
// touches the store.
//
// The zero time with ok == true means a session exists that has never run a turn.
type SessionLookup func(ctx context.Context, sourceKey string) (lastTurnAt time.Time, ok bool)

// CursorStore persists the poll watermark. Get returns the zero time when there is none.
type CursorStore struct {
	Get func(ctx context.Context) (time.Time, error)
	Put func(ctx context.Context, at time.Time) error
}

// Options configures the source.
type Options struct {
	// APIKey is the bot user's PERSONAL API key. Required. SENSITIVE.
	APIKey string
	// Endpoint is the GraphQL endpoint. Required.
	Endpoint string
	// PollInterval is how often assigned issues are asked for. Required.
	PollInterval time.Duration
	// Playbook names the playbook Linear tickets run — the one with linear: true. Required: a
	// ticket has no channel and no /playbook prefix, so the source names it and the profile's
	// routing rules are bypassed. It is a function because that playbook can change while the
	// process runs, and it must return non-empty at New.
	Playbook func() string
	// TaskURL renders a link to a Podium task page for a human. It is the fallback when an
	// attachment cannot be uploaded into the conversation.
	TaskURL func(taskID string) string
	// Session is how the source tells an assignment from a follow-up.
	Session SessionLookup
	// Cursor persists the poll watermark.
	Cursor CursorStore
	// Metrics is optional; nil registers nothing.
	Metrics *Metrics
	Logger  *slog.Logger
	// HTTPClient lets a test point at an httptest server.
	HTTPClient *http.Client
	// Clock is time.Now unless a test replaces it. It is only read for the edit throttle
	// and the first-run lookback.
	Clock func() time.Time
}

// Source is the Linear integration.
type Source struct {
	client   *Client
	logger   *slog.Logger
	metrics  *Metrics
	events   chan conductor.InboundEvent
	interval time.Duration
	playbook func() string
	taskURL  func(string) string
	session  SessionLookup
	cursor   CursorStore
	now      func() time.Time
	upload   *http.Client

	// me is the bot user, learned once in Run. Its id is the assignee filter and its
	// comments are the ones that never start a turn.
	me User

	mu sync.Mutex
	// states caches each team's "In Progress" state id for the process's lifetime. A
	// team's board does not change while a conductor runs, and a query per turn would.
	states map[string]string
	// assigned remembers which source keys this process has already emitted an assignment
	// for, closing the window between the emit and the conductor's UpsertSession. One
	// short string per ticket the bot was assigned during this process's life.
	assigned map[string]bool
	// turns is the per-turn comment bookkeeping, keyed by ref.
	turns map[string]*turnState
	// backoff is the current rate-limit delay, zero when polling normally.
	backoff time.Duration
}

// turnState is what the source has to remember between the conductor's calls for one turn.
// It is deliberately in memory only: after a restart the ids are gone, a resumed turn posts
// rather than edits, and that is the same trade the Slack source makes.
type turnState struct {
	// working is the "working…" comment. The outcome edit rewrites it.
	working string
	// held is a progress text the throttle superseded. The next edit sends it; the outcome
	// edit drops it.
	held     string
	haveHeld bool
	lastEdit time.Time
	// attachTo and attachBody are the comment attachments are appended to — the final —
	// and its body as this source last wrote it.
	attachTo   string
	attachBody string
	// taskID is the task the turn is running, for the footer and the fallback link.
	taskID string
}

var _ conductor.Source = (*Source)(nil)

// New builds the source. Nothing is dialled until Run.
func New(opts Options) (*Source, error) {
	switch {
	case opts.Playbook == nil || opts.Playbook() == "":
		return nil, errors.New("linear: no playbook sets `linear: true`, so there is nothing to run a " +
			"ticket with. Set it on exactly one playbook — a playbooks/*.yaml, or one made in the web " +
			"UI — or unset PODIUM_AGENT_LINEAR_API_KEY")
	case opts.PollInterval <= 0:
		return nil, errors.New("linear: a poll interval is required")
	case opts.Session == nil:
		return nil, errors.New("linear: a session lookup is required")
	case opts.Cursor.Get == nil || opts.Cursor.Put == nil:
		return nil, errors.New("linear: a cursor store is required")
	}
	client, err := NewClient(ClientOptions{
		Endpoint:   opts.Endpoint,
		APIKey:     opts.APIKey,
		HTTPClient: opts.HTTPClient,
	})
	if err != nil {
		return nil, err
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
	return &Source{
		client:   client,
		logger:   logger,
		metrics:  metrics,
		events:   make(chan conductor.InboundEvent, 32),
		interval: opts.PollInterval,
		playbook: opts.Playbook,
		taskURL:  taskURL,
		session:  opts.Session,
		cursor:   opts.Cursor,
		now:      now,
		upload:   upload,
		states:   map[string]string{},
		assigned: map[string]bool{},
		turns:    map[string]*turnState{},
	}, nil
}

// Kind implements conductor.Source.
func (s *Source) Kind() string { return Kind }

// Events implements conductor.Source. The channel closes when Run returns.
func (s *Source) Events() <-chan conductor.InboundEvent { return s.events }

// Run learns who the API key belongs to and then polls until ctx is cancelled.
//
// A key that is set but does not work is FATAL: podium-agent exits non-zero at boot rather
// than discovering an hour later that no ticket was ever picked up.
func (s *Source) Run(ctx context.Context) error {
	defer close(s.events)

	me, err := s.client.Viewer(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("linear: the API key does not work (PODIUM_AGENT_LINEAR_API_KEY; it must be "+
			"a personal API key belonging to the bot user): %w", err)
	}
	s.me = me
	s.logger.InfoContext(ctx, "linear source connected", "bot_user_id", me.ID,
		"bot_user", me.label(), "poll_interval", s.interval, "playbook", s.playbook)

	for {
		s.tick(ctx)
		delay := s.delay()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// delay is how long to wait before the next tick: the poll interval, or the rate-limit
// backoff when one is in force.
func (s *Source) delay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoff > 0 {
		return s.backoff
	}
	return s.interval
}

// tick reads every page of issues assigned to the bot and touched since the watermark, and
// hands the events they imply to the conductor.
//
// The watermark is written ONCE, after the last page, and only then. `orderBy: updatedAt`
// is descending with no ascending option, so the newest issue is on the first page: writing
// the watermark per page would make a crash mid-tick skip every older page for good.
func (s *Source) tick(ctx context.Context) {
	since, err := s.since(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "reading the linear poll watermark failed; skipping this tick",
			"error", err)
		s.metrics.Polls.WithLabelValues(PollError).Inc()
		return
	}

	high := since
	after := ""
	for {
		issues, next, err := s.client.AssignedIssues(ctx, s.me.ID, since, after)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.fail(ctx, err)
			return
		}
		for _, issue := range issues {
			if issue.UpdatedAt.After(high) {
				high = issue.UpdatedAt
			}
			s.derive(ctx, issue)
		}
		if next == "" {
			break
		}
		after = next
	}

	s.ok()
	if high.After(since) {
		if err := s.cursor.Put(ctx, high); err != nil {
			// The events are already out. Not advancing the watermark replays this tick,
			// which the session gate and the last_turn_at comparison both absorb.
			s.logger.ErrorContext(ctx, "storing the linear poll watermark failed; the next tick "+
				"will re-read these issues", "error", err)
		}
	}
}

// since is the lower bound of this tick.
func (s *Source) since(ctx context.Context) (time.Time, error) {
	at, err := s.cursor.Get(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if at.IsZero() {
		return s.now().UTC().Add(-FirstRunLookback), nil
	}
	return at, nil
}

// fail records a failed tick and, when Linear throttled it, backs off.
func (s *Source) fail(ctx context.Context, err error) {
	if !isRateLimited(err) {
		s.logger.WarnContext(ctx, "polling linear failed", "error", err)
		s.metrics.Polls.WithLabelValues(PollError).Inc()
		return
	}
	s.mu.Lock()
	next := s.backoff * 2
	if next < s.interval {
		next = s.interval
	}
	if next > MaxBackoff {
		next = MaxBackoff
	}
	s.backoff = next
	s.mu.Unlock()

	s.metrics.Polls.WithLabelValues(PollRateLimited).Inc()
	s.metrics.Backoff.Set(next.Seconds())
	// Once per backoff step, not once per attempt: a throttled bot should not also fill
	// the log.
	s.logger.WarnContext(ctx, "linear rate-limited this poll; backing off",
		"backoff", next, "error", err)
}

// ok clears a backoff after a tick that worked.
func (s *Source) ok() {
	s.mu.Lock()
	had := s.backoff > 0
	s.backoff = 0
	s.mu.Unlock()
	if had {
		s.metrics.Backoff.Set(0)
	}
	s.metrics.Polls.WithLabelValues(PollOK).Inc()
}

// derive turns one issue into at most one inbound event.
func (s *Source) derive(ctx context.Context, issue Issue) {
	if issue.ID == "" {
		return
	}
	key := SourceKey(issue.ID)
	if issue.closed() {
		// A completed or cancelled ticket gets no turns, however it was edited. The
		// conductor's schema has no closed session state, so nothing is recorded: the
		// session simply stops producing turns.
		s.logger.DebugContext(ctx, "ignoring a linear issue that is finished",
			"issue", issue.Identifier, "state", issue.State.Name, "state_type", issue.State.Type)
		return
	}

	lastTurnAt, known := s.session(ctx, key)
	s.mu.Lock()
	alreadyAssigned := s.assigned[key]
	s.mu.Unlock()

	if !known && !alreadyAssigned {
		s.mu.Lock()
		s.assigned[key] = true
		s.mu.Unlock()
		s.metrics.Events.WithLabelValues(EventAssignment).Inc()
		s.emit(ctx, conductor.InboundEvent{
			SourceKey: key,
			Ref:       Ref(issue.ID, issue.Team.ID, issue.Identifier),
			// Linear does not say who assigned the issue in this query, and the assignee
			// is the bot. So the author is the assignee, or the source itself, said plainly.
			Author: firstNonEmpty(issue.Assignee.label(), "linear"),
			Text:   issue.brief(),
			TS:     issue.UpdatedAt,
			URL:    issue.URL,
		})
		return
	}

	// A follow-up: everything a human has said since the last turn started, oldest first,
	// as one instruction. Several comments inside one tick are one turn, not three.
	var fresh []Comment
	for _, c := range issue.Comments.Nodes {
		if !c.CreatedAt.After(lastTurnAt) || !s.human(c) || strings.TrimSpace(c.Body) == "" {
			continue
		}
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].CreatedAt.Before(fresh[j].CreatedAt) })

	bodies := make([]string, 0, len(fresh))
	for _, c := range fresh {
		bodies = append(bodies, strings.TrimSpace(c.Body))
	}
	last := fresh[len(fresh)-1]
	s.metrics.Events.WithLabelValues(EventFollowup).Inc()
	s.emit(ctx, conductor.InboundEvent{
		SourceKey: key,
		Ref:       Ref(issue.ID, issue.Team.ID, issue.Identifier),
		Author:    last.author(),
		Text:      strings.Join(bodies, "\n\n"),
		TS:        last.CreatedAt,
		URL:       issue.URL,
	})
}

// emit fills in the fields that are the same for every Linear event and offers it to the
// conductor. Env stays empty: the conductor honours it only for the dev source, and a
// source that could put environment on a task spec is a source that could hand a task a
// credential its playbook file never named.
func (s *Source) emit(ctx context.Context, ev conductor.InboundEvent) {
	ev.SourceKind = Kind
	ev.BriefKind = conductor.SourceLinear
	// The playbook is named here rather than routed: Select honours a non-empty Playbook first.
	ev.Playbook = s.playbook()
	select {
	case s.events <- ev:
	case <-ctx.Done():
	}
}

// human reports whether a comment was written by a person other than the bot.
//
// A nil User is the whole of the bot-comment test that matters: Linear's schema says the
// field is "null for comments created by integrations or bots without a user association",
// so anything with no user is machinery. isMe and the id comparison catch the case where
// the bot user posted through this very API key.
func (s *Source) human(c Comment) bool {
	if c.User == nil {
		return false
	}
	if c.User.IsMe || (s.me.ID != "" && c.User.ID == s.me.ID) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// FetchTranscript
// ---------------------------------------------------------------------------

// FetchTranscript reads the ticket and its comments, oldest first: the description is the
// first thing said and every comment follows it. The bot's own comments are the assistant's
// turns; its scaffolding — the working comment and the outcome edit — is left out.
func (s *Source) FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error) {
	issueID, _, _, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	issue, err := s.client.Issue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("linear: read issue %s: %w", issueID, err)
	}

	out := []conductor.BriefEntry{{
		Role:   conductor.RoleUser,
		Author: firstNonEmpty(issue.Assignee.label(), "linear"),
		TS:     conductor.BriefTimestamp(issue.CreatedAt),
		Text:   issue.brief(),
	}}
	for _, c := range issue.Comments.Nodes {
		text := strings.TrimSpace(c.Body)
		if text == "" {
			continue
		}
		role := conductor.RoleUser
		if !s.human(c) {
			if isScaffolding(text) {
				continue
			}
			role = conductor.RoleAssistant
		}
		out = append(out, conductor.BriefEntry{
			Role:   role,
			Author: c.author(),
			TS:     conductor.BriefTimestamp(c.CreatedAt),
			Text:   text,
		})
	}
	return out, nil
}

// isScaffolding reports whether one of the bot's own comments is a status line rather than
// something it said.
func isScaffolding(text string) bool {
	switch {
	case text == conductor.Placeholder, text == doneText, text == failedText:
		return true
	case strings.HasPrefix(text, "⏳"), strings.HasPrefix(text, "👀"):
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Post, Edit, React
// ---------------------------------------------------------------------------

// Post says something new. A turn produces at most two comments: one working comment,
// created by the first progress, and one final. Attachments are appended to the final
// rather than posted separately, so a turn with six screenshots is still two comments.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	issueID, _, _, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	body := out.Text
	if out.Type == conductor.OutFinal || out.Type == conductor.OutFailure {
		body = withFooter(body, out.TaskID)
	}
	id, err := s.client.AddComment(ctx, issueID, body)
	if err != nil {
		return "", fmt.Errorf("linear: comment on %s: %w", issueID, err)
	}

	st := s.state(ref)
	s.mu.Lock()
	if out.Type == conductor.OutProgress && st.working == "" {
		st.working = id
		st.lastEdit = s.now()
	}
	if out.Type == conductor.OutFinal {
		st.attachTo, st.attachBody = id, body
	}
	if out.TaskID != "" {
		st.taskID = out.TaskID
	}
	s.mu.Unlock()
	return id, nil
}

// Edit replaces a comment this source posted, at most once every EditThrottle. An edit
// inside the window is held and superseded by the next one — the same coalescing the Slack
// source does, and for the same reason: the runtime already emits progress at most every
// five seconds, so the throttle exists to bound a misbehaving one rather than a normal turn.
//
// A held edit at the end of a turn is dropped, because React rewrites the comment to the
// outcome anyway.
func (s *Source) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
	if _, _, _, err := ParseRef(ref); err != nil {
		return err
	}
	if msgID == "" {
		return errors.New("linear: no comment to edit")
	}
	st := s.state(ref)

	s.mu.Lock()
	text := out.Text
	if st.haveHeld {
		// The held text is older than this one, so this one wins outright.
		st.haveHeld, st.held = false, ""
	}
	if s.now().Sub(st.lastEdit) < EditThrottle {
		st.haveHeld, st.held = true, text
		s.mu.Unlock()
		return nil
	}
	st.lastEdit = s.now()
	s.mu.Unlock()

	if err := s.client.EditComment(ctx, msgID, text); err != nil {
		return fmt.Errorf("linear: edit comment %s: %w", msgID, err)
	}
	return nil
}

// React shows a turn's state on the ticket.
//
//   - working moves the issue to the team's "In Progress" state, which is what an engineer
//     scanning the board is looking for.
//   - done and failed rewrite the working comment. They deliberately do NOT move the state:
//     whether a ticket is finished is decided by a human reading the pull request, not by
//     the bot having opened one.
//
// The conductor posts its own plain-words failure message through Post before this is
// called, so React(failed) says nothing new — it only stops the comment claiming to still
// be working.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	issueID, teamID, _, err := ParseRef(ref)
	if err != nil {
		return err
	}
	switch kind {
	case conductor.ReactionWorking:
		return s.start(ctx, issueID, teamID)
	case conductor.ReactionDone:
		return s.outcome(ctx, ref, doneText)
	case conductor.ReactionFailed:
		return s.outcome(ctx, ref, failedText)
	default:
		return fmt.Errorf("linear: %q is not a reaction", kind)
	}
}

// start moves an issue into the team's In Progress state. A team with no such state and no
// started state at all is logged and left alone: a board Podium does not understand is not
// a reason to refuse the work.
func (s *Source) start(ctx context.Context, issueID, teamID string) error {
	if teamID == "" {
		s.logger.DebugContext(ctx, "no team on this ref; leaving the issue's state alone", "issue", issueID)
		return nil
	}
	stateID, err := s.inProgressState(ctx, teamID)
	if err != nil {
		return err
	}
	if stateID == "" {
		s.logger.WarnContext(ctx, "no workflow state is called "+InProgressStateName+
			" and none has type "+StateTypeStarted+"; leaving the issue's state alone",
			"team_id", teamID, "issue", issueID)
		return nil
	}
	if err := s.client.MoveIssue(ctx, issueID, stateID); err != nil {
		return fmt.Errorf("linear: move %s to %s: %w", issueID, InProgressStateName, err)
	}
	return nil
}

// inProgressState resolves and caches a team's In Progress state id: by name first, because
// that is the convention a team can see, then the lowest-positioned state with
// type: started, because a renamed column is still the column that means started.
func (s *Source) inProgressState(ctx context.Context, teamID string) (string, error) {
	s.mu.Lock()
	cached, ok := s.states[teamID]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}

	states, err := s.client.TeamStates(ctx, teamID)
	if err != nil {
		return "", fmt.Errorf("linear: read team %s workflow states: %w", teamID, err)
	}
	chosen := ""
	for _, st := range states {
		if strings.EqualFold(strings.TrimSpace(st.Name), InProgressStateName) {
			chosen = st.ID
			break
		}
	}
	if chosen == "" {
		started := make([]WorkflowState, 0, len(states))
		for _, st := range states {
			if st.Type == StateTypeStarted {
				started = append(started, st)
			}
		}
		sort.SliceStable(started, func(i, j int) bool { return started[i].Position < started[j].Position })
		if len(started) > 0 {
			chosen = started[0].ID
			s.logger.InfoContext(ctx, "no workflow state is called "+InProgressStateName+
				"; using the first one of type "+StateTypeStarted,
				"team_id", teamID, "state", started[0].Name)
		}
	}
	// An empty answer is cached too: the query is the expensive part and the board will
	// not sprout a column while this process runs.
	s.mu.Lock()
	s.states[teamID] = chosen
	s.mu.Unlock()
	return chosen, nil
}

// outcome rewrites the working comment to say how the turn ended. With no working comment —
// a turn resumed after a restart, whose comment ids died with the old process — there is
// nothing to rewrite and nothing to say: the final comment is the answer either way.
func (s *Source) outcome(ctx context.Context, ref, text string) error {
	st := s.state(ref)
	s.mu.Lock()
	id := st.working
	// A progress edit held by the throttle is dropped here on purpose: this text
	// supersedes it.
	st.haveHeld, st.held = false, ""
	s.mu.Unlock()
	if id == "" {
		s.logger.DebugContext(ctx, "no working comment for this turn; not marking the outcome", "ref", ref)
		return nil
	}
	if err := s.client.EditComment(ctx, id, text); err != nil {
		return fmt.Errorf("linear: mark the outcome on comment %s: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Attach
// ---------------------------------------------------------------------------

// Attach puts one file into the conversation.
//
// Linear has no message-with-files primitive: a file is uploaded to its asset store and
// then referenced from markdown. So the bytes go up first and the final comment is edited
// to carry the link — an image inline, anything else as a plain link. Two comments per turn
// is the budget and appending is what keeps to it.
//
// Anything that goes wrong — a file over the limit, an upload Linear refuses, a PUT that
// fails — degrades to a link to the Podium task page, which is where the artifact actually
// lives. The reader has to be logged in to Podium to open it, and that is said in the
// comment.
func (s *Source) Attach(ctx context.Context, ref string, file conductor.Attachment) error {
	if _, _, _, err := ParseRef(ref); err != nil {
		return err
	}
	switch {
	case file.Name == "":
		return errors.New("linear: an attachment needs a file name")
	case file.Body == nil:
		return fmt.Errorf("linear: attachment %q has no body", file.Name)
	}

	taskID := file.TaskID
	if taskID == "" {
		s.mu.Lock()
		taskID = s.state(ref).taskID
		s.mu.Unlock()
	}

	if file.Size > MaxUploadBytes {
		s.logger.InfoContext(ctx, "an attachment is larger than linear takes; linking to the task instead",
			"name", file.Name, "size_bytes", file.Size)
		return s.appendToFinal(ctx, ref, s.fallbackLine(file.Name, taskID))
	}
	link, err := s.uploadFile(ctx, file)
	if err != nil {
		s.logger.WarnContext(ctx, "uploading an attachment to linear failed; linking to the task instead",
			"name", file.Name, "error", err)
		return s.appendToFinal(ctx, ref, s.fallbackLine(file.Name, taskID))
	}
	return s.appendToFinal(ctx, ref, link)
}

// uploadFile asks Linear for an upload target, streams the bytes to it and returns the
// markdown that references the result.
func (s *Source) uploadFile(ctx context.Context, file conductor.Attachment) (string, error) {
	if file.Size <= 0 {
		// fileUpload declares the length up front, so a size nobody knows cannot be
		// uploaded. The caller always knows it (Artifact.size_bytes).
		return "", fmt.Errorf("attachment %q has no length", file.Name)
	}
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	target, err := s.client.PrepareUpload(ctx, file.Name, contentType, file.Size)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.UploadURL, file.Body)
	if err != nil {
		return "", fmt.Errorf("build the upload of %s: %w", file.Name, err)
	}
	req.ContentLength = file.Size
	req.Header.Set("Content-Type", contentType)
	for _, h := range target.Headers {
		req.Header.Set(h.Key, h.Value)
	}
	res, err := s.upload.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", file.Name, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody))
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("upload %s: HTTP %d", file.Name, res.StatusCode)
	}
	if isImage(file.Name, contentType) {
		return fmt.Sprintf("![%s](%s)", file.Name, target.AssetURL), nil
	}
	return fmt.Sprintf("[%s](%s)", file.Name, target.AssetURL), nil
}

// fallbackLine is what a comment says about a file that could not be uploaded.
func (s *Source) fallbackLine(name, taskID string) string {
	url := s.taskURL(taskID)
	if url == "" {
		return fmt.Sprintf("`%s` could not be attached here. It is an artifact of Podium task `%s`.",
			name, taskID)
	}
	return fmt.Sprintf("`%s` could not be attached here: [see it on the Podium task](%s) "+
		"(you have to be signed in to Podium).", name, url)
}

// appendToFinal adds a line to the comment the final was posted as. With no final comment —
// an answer that was empty, or a turn resumed after a restart — the line becomes its own
// comment, which is the one case a turn produces three.
func (s *Source) appendToFinal(ctx context.Context, ref, line string) error {
	st := s.state(ref)
	s.mu.Lock()
	id, body := st.attachTo, st.attachBody
	s.mu.Unlock()

	if id == "" {
		issueID, _, _, err := ParseRef(ref)
		if err != nil {
			return err
		}
		newID, err := s.client.AddComment(ctx, issueID, line)
		if err != nil {
			return fmt.Errorf("linear: comment with an attachment on %s: %w", issueID, err)
		}
		s.mu.Lock()
		st.attachTo, st.attachBody = newID, line
		s.mu.Unlock()
		return nil
	}

	updated := body + "\n\n" + line
	if err := s.client.EditComment(ctx, id, updated); err != nil {
		return fmt.Errorf("linear: append an attachment to comment %s: %w", id, err)
	}
	s.mu.Lock()
	st.attachBody = updated
	s.mu.Unlock()
	return nil
}

// state returns the per-turn bookkeeping for a ref, creating it on first use.
func (s *Source) state(ref string) *turnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.turns[ref]
	if st == nil {
		st = &turnState{}
		s.turns[ref] = st
	}
	return st
}

// ---------------------------------------------------------------------------
// refs and text
// ---------------------------------------------------------------------------

// Ref is what the conductor carries around for one turn. Three parts, like the Slack
// source's, and for a related reason: posting needs the issue, moving the state needs the
// team, and the identifier is what makes a branch name — and it has to survive a restart,
// because the ref is persisted as turns.trigger_ref and a resumed turn has nothing else.
func Ref(issueID, teamID, identifier string) string {
	return issueID + "/" + teamID + "/" + identifier
}

// ParseRef splits a Ref. Only the issue id is required: an issue with no team or no
// identifier is not something Linear produces, but a ref written by an older version must
// not make a resumed turn unpostable.
func ParseRef(ref string) (issueID, teamID, identifier string, err error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" {
		return "", "", "", fmt.Errorf("linear: %q is not an issue/team/identifier ref", ref)
	}
	return parts[0], parts[1], parts[2], nil
}

// SourceKey is a session's identity: the issue, for the life of the ticket.
func SourceKey(issueID string) string { return Kind + ":" + issueID }

// withFooter adds the one line that says which Podium task said this. It is the thread a
// human pulls when they want the logs, and it is the only thing this source adds to text
// that came out of a task.
func withFooter(text, taskID string) string {
	if taskID == "" {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n_Podium task " + taskID + "_"
}

// isImage reports whether a file should be embedded rather than linked.
func isImage(name, contentType string) bool {
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
