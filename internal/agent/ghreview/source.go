// Package ghreview is the GitHub App as a conductor.Source: webhooks turned into inbound events,
// comments posted back as the App, and Slack threads that asked for the same pull-request
// review joined onto the same session.
//
// The conversation identity is always github:owner/repo#N. Slack is a second door into
// this source, not a second conversation. The conductor answers it as a conversation —
// the assistant on the host, playbooks delegated — the way it answers Slack.
package ghreview

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/netlisten"
)

// Kind is the source kind GitHub sessions are recorded under.
const Kind = "github"

const (
	eventBuffer      = 32
	dedupeTTL        = 5 * time.Minute
	shutdownWait     = 5 * time.Second
	doneText         = "✅ Done — see below"
	failedText       = "❌ Failed"
	editThrottle     = 2 * time.Second
	reviewingText    = "Reviewing this pull request."
	reviewedNote     = "Review posted."
	reviewFailedText = "The review did not finish."
	replyFailedText  = "❌ I could not answer that."
	// statusMarker is in the status comment the conductor once posted for a review. It
	// posts none now, but an old one on a pull request is still scaffolding, not something
	// anybody said.
	statusMarker = "<!-- podium-review-status -->"
)

// ErrBoundToOther is a Slack thread that is already reviewing a different pull request.
var ErrBoundToOther = errors.New("this slack thread is already reviewing another pull request")

// SessionLookup reports whether a source key already has a session.
type SessionLookup func(ctx context.Context, sourceKey string) (lastTurnAt time.Time, ok bool)

// Surfaces is the bind table for Slack threads that asked for a PR review. Injected so
// this package never imports the store.
type Surfaces struct {
	LookupSlack func(ctx context.Context, slackRef string) (sourceKey string, ok bool, err error)
	BindSlack   func(ctx context.Context, slackRef, sourceKey string) error
	ListSlack   func(ctx context.Context, sourceKey string) ([]string, error)
}

// SlackBridge is the Slack source, as this package is allowed to see it: enough to fan
// an answer out to every bound thread and to merge those threads into a transcript.
type SlackBridge interface {
	FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error)
	Post(ctx context.Context, ref string, out conductor.Outbound) (string, error)
	Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error
	Attach(ctx context.Context, ref string, file conductor.Attachment) error
	React(ctx context.Context, ref string, kind conductor.Reaction) error
}

// SlackMention is one Slack @Podium that named a pull request, or that landed in a
// thread already bound to one.
type SlackMention struct {
	PR      conductor.PullRequest
	Channel string
	Thread  string
	Trigger string
	Author  string
	Text    string
	TS      time.Time
	// Permalink is the Slack link; the inbound event's URL is the pull request, because
	// that is the conversation.
	Permalink string
	// Login, SpeakerID and SpeakerAvatar are the Slack speaker as the Slack source proved
	// them: a candidate email and the "<team>/<user>" identities key. A Slack-started review
	// follows Slack's rule — the conductor resolves the candidate and refuses anybody who
	// is not in Podium.
	Login         string
	SpeakerID     string
	SpeakerAvatar string
}

// SourceOptions configures the GitHub review source.
type SourceOptions struct {
	AppID         string
	PrivateKey    *rsa.PrivateKey
	WebhookSecret string
	Listen        string
	APIURL        string
	Session       SessionLookup
	Surfaces      Surfaces
	Slack         SlackBridge
	TaskURL       func(string) string
	Logger        *slog.Logger
	HTTPClient    *http.Client
	Clock         func() time.Time
	// Routines keeps each verified delivery for the routines that run on GitHub events
	// (routine_events.go). Nil keeps nothing: this conductor runs no routines.
	Routines RoutineSink
	// RoutinesWant is asked before a commit's checks are read from GitHub for routines on
	// "all checks finished" (commit_checks.go). Nil reads every commit that finishes.
	RoutinesWant RoutineWants
	// Serve, when set, serves the webhook handler on a listener the caller owns: the
	// conductor's, which puts another app's webhooks on the same address when the two are
	// configured alike. It blocks until ctx ends, like Run. Nil binds Listen here.
	Serve func(ctx context.Context, listen, path string, h http.Handler) error
}

