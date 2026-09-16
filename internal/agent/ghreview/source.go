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
	eventBuffer  = 32
	dedupeTTL    = 5 * time.Minute
	shutdownWait = 5 * time.Second
	doneText     = "✅ Done — see below"
	failedText   = "❌ Failed"
	EditThrottle = 2 * time.Second
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

// Options configures the source.
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
}

// Source is the GitHub App integration.
type Source struct {
	client        *restClient
	logger        *slog.Logger
	events        chan conductor.InboundEvent
	listen        string
	webhookSecret string
	session       SessionLookup
	surfaces      Surfaces
	slack         SlackBridge
	taskURL       func(string) string
	now           func() time.Time

	slug      string
	mentionRE *regexp.Regexp

	mu   sync.Mutex
	seen map[string]time.Time
	// turns is per-ref bookkeeping for the working comment, keyed by the triggering ref.
	turns map[string]*turnState
}

type turnState struct {
	working  string
	held     string
	haveHeld bool
	lastEdit time.Time
	taskID   string
}

var _ conductor.Source = (*Source)(nil)

// New builds the source. Nothing is dialled until Run. The webhook listener is bound in Run.
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
		surfaces:      opts.Surfaces,
		slack:         opts.Slack,
		taskURL:       taskURL,
		now:           now,
		seen:          map[string]time.Time{},
		turns:         map[string]*turnState{},
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

// Post says something new. Progress (including the placeholder) is a GitHub comment that
// later edits replace. Finals, failures and questions also go to every bound Slack thread.
func (s *Source) Post(ctx context.Context, ref string, out conductor.Outbound) (string, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	body := out.Text
	if out.Type == conductor.OutFinal || out.Type == conductor.OutFailure {
		body = withFooter(body, out.TaskID)
	}

	id, gerr := s.postGitHub(ctx, p, body)
	if gerr != nil && !errors.Is(gerr, ErrNotInstalled) {
		s.logger.WarnContext(ctx, "github: posting a comment failed",
			"pr", PRURL(p.Owner, p.Repo, p.Number), "error", gerr)
	}

	st := s.state(ref)
	s.mu.Lock()
	if out.Type == conductor.OutProgress && st.working == "" && id != "" {
		st.working = id
		st.lastEdit = s.now()
	}
	if out.TaskID != "" {
		st.taskID = out.TaskID
	}
	s.mu.Unlock()

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

// Edit replaces a GitHub comment this source posted. Slack does not carry progress.
func (s *Source) Edit(ctx context.Context, ref, msgID string, out conductor.Outbound) error {
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
	if s.now().Sub(st.lastEdit) < EditThrottle {
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

// React rewrites the GitHub working comment on done/failed, and sets the Slack reaction
// on every bound thread.
func (s *Source) React(ctx context.Context, ref string, kind conductor.Reaction) error {
	p, err := ParseRef(ref)
	if err != nil {
		return err
	}
	switch kind {
	case conductor.ReactionWorking:
		// The placeholder comment is the GitHub ack; Slack gets ⏳ on the mention.
	case conductor.ReactionDone:
		_ = s.outcome(ctx, ref, doneText)
	case conductor.ReactionFailed:
		_ = s.outcome(ctx, ref, failedText)
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

func (s *Source) outcome(ctx context.Context, ref, text string) error {
	st := s.state(ref)
	s.mu.Lock()
	id := st.working
	s.mu.Unlock()
	if id == "" {
		return nil
	}
	return s.Edit(ctx, ref, id, conductor.Outbound{Type: conductor.OutProgress, Text: text})
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
	case strings.HasPrefix(text, "⏳"), strings.HasPrefix(text, "👀"):
		return true
	default:
		return false
	}
}

func withFooter(text, taskID string) string {
	if taskID == "" {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n_Podium task " + taskID + "_"
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
