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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
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
	// statusMarker hides in the rendered comment and is how a later turn finds
	// the one status comment after a restart. The visible line is the title.
	statusMarker       = "<!-- podium-review-status -->"
	statusInProgress   = "Review in progress"
	statusApproved     = "Approved"
	statusChanges      = "Changes requested"
	statusReviewed     = "Reviewed"
	statusDidNotFinish = "Review did not finish"
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
	// Reviews remembers the pull-request review this session submitted. Nil keeps it in
	// memory for the life of the process, which is what the tests do.
	Reviews ReviewMemory
}

// ReviewMemory is the session's submitted review id. Get returns 0 when there is none.
type ReviewMemory struct {
	Get func(ctx context.Context, sourceKey string) (int64, error)
	Set func(ctx context.Context, sourceKey string, reviewID int64) error
}

// Source is the GitHub App integration.
type Source struct {
	client        *restClient
	logger        *slog.Logger
	events        chan conductor.InboundEvent
	listen        string
	webhookSecret string
	session       SessionLookup
	reviews       ReviewMemory
	surfaces      Surfaces
	slack         SlackBridge
	taskURL       func(string) string
	now           func() time.Time

	slug      string
	name      string
	mentionRE *regexp.Regexp

	mu   sync.Mutex
	seen map[string]time.Time
	// turns is per-ref bookkeeping for edits other than progress, keyed by the triggering ref.
	turns map[string]*turnState
	// reviewIDs is the submitted review for a pull request, keyed by source key. It is the
	// process cache of Reviews, so a second final in this process updates the review even
	// when the save has not landed yet.
	reviewIDs map[string]int64
	// statusIDs is the one pull-request comment that says the review is running
	// or finished. statusTitles is the title last written there, so a progress
	// line does not edit it again. pulls caches the summary those comments show.
	statusIDs    map[string]int64
	statusTitles map[string]string
	pulls        map[string]pullInfo
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
		webhookSecret: opts.WebhookSecret,
		session:       opts.Session,
		reviews:       opts.Reviews,
		surfaces:      opts.Surfaces,
		slack:         opts.Slack,
		taskURL:       taskURL,
		now:           now,
		seen:          map[string]time.Time{},
		turns:         map[string]*turnState{},
		reviewIDs:     map[string]int64{},
		statusIDs:     map[string]int64{},
		statusTitles:  map[string]string{},
		pulls:         map[string]pullInfo{},
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

	ln, err := net.Listen("tcp", s.listen)
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
	s.name = app.Name
	if s.name == "" {
		s.name = app.Slug
	}
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
	_, known := s.session(ctx, key)
	text := strings.TrimSpace(m.Text)
	if !known {
		text = reviewInstruction(m.Author, m.PR.URL, text)
	}
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
		Author:    m.Author,
		Text:      text,
		TS:        m.TS,
		URL:       m.PR.URL,
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

// Post says something new. A pull request gets two GitHub posts and no running
// commentary: one status comment, then the review. Progress is not posted and
// is not edited into that comment. Slack threads still hear the answer.
//
// A failure changes the status comment's title and is not itself a comment.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	if out.Type == conductor.OutFailure {
		s.showStatus(ctx, p, statusDidNotFinish, false)
		if s.fanoutSlack(out) {
			s.postSlack(ctx, p, out)
		}
		return "", nil
	}
	inline := p.Kind == KindReviewComment && p.CommentID > 0
	if out.Type == conductor.OutProgress {
		if inline {
			return "", nil
		}
		id := s.showStatus(ctx, p, statusInProgress, true)
		return id, nil
	}
	body := out.Text

	// The answer is one submitted review under the status comment. A later answer
	// updates it. A reply in an inline thread stays in that thread, except an
	// approval: replying "approved" on the notes does not approve the pull request,
	// so that verdict is still submitted as a review.
	if out.Type == conductor.OutFinal && (!inline || reviewEvent(body) == eventApprove) {
		event := s.reviewAction(ctx, p, body)
		s.showStatus(ctx, p, statusTitle(event), true)
		id, gerr := s.postReview(ctx, p, body)
		if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
			s.logger.WarnContext(ctx, "github: posting the review failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "error", gerr)
		}
		if s.fanoutSlack(out) {
			s.postSlack(ctx, p, out)
		}
		if id != "" {
			return id, nil
		}
		if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
			return "", gerr
		}
		return "", nil
	}
	if out.Type == conductor.OutQuestion {
		if s.fanoutSlack(out) {
			s.postSlack(ctx, p, out)
		}
		return "", nil
	}

	id, gerr := s.postGitHub(ctx, p, body)
	if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
		s.logger.WarnContext(ctx, "github: posting a comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", gerr)
	}
	if s.fanoutSlack(out) {
		s.postSlack(ctx, p, out)
	}
	if id != "" {
		return id, nil
	}
	if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
		return "", gerr
	}
	return "", nil
}