// Source is the GitHub App integration.
type Source struct {
	client        *restClient
	logger        *slog.Logger
	events        chan conductor.InboundEvent
	listen        string
	serve         func(ctx context.Context, listen, path string, h http.Handler) error
	webhookSecret string
	session       SessionLookup
	surfaces      Surfaces
	slack         SlackBridge
	// deliverer owes each copy fanned out to a bound Slack thread through the conductor's
	// outbox, so a copy that fails is retried and shown as not delivered. Nil (a source
	// built without a conductor) posts the copies directly, as before.
	deliverer atomic.Pointer[conductor.Deliverer]
	taskURL   func(string) string
	now       func() time.Time

	// routines keeps deliveries for the routines on GitHub events; nil keeps none.
	routines     RoutineSink
	routinesWant RoutineWants
	// checking is the commits whose checks are being read for routines, keyed repo@sha, and
	// whether another finish on one came in meanwhile. checksWG is those reads, and
	// checksSettle how long each waits first (commit_checks.go).
	checking     map[string]bool
	checksWG     sync.WaitGroup
	checksSettle time.Duration

	slug      string
	mentionRE *regexp.Regexp

	mu   sync.Mutex
	seen map[string]time.Time
	// turns is per-ref bookkeeping for edits other than progress, keyed by the triggering ref.
	turns map[string]*turnState
	// threadBots is whether the App started an inline review thread, keyed by the
	// thread's first comment. Who started a thread never changes.
	threadBots map[int64]bool
	// threadJoined is the inline threads a person started that the App has replied in.
	threadJoined map[int64]bool
	// roles is a commenter's role on a repository, keyed owner/repo/login, for roleTTL.
	roles map[string]knownRole
}

type turnState struct {
	held     string
	haveHeld bool
	lastEdit time.Time
}

var _ conductor.Source = (*Source)(nil)

// NewSource builds the source. Nothing is dialled until Run. The webhook listener is bound in Run.
func NewSource(opts SourceOptions) (*Source, error) {
	switch {
	case opts.PrivateKey == nil:
		return nil, errors.New("github: a private key is required")
	case opts.AppID == "":
		return nil, errors.New("github: an app id is required")
	case opts.WebhookSecret == "":
		return nil, errors.New("github: a webhook secret is required")
	case opts.Listen == "":
		return nil, errors.New("github: a webhook listen address is required")
	case opts.Session == nil:
		return nil, errors.New("github: a session lookup is required")
	}
	client, err := newRest(restOptions{
		BaseURL:    opts.APIURL,
		AppID:      opts.AppID,
		PrivateKey: opts.PrivateKey,
		HTTPClient: opts.HTTPClient,
		Clock:      opts.Clock,
	})
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	taskURL := opts.TaskURL
	if taskURL == nil {
		taskURL = func(string) string { return "" }
	}
	return &Source{
		client:        client,
		logger:        logger,
		events:        make(chan conductor.InboundEvent, eventBuffer),
		listen:        opts.Listen,
		serve:         opts.Serve,
		webhookSecret: opts.WebhookSecret,
		session:       opts.Session,
		surfaces:      opts.Surfaces,
		slack:         opts.Slack,
		taskURL:       taskURL,
		now:           now,
		routines:      opts.Routines,
		routinesWant:  opts.RoutinesWant,
		checksSettle:  checksSettle,
		seen:          map[string]time.Time{},
		turns:         map[string]*turnState{},
		threadBots:    map[int64]bool{},
		roles:         map[string]knownRole{},
		threadJoined:  map[int64]bool{},
	}, nil
}

// Kind implements conductor.Source.
func (s *Source) Kind() string { return Kind }

// Events implements conductor.Source. The channel closes when Run returns.
func (s *Source) Events() <-chan conductor.InboundEvent { return s.events }

// Run learns the App's slug and then serves POST /webhooks/github until ctx is cancelled.
// A slug that cannot be fetched is fatal: mentions would never match.
func (s *Source) Run(ctx context.Context) error {
	defer close(s.events)

	if err := s.identify(ctx); err != nil {
		return err
	}

	if s.serve != nil {
		s.logger.InfoContext(ctx, "github source connected", "slug", s.slug, "listen", s.listen)
		if err := s.serve(ctx, s.listen, WebhookPath, s.Handler()); err != nil {
			return fmt.Errorf("github: serve webhooks: %w", err)
		}
		return nil
	}

	ln, err := netlisten.Listen("tcp", s.listen)
	if err != nil {
		return fmt.Errorf("github: listen on %s: %w", s.listen, err)
	}
	s.logger.InfoContext(ctx, "github source connected",
		"slug", s.slug, "listen", ln.Addr().String())

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("github: serve webhooks: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.logger.WarnContext(ctx, "github: shutting the webhook listener down failed", "error", err)
	}
	return nil
}

func (s *Source) identify(ctx context.Context) error {
	app, err := s.client.App(ctx)
	if err != nil {
		return fmt.Errorf("github: GET /app failed (is the app id and private key right?): %w", err)
	}
	s.slug = app.Slug
	s.mentionRE = compileMention(app.Slug)
	return nil
}

// LookupSlack reports whether this Slack thread is already a door into a PR review.
func (s *Source) LookupSlack(ctx context.Context, slackRef string) (string, bool, error) {
	if s.surfaces.LookupSlack == nil {
		return "", false, nil
	}
	return s.surfaces.LookupSlack(ctx, slackRef)
}

// BindSlack records that this Slack thread is a door into sourceKey.
func (s *Source) BindSlack(ctx context.Context, slackRef, sourceKey string) error {
	if s.surfaces.BindSlack == nil {
		return errors.New("github: no surface store")
	}
	return s.surfaces.BindSlack(ctx, slackRef, sourceKey)
}

// IngestSlack turns a Slack mention that named (or is bound to) a pull request into the
// same inbound event a GitHub @mention would have produced. Slack's own Events channel
// never sees it.
func (s *Source) IngestSlack(ctx context.Context, m SlackMention) error {
	if m.PR.Owner == "" || m.PR.Number == 0 {
		return errors.New("github: ingest: a pull request is required")
	}
	key := SourceKeyPR(m.PR)
	text := slackInstruction(m.Author, m.PR.URL, m.Text)
	install, err := s.client.RepoInstallation(ctx, m.PR.Owner, m.PR.Repo)
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		s.logger.WarnContext(ctx, "github: looking up the installation for a slack-started review failed",
			"repo", m.PR.Owner+"/"+m.PR.Repo, "error", err)
	}
	s.emit(ctx, conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  key,
		Ref: Ref(Parsed{
			Owner: m.PR.Owner, Repo: m.PR.Repo, Number: m.PR.Number, InstallID: install,
			Kind: KindSlack, SlackChan: m.Channel, SlackThread: m.Thread, SlackTS: m.Trigger,
		}),
		Author:          m.Author,
		Login:           m.Login,
		SpeakerProvider: conductor.SpeakerSlack,
		SpeakerID:       m.SpeakerID,
		SpeakerAvatar:   m.SpeakerAvatar,
		// The Slack message is its own identity in the pull request's record: a Slack retry
		// of the mention is the same row, and never a second review.
		ExternalID:  m.Trigger,
		ExternalURL: m.Permalink,
		RecordText:  strings.TrimSpace(m.Text),
		Mentioned:   true,
		Text:        text,
		TS:          m.TS,
		URL:         m.PR.URL,
		// Slack does not carry the pull request's title. The number names a new chat; a chat
		// GitHub already named keeps its fuller title.
		Title:     ChatTitle(m.PR.Number, ""),
		BriefKind: conductor.SourceGitHub,
	})
	return nil
}