func (s *Source) postReview(ctx context.Context, p Parsed, body string) (string, error) {
	install, err := s.installation(ctx, p)
	if err != nil {
		return "", err
	}
	key := SourceKey(p.Owner, p.Repo, p.Number)
	id, err := s.reviewID(ctx, key)
	if err != nil {
		return "", err
	}
	summary, notes := splitInline(body)
	event := s.reviewAction(ctx, p, body)
	state := ""
	if id > 0 {
		state = s.reviewState(ctx, install, p, id)
	}
	// Updating a review replaces its summary. It cannot change the event or add
	// inline comments, and a comment left on top of an approval keeps the pull
	// request approved. This App cannot dismiss a review, so a follow-up that no
	// longer approves has to request changes. That is the event that replaces it.
	approvesAgain := event == eventApprove && state == "APPROVED"
	commentsAgain := event == eventComment && state != "APPROVED"
	if id > 0 && len(notes) == 0 && (approvesAgain || commentsAgain) {
		if err := s.client.UpdateReview(ctx, install, p.Owner, p.Repo, p.Number, id, summary); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	}
	created, err := s.submitReview(ctx, install, p, event, summary, notes)
	if err != nil {
		return "", err
	}
	if created.ID <= 0 {
		return "", errors.New("github: the review was submitted without an id")
	}
	s.replaceReview(key, created.ID)
	if s.reviews.Set != nil {
		if err := s.reviews.Set(ctx, key, created.ID); err != nil {
			s.logger.WarnContext(ctx, "github: recording the review id failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "review", created.ID, "error", err)
		}
	}
	return strconv.FormatInt(created.ID, 10), nil
}

// reviewAction is the GitHub event for this answer. An approval that a later
// answer no longer stands behind becomes a request for changes.
func (s *Source) reviewAction(ctx context.Context, p Parsed, body string) string {
	event := reviewEvent(body)
	if event == eventApprove {
		return event
	}
	id, err := s.reviewID(ctx, SourceKey(p.Owner, p.Repo, p.Number))
	if err != nil || id == 0 {
		return event
	}
	install, err := s.installation(ctx, p)
	if err != nil {
		return event
	}
	if s.reviewState(ctx, install, p, id) == "APPROVED" {
		return eventRequestChanges
	}
	return event
}

func statusTitle(event string) string {
	switch event {
	case eventApprove:
		return statusApproved
	case eventRequestChanges:
		return statusChanges
	default:
		return statusReviewed
	}
}

// showStatus writes the one pull-request comment. create posts it when this
// pull request does not have one yet. A failure does not create one: there is
// nothing to retitle, and a new comment would only say that Podium stopped.
// The returned id is empty when no comment was written.
func (s *Source) showStatus(ctx context.Context, p Parsed, title string, create bool) string {
	key := SourceKey(p.Owner, p.Repo, p.Number)
	id := s.knownStatus(ctx, p)
	if id == 0 && !create {
		return ""
	}
	if id > 0 {
		s.mu.Lock()
		current := s.statusTitles[key]
		s.mu.Unlock()
		if current == title {
			return strconv.FormatInt(id, 10)
		}
	}
	info := s.pullInfo(ctx, p)
	body := statusComment(s.name, title, p.Owner, p.Repo, info)
	if id == 0 {
		install, err := s.installation(ctx, p)
		if err != nil {
			if !errors.Is(err, ErrNotInstalled) {
				s.logger.WarnContext(ctx, "github: posting the status comment failed",
					"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
			}
			return ""
		}
		c, err := s.client.CreateIssueComment(ctx, install, p.Owner, p.Repo, p.Number, body)
		if err != nil {
			s.logger.WarnContext(ctx, "github: posting the status comment failed",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
			return ""
		}
		s.rememberStatus(key, c.ID, title)
		return strconv.FormatInt(c.ID, 10)
	}
	install, err := s.installation(ctx, p)
	if err != nil {
		return strconv.FormatInt(id, 10)
	}
	if err := s.client.EditIssueComment(ctx, install, p.Owner, p.Repo, id, body); err != nil {
		s.logger.WarnContext(ctx, "github: updating the status comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		return strconv.FormatInt(id, 10)
	}
	s.mu.Lock()
	s.statusTitles[key] = title
	s.mu.Unlock()
	return strconv.FormatInt(id, 10)
}

func (s *Source) knownStatus(ctx context.Context, p Parsed) int64 {
	key := SourceKey(p.Owner, p.Repo, p.Number)
	s.mu.Lock()
	id := s.statusIDs[key]
	s.mu.Unlock()
	if id > 0 {
		return id
	}
	install, err := s.installation(ctx, p)
	if err != nil {
		return 0
	}
	comments, err := s.client.ListIssueComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the status comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		return 0
	}
	for _, c := range comments {
		if strings.Contains(c.Body, statusMarker) {
			s.rememberStatus(key, c.ID, "")
			return c.ID
		}
	}
	return 0
}

func (s *Source) rememberStatus(key string, id int64, title string) {
	if id <= 0 {
		return
	}
	s.mu.Lock()
	s.statusIDs[key] = id
	if title != "" {
		s.statusTitles[key] = title
	}
	s.mu.Unlock()
}

// pullInfo reads the pull request for the status comment. A failed read keeps the
// summary already shown, so a later title change does not wipe it.
func (s *Source) pullInfo(ctx context.Context, p Parsed) pullInfo {
	key := SourceKey(p.Owner, p.Repo, p.Number)
	install, err := s.installation(ctx, p)
	if err != nil {
		return s.cachedPull(key)
	}
	info, err := s.client.Pull(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the pull request summary failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		return s.cachedPull(key)
	}
	info.OK = true
	s.mu.Lock()
	s.pulls[key] = info
	s.mu.Unlock()
	return info
}

func (s *Source) cachedPull(key string) pullInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pulls[key]
}

// statusComment is the one conversation comment. The title and the lead change
// when the review finishes. The pull-request summary under them is rewritten
// from the same fields, so the card stays one comment. name is the App's display
// name from GET /app: the comment is posted as that App, so it speaks as it.
func statusComment(name, title, owner, repo string, info pullInfo) string {
	alert, lead := statusLead(name, title)
	var b strings.Builder
	b.WriteString(statusMarker)
	b.WriteString("\n\n> [!")
	b.WriteString(alert)
	b.WriteString("]\n> **")
	b.WriteString(title)
	b.WriteString("**\n>\n> ")
	b.WriteString(lead)
	if !info.OK {
		return b.String()
	}
	if info.Title != "" {
		b.WriteString("\n\n### ")
		b.WriteString(oneLine(info.Title))
	}
	if line := branchLine(owner, repo, info); line != "" {
		b.WriteString("\n\n")
		b.WriteString(line)
	}
	b.WriteString("\n\n")
	b.WriteString(statTable(info))
	if href := compareURL(owner, repo, info.Base, info.Head); href != "" {
		b.WriteString("\n\n[View the diff](")
		b.WriteString(href)
		b.WriteString(")")
	}
	return b.String()
}

// statusLead is the GitHub alert and the one sentence under the title.
// NOTE is blue and labeled Note, WARNING is amber and labeled Warning, CAUTION
// is red and labeled Caution. The green alert is labeled Tip, so an approval
// stays a Note. The review under the comment is what says Approved.
func statusLead(name, title string) (alert, lead string) {
	switch title {
	case statusInProgress:
		return "NOTE", name + " is reading this pull request. The review will be posted below."
	case statusApproved:
		return "NOTE", "Approved. The full review is below."
	case statusChanges:
		return "WARNING", "Changes requested. The full review is below."
	case statusDidNotFinish:
		return "CAUTION", "This review stopped before it was posted."
	default:
		return "NOTE", "Reviewed. The full review is below."
	}
}

func branchLine(owner, repo string, info pullInfo) string {
	var parts []string
	if info.Head != "" || info.Base != "" {
		head := linkedCode(info.Head, treeURL(owner, repo, info.Head))
		base := linkedCode(info.Base, treeURL(owner, repo, info.Base))
		parts = append(parts, head+" → "+base)
	}
	if info.SHA != "" {
		short := info.SHA
		if len(short) > 7 {
			short = short[:7]
		}
		parts = append(parts, linkedCode(short, commitURL(owner, repo, info.SHA)))
	}
	return strings.Join(parts, " · ")
}

func statTable(info pullInfo) string {
	return "| Files | Additions | Deletions | Commits |\n" +
		"| ---: | ---: | ---: | ---: |\n" +
		"| " + groupedInt(info.Files) +
		" | +" + groupedInt(info.Additions) +
		" | −" + groupedInt(info.Deletions) +
		" | " + groupedInt(info.Commits) + " |"
}

func treeURL(owner, repo, ref string) string {
	if owner == "" || repo == "" || ref == "" {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo + "/tree/" + escapeRef(ref)
}

func commitURL(owner, repo, sha string) string {
	if owner == "" || repo == "" || sha == "" {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo + "/commit/" + url.PathEscape(sha)
}

func compareURL(owner, repo, base, head string) string {
	if owner == "" || repo == "" || base == "" || head == "" {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo + "/compare/" + escapeRef(base) + "..." + escapeRef(head)
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func linkedCode(label, href string) string {
	span := codeSpan(label)
	if href == "" {
		return span
	}
	return "[" + span + "](" + href + ")"
}

func codeSpan(s string) string {
	fence := "`"
	if strings.Contains(s, "`") {
		fence = "``"
	}
	return fence + s + fence
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func groupedInt(n int) string {
	sign := ""
	if n < 0 {
		sign = "−"
		n = -n
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	return sign + b.String()
}

// reviewState is the GitHub state of the review this session already submitted.
// Empty means the list failed or the id was not in it. A later approval is then
// submitted again rather than written into a review that might not be one.
func (s *Source) reviewState(ctx context.Context, install int64, p Parsed, id int64) string {
	reviews, err := s.client.ListReviews(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the stored review failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "review", id, "error", err)
		return ""
	}
	for _, rev := range reviews {
		if rev.ID == id {
			return rev.State
		}
	}
	return ""
}

// submitReview posts one review. Inline notes ride on it. GitHub rejects the
// whole submission when a note is not on the diff, and the review is then
// posted again with those notes in the summary so the approval still lands.
func (s *Source) submitReview(ctx context.Context, install int64, p Parsed, event, summary string, notes []inlineNote) (Review, error) {
	comments := commentsOf(notes)
	commit := ""
	if len(comments) > 0 {
		sha, err := s.client.PullHeadSHA(ctx, install, p.Owner, p.Repo, p.Number)
		if err != nil {
			s.logger.WarnContext(ctx, "github: no head commit for inline comments; leaving them in the summary",
				"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
			comments = nil
			summary = summaryWithNotes(summary, notes)
		} else {
			commit = sha
		}
	}
	created, err := s.client.CreateReview(ctx, install, p.Owner, p.Repo, p.Number, event, summary, commit, comments)
	// GitHub refuses the whole review when one note is not on the diff. That
	// refusal is an API error. A truncated body or a dropped connection is not,
	// and submitting again would post a second review for a failure that was
	// not about the notes.
	var refused *APIError
	if err != nil && len(comments) > 0 && errors.As(err, &refused) {
		s.logger.WarnContext(ctx, "github: inline comments rejected; submitting the review without them",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", err)
		created, err = s.client.CreateReview(ctx, install, p.Owner, p.Repo, p.Number, event, summaryWithNotes(summary, notes), "", nil)
	}
	return created, err
}

func (s *Source) reviewID(ctx context.Context, key string) (int64, error) {
	s.mu.Lock()
	id := s.reviewIDs[key]
	s.mu.Unlock()
	if id > 0 || s.reviews.Get == nil {
		return id, nil
	}
	id, err := s.reviews.Get(ctx, key)
	if err != nil || id <= 0 {
		return 0, err
	}
	s.rememberReview(key, id)
	return id, nil
}

func (s *Source) rememberReview(key string, id int64) {
	s.mu.Lock()
	if s.reviewIDs[key] == 0 {
		s.reviewIDs[key] = id
	}
	s.mu.Unlock()
}

// replaceReview points the session at the review a follow-up should edit.
func (s *Source) replaceReview(key string, id int64) {
	s.mu.Lock()
	s.reviewIDs[key] = id
	s.mu.Unlock()
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

func (s *Source) postSlack(ctx context.Context, p Parsed, out conductor.Outbound) {
	if s.slack == nil {
		return
	}
	for _, ref := range s.slackRefs(ctx, p) {
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

// React sets the Slack reaction on every bound thread. GitHub does not get a
// reaction comment: the status comment's title is the whole of that signal,
// and it is written when the turn starts and when the answer or the failure lands.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	switch kind {
	case conductor.ReactionWorking, conductor.ReactionDone:
	case conductor.ReactionAwaiting:
		return nil
	case conductor.ReactionFailed:
		s.showStatus(ctx, p, statusDidNotFinish, false)
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