func (s *Source) emit(ctx context.Context, ev conductor.InboundEvent) {
	select {
	case s.events <- ev:
	case <-ctx.Done():
	}
}

func (s *Source) firstSighting(delivery string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, at := range s.seen {
		if now.Sub(at) > dedupeTTL {
			delete(s.seen, k)
		}
	}
	if _, ok := s.seen[delivery]; ok {
		return false
	}
	s.seen[delivery] = now
	return true
}

// MirrorKey is the session key a ref belongs to, so the conductor can keep a readable copy
// of this review in the Podium UI. It is the PR, never the triggering comment or the Slack
// thread, because those are doors into one conversation.
func (s *Source) MirrorKey(ref string) (string, bool) {
	p, err := ParseRef(ref)
	if err != nil {
		return "", false
	}
	return SourceKey(p.Owner, p.Repo, p.Number), true
}

// RefForKey is a ref naming the pull request a session key names, for reading its
// conversation when what the conductor holds is not a GitHub ref — a message somebody sent
// from the web chat into a PR's chat, whose ref is the chat's. It names no comment.
func (s *Source) RefForKey(sourceKey string) (string, bool) {
	owner, repo, number, ok := ParseSourceKey(sourceKey)
	if !ok {
		return "", false
	}
	return Ref(Parsed{Owner: owner, Repo: repo, Number: number, Kind: KindPullRequest}), true
}

// FetchTranscript reads the PR's conversation and any bound Slack threads, oldest first.
func (s *Source) FetchTranscript(ctx context.Context, ref string) ([]conductor.BriefEntry, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	install, err := s.installation(ctx, p)
	var gh []conductor.BriefEntry
	if err == nil {
		gh, err = s.githubTranscript(ctx, install, p)
		if err != nil {
			s.logger.WarnContext(ctx, "github: reading the pull request transcript failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		}
	} else if !errors.Is(err, ErrNotInstalled) {
		s.logger.WarnContext(ctx, "github: no installation for transcript",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
	}

	var sl []conductor.BriefEntry
	if s.slack != nil && s.surfaces.ListSlack != nil {
		refs, lerr := s.surfaces.ListSlack(ctx, SourceKey(p.Owner, p.Repo, p.Number))
		if lerr != nil {
			s.logger.WarnContext(ctx, "github: listing bound slack threads failed", "error", lerr)
		}
		for _, r := range refs {
			channel, thread, ok := splitSlackSurface(r)
			if !ok {
				continue
			}
			entries, ferr := s.slack.FetchTranscript(ctx, slackCoord(channel, thread, thread))
			if ferr != nil {
				s.logger.WarnContext(ctx, "github: reading a bound slack thread failed",
					"ref", r, "error", ferr)
				continue
			}
			sl = append(sl, entries...)
		}
	}
	return mergeTranscripts(gh, sl), nil
}

func (s *Source) githubTranscript(ctx context.Context, install int64, p Parsed) ([]conductor.BriefEntry, error) {
	issues, err := s.client.ListIssueComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}
	reviews, err := s.client.ListReviews(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}
	inline, err := s.client.ListReviewComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}

	var out []conductor.BriefEntry
	for _, c := range issues {
		if e, ok := s.entryFromComment(c); ok {
			out = append(out, e)
		}
	}
	for _, r := range reviews {
		text := strings.TrimSpace(r.Body)
		if text == "" {
			continue
		}
		if e, ok := s.entryFromUser(r.User, r.SubmittedAt, text); ok {
			out = append(out, e)
		}
	}
	for _, c := range inline {
		if e, ok := s.entryFromComment(c); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Source) entryFromComment(c Comment) (conductor.BriefEntry, bool) {
	return s.entryFromUser(c.User, c.CreatedAt, c.Body)
}

func (s *Source) entryFromUser(u User, ts time.Time, body string) (conductor.BriefEntry, bool) {
	text := strings.TrimSpace(body)
	if text == "" {
		return conductor.BriefEntry{}, false
	}
	role := conductor.RoleUser
	if s.isAppUser(u.Login, u.Type) {
		if isScaffolding(text) {
			return conductor.BriefEntry{}, false
		}
		role = conductor.RoleAssistant
	}
	return conductor.BriefEntry{
		Role:   role,
		Author: u.Login,
		TS:     conductor.BriefTimestamp(ts),
		Text:   text,
	}, true
}

func mergeTranscripts(a, b []conductor.BriefEntry) []conductor.BriefEntry {
	out := make([]conductor.BriefEntry, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].TS <= b[j].TS {
			out = append(out, a[i])
			i++
			continue
		}
		out = append(out, b[j])
		j++
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// Post says something new: a reply to whoever asked, from the assistant, a playbook or the
// conductor itself. It goes where they asked: the review thread a reply came from, the
// conversation tab, or only the Slack thread when Slack is where the question was. Progress
// is not posted. Bound Slack threads still hear every answer.
//
// Nobody's answer is a review. A playbook that reviews posts its review itself, with the
// GitHub token its task is given (gh pr review, or the reviews API), and a status comment too
// if its prompt asks for one: what a review is and how it reads is the playbook's to say.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	return s.postReply(ctx, p, out)
}

func (s *Source) postReply(ctx context.Context, p Parsed, out conductor.Outbound) (string, error) {
	body := out.Text
	switch out.Type {
	case conductor.OutFinal:
		// Replies to the review's own threads go into those threads. What is left is the
		// answer, which is posted where the question was asked.
		if asked(p) == "github" {
			rest, notes := splitThreadNotes(body)
			if len(notes) > 0 {
				body = withThreadNotes(rest, s.replyInThreads(ctx, p, notes))
				if body == "" {
					s.postSlack(ctx, p, out)
					return "", nil
				}
			}
		}
	case conductor.OutQuestion:
	case conductor.OutFailure:
		// The failure's own words can carry a task id. A pull request is often public, so
		// it hears that the answer did not come, and the operator has the log.
		body = replyFailedText
	default:
		return "", nil
	}
	if s.fanoutSlack(out) {
		s.postSlack(ctx, p, out)
	}
	if p.Kind == KindSlack || asked(p) == "podium" {
		return "", nil
	}
	id, gerr := s.postGitHub(ctx, p, body)
	if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
		s.logger.WarnContext(ctx, "github: posting a comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", gerr)
		return "", gerr
	}
	return id, nil
}

// asked is where the question a reply answers was asked. A pull request's ref with no comment
// is RefForKey's: a turn of its conversation nobody on GitHub started — a routine's run, or a
// message written in its chat in Podium. Its answer stays in Podium, where it was asked, and
// the pull request gets only what a playbook or a routine posts there itself.
func asked(p Parsed) string {
	switch {
	case p.Kind == KindSlack:
		return "slack"
	case p.Kind == KindPullRequest && p.CommentID == 0:
		return "podium"
	default:
		return "github"
	}
}

// replyInThreads posts each note in the thread of the review's inline comment at its
// path:line, and resolves the thread when the note says so. It returns the notes whose thread
// it could not find or reply in, for the caller to keep in its answer.
func (s *Source) replyInThreads(ctx context.Context, p Parsed, notes []threadNote) []threadNote {
	install, err := s.installation(ctx, p)
	if err != nil {
		return notes
	}
	comments, err := s.client.ListReviewComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the review threads failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		return notes
	}
	var missed []threadNote
	for _, n := range notes {
		root := s.threadAt(comments, n.Path, n.Line)
		if root == 0 {
			missed = append(missed, n)
			continue
		}
		if _, err := s.client.CreateReviewReply(ctx, install, p.Owner, p.Repo, p.Number, root, n.Body); err != nil {
			s.logger.WarnContext(ctx, "github: replying in a review thread failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "comment", root, "error", err)
			missed = append(missed, n)
			continue
		}
		if !n.Resolve {
			continue
		}
		if err := s.client.ResolveThread(ctx, install, p.Owner, p.Repo, p.Number, root); err != nil {
			s.logger.WarnContext(ctx, "github: resolving a review thread failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "comment", root, "error", err)
		}
	}
	return missed
}

// threadAt is the first comment of the latest thread this App started at path:line, where it
// was made or where that line is now. Zero when there is none.
func (s *Source) threadAt(comments []Comment, path string, line int) int64 {
	var root int64
	for _, c := range comments {
		if c.InReplyTo != 0 || c.Path != path || !s.isAppUser(c.User.Login, c.User.Type) {
			continue
		}
		if c.Line == line || c.OriginalLine == line {
			root = c.ID
		}
	}
	return root
}

func (s *Source) postGitHub(ctx context.Context, p Parsed, body string) (string, error) {
	install, err := s.installation(ctx, p)
	if err != nil {
		return "", err
	}
	var c Comment
	if p.Kind == KindReviewComment && p.CommentID > 0 {
		c, err = s.client.CreateReviewReply(ctx, install, p.Owner, p.Repo, p.Number, p.CommentID, body)
	} else {
		c, err = s.client.CreateIssueComment(ctx, install, p.Owner, p.Repo, p.Number, body)
	}
	if err != nil {
		return "", err
	}
	return strconvInt(c.ID), nil
}

func (s *Source) fanoutSlack(out conductor.Outbound) bool {
	switch out.Type {
	case conductor.OutFinal, conductor.OutFailure, conductor.OutQuestion:
		return true
	default:
		return false
	}
}

// SetDeliverer implements the conductor's deliverySetter: the conductor hands itself over so
// the Slack copies below go through its outbox.
func (s *Source) SetDeliverer(d conductor.Deliverer) {
	if d == nil {
		s.deliverer.Store(nil)
		return
	}
	s.deliverer.Store(&d)
}

func (s *Source) postSlack(ctx context.Context, p Parsed, out conductor.Outbound) {
	if s.slack == nil {
		return
	}
	var d conductor.Deliverer
	if dp := s.deliverer.Load(); dp != nil {
		d = *dp
	}
	for _, ref := range s.slackRefs(ctx, p) {
		if d != nil && d.DeliverTo(ctx, conductor.SourceSlack, ref, out) {
			continue
		}
		if _, err := s.slack.Post(ctx, ref, out); err != nil {
			s.logger.WarnContext(ctx, "github: posting to a bound slack thread failed",
				"ref", ref, "error", err)
		}
	}
}

// Edit replaces a GitHub comment this source posted. Progress is not one of
// those edits: the status comment keeps its title until the review finishes.
// Slack does not carry progress.
func (s *Source) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
	if out.Type == conductor.OutProgress {
		return nil
	}
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if msgID == "" {
		return errors.New("github: no comment to edit")
	}
	st := s.state(ref)

	s.mu.Lock()
	text := out.Text
	if st.haveHeld {
		st.haveHeld, st.held = false, ""
	}
	if s.now().Sub(st.lastEdit) < editThrottle {
		st.haveHeld, st.held = true, text
		s.mu.Unlock()
		return nil
	}
	st.lastEdit = s.now()
	s.mu.Unlock()

	install, err := s.installation(ctx, p)
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return nil
		}
		return err
	}
	id, err := parseID(msgID)
	if err != nil {
		return err
	}
	return s.client.EditIssueComment(ctx, install, p.Owner, p.Repo, id, text)
}

// Attach fans files out to bound Slack threads and leaves a task link on the PR when
// GitHub cannot take the bytes.
func (s *Source) Attach(ctx context.Context, ref string, file conductor.Attachment) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if s.slack != nil {
		for _, r := range s.slackRefs(ctx, p) {
			if err := s.slack.Attach(ctx, r, file); err != nil {
				s.logger.WarnContext(ctx, "github: attaching to a bound slack thread failed",
					"ref", r, "error", err)
			}
		}
	}
	link := s.taskURL(file.TaskID)
	if link == "" {
		return nil
	}
	_, err = s.postGitHub(ctx, p, fmt.Sprintf("[%s](%s)", file.Name, link))
	if errors.Is(err, ErrNotInstalled) {
		return nil
	}
	return err
}

// React marks the comment a turn answers with 👀 when the turn starts, so the person who
// asked sees it was heard before the answer lands. GitHub gets nothing else: how the work
// went is in the answer, or in the status comment's title for a review. Bound Slack threads
// get every reaction.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	switch kind {
	case conductor.ReactionWorking:
		s.reactSeen(ctx, p)
	case conductor.ReactionDone, conductor.ReactionFailed:
	case conductor.ReactionRefused:
		s.reactOn(ctx, p, reactionRefused)
	case conductor.ReactionAwaiting:
		return nil
	default:
		return fmt.Errorf("github: %q is not a reaction", kind)
	}
	if s.slack != nil {
		for _, r := range s.slackRefs(ctx, p) {
			if err := s.slack.React(ctx, r, kind); err != nil {
				s.logger.WarnContext(ctx, "github: reacting in a bound slack thread failed",
					"ref", r, "error", err)
			}
		}
	}
	return nil
}

// reactSeen puts 👀 on the triggering comment.
func (s *Source) reactSeen(ctx context.Context, p Parsed) { s.reactOn(ctx, p, reactionSeen) }

// reactOn puts one reaction on the triggering comment. A review body and a pull request have
// no comment to react on.
func (s *Source) reactOn(ctx context.Context, p Parsed, content string) {
	if p.CommentID <= 0 || (p.Kind != KindIssueComment && p.Kind != KindReviewComment) {
		return
	}
	install, err := s.installation(ctx, p)
	if err != nil {
		return
	}
	if err := s.client.CreateReaction(ctx, install, p.Owner, p.Repo, p.Kind, p.CommentID, content); err != nil {
		s.logger.WarnContext(ctx, "github: reacting to the comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "comment", p.CommentID, "error", err)
	}
}

func (s *Source) installation(ctx context.Context, p Parsed) (int64, error) {
	if p.InstallID > 0 {
		return p.InstallID, nil
	}
	return s.client.RepoInstallation(ctx, p.Owner, p.Repo)
}

func (s *Source) slackRefs(ctx context.Context, p Parsed) []string {
	var refs []string
	if p.SlackChan != "" && p.SlackThread != "" && p.SlackTS != "" {
		refs = append(refs, slackCoord(p.SlackChan, p.SlackThread, p.SlackTS))
	}
	if s.surfaces.ListSlack == nil {
		return unique(refs)
	}
	surfaces, err := s.surfaces.ListSlack(ctx, SourceKey(p.Owner, p.Repo, p.Number))
	if err != nil {
		s.logger.WarnContext(ctx, "github: listing bound slack threads failed", "error", err)
		return unique(refs)
	}
	for _, r := range surfaces {
		channel, thread, ok := splitSlackSurface(r)
		if !ok {
			continue
		}
		trigger := thread
		if p.SlackChan == channel && p.SlackThread == thread && p.SlackTS != "" {
			trigger = p.SlackTS
		}
		refs = append(refs, slackCoord(channel, thread, trigger))
	}
	return unique(refs)
}

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

// slackCoord is Slack's Ref shape (channel/thread/trigger) without importing that package:
// github is the owner of a review session, Slack is a door, and a cycle would reverse that.
func slackCoord(channel, thread, trigger string) string {
	return channel + "/" + thread + "/" + trigger
}

func splitSlackSurface(ref string) (channel, thread string, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func isScaffolding(text string) bool {
	switch {
	case text == conductor.Placeholder, text == doneText, text == failedText:
		return true
	case text == reviewingText, text == reviewedNote, text == reviewFailedText:
		return true
	case strings.Contains(text, statusMarker):
		return true
	case strings.HasPrefix(text, "⏳"), strings.HasPrefix(text, "👀"):
		return true
	default:
		return false
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func strconvInt(n int64) string {
	return fmt.Sprintf("%d", n)
}

func parseID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("github: %q is not a comment id", s)
	}
	return n, nil
}
