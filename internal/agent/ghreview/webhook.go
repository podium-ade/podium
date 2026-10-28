package ghreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// WebhookPath is the only route on the webhook mux. Funnel/a tunnel points here and
// nowhere else, so exposing it never exposes the AgentService.
const WebhookPath = "/webhooks/github"

// MaxWebhookBytes is the largest delivery we will read. GitHub's documented cap is 25 MB;
// a comment webhook is kilobytes. A megabyte is what stops a delivery filling memory
// without refusing a real comment.
const MaxWebhookBytes = 1 << 20

const (
	headerEvent     = "X-GitHub-Event"
	headerDelivery  = "X-GitHub-Delivery"
	headerSignature = "X-Hub-Signature-256"
)

// Handler is POST /webhooks/github: HMAC-verified deliveries, nothing else.
func (s *Source) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(WebhookPath, s.serveWebhook)
	return mux
}

func (s *Source) serveWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	event := r.Header.Get(headerEvent)
	delivery := r.Header.Get(headerDelivery)
	r.Body = http.MaxBytesReader(w, r.Body, MaxWebhookBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logWebhook(r.Context(), slog.LevelWarn, event, delivery, classified{}, "rejected", "payload too large")
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.webhookSecret, body, r.Header.Get(headerSignature)) {
		s.logWebhook(r.Context(), slog.LevelWarn, event, delivery, classified{}, "rejected", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	if event == "ping" {
		s.logWebhook(r.Context(), slog.LevelInfo, event, delivery, classified{}, "ok", "")
		w.WriteHeader(http.StatusOK)
		return
	}
	if delivery != "" && !s.firstSighting(delivery) {
		c, _ := s.classify(event, body)
		s.logWebhook(r.Context(), slog.LevelInfo, event, delivery, c, "duplicate", "")
		w.WriteHeader(http.StatusOK)
		return
	}

	s.handleDelivery(r.Context(), event, delivery, body)
	w.WriteHeader(http.StatusOK)
}

func validSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil || len(want) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

func (s *Source) handleDelivery(ctx context.Context, event, delivery string, body []byte) {
	c, ok := s.classify(event, body)
	if !ok {
		reason := c.reason
		if reason == "" {
			reason = "unhandled"
		}
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", reason)
		return
	}
	if s.isAppUser(c.senderLogin, c.senderType) {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "own delivery")
		return
	}
	f, err := s.currentFilters(ctx)
	if err != nil {
		// Without the filters an allowlist could not be honoured, so nothing starts.
		s.logger.WarnContext(ctx, "github: reading the webhook filters failed", "error", err)
		s.logWebhook(ctx, slog.LevelWarn, event, delivery, c, "ignored", "filters unavailable")
		return
	}
	if ok, why := f.actorAllowed(firstNonEmpty(c.author, c.senderLogin), c.senderType); !ok {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", why)
		return
	}
	if c.ready && f.NoAutoReview {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "auto review off")
		return
	}

	key := SourceKey(c.owner, c.repo, c.number)
	_, known := s.session(ctx, key)
	phrases := f.phraseRE()
	triggered := c.ready || c.mentioned || (phrases != nil && phrases.MatchString(c.body))
	if !triggered && c.inReply {
		triggered = s.replyTriggers(ctx, f, c, known)
	}
	if !triggered {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "not mentioned")
		return
	}

	text := s.stripMention(c.body)
	if phrases != nil {
		text = strings.TrimSpace(phrases.ReplaceAllString(text, "$1$2"))
	}
	switch {
	case c.ready && known:
		commit, head := s.reviewBaseline(ctx, c)
		text = followUpInstruction(pullInstruction(c.author, PRURL(c.owner, c.repo, c.number), c.action), commit, head)
	case c.ready:
		text = pullInstruction(c.author, PRURL(c.owner, c.repo, c.number), c.action)
	case !known:
		text = reviewInstruction(c.author, PRURL(c.owner, c.repo, c.number), text)
	case c.inReply:
		text = reviewReplyInstruction(c.path, c.line, c.diffHunk, text)
	default:
		commit, head := s.reviewBaseline(ctx, c)
		text = followUpInstruction(text, commit, head)
	}

	s.emit(ctx, conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  key,
		Ref: Ref(Parsed{
			Owner: c.owner, Repo: c.repo, Number: c.number, InstallID: c.installID,
			Kind: c.kind, CommentID: c.commentID,
		}),
		Author:    c.author,
		Text:      text,
		TS:        c.ts,
		URL:       PRURL(c.owner, c.repo, c.number),
		BriefKind: conductor.SourceGitHub,
	})
	s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "started", "")
}

// logWebhook records one delivery. The body stays out of the log: it is the
// comment or the diff, and the signature header is a secret.
func (s *Source) logWebhook(ctx context.Context, level slog.Level, event, delivery string, c classified, outcome, reason string) {
	attrs := []any{"event", event, "outcome", outcome}
	if delivery != "" {
		attrs = append(attrs, "delivery", delivery)
	}
	if c.action != "" {
		attrs = append(attrs, "action", c.action)
	}
	if c.owner != "" && c.repo != "" {
		attrs = append(attrs, "repo", c.owner+"/"+c.repo)
	}
	if c.number > 0 {
		attrs = append(attrs, "pr", c.number)
	}
	if c.senderLogin != "" {
		attrs = append(attrs, "sender", c.senderLogin)
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	s.logger.Log(ctx, level, "github: webhook", attrs...)
}

type classified struct {
	owner, repo string
	number      int
	installID   int64
	kind        string
	commentID   int64
	body        string
	author      string
	senderLogin string
	senderType  string
	ts          time.Time
	path        string
	line        int
	diffHunk    string
	inReply     bool
	// threadID is the first comment of the review thread a reply is in.
	threadID  int64
	mentioned bool
	// ready is a pull request opened already ready, or a draft marked ready.
	// GitHub cannot request this App as a reviewer, so those actions start the
	// review instead. A draft's own open stays quiet.
	ready  bool
	action string
	// reason is why classify rejected the delivery. Empty when it accepted it.
	reason string
}

func (s *Source) classify(event string, body []byte) (classified, bool) {
	var env struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return classified{}, false
	}
	base := classified{
		action:      env.Action,
		owner:       env.Repository.Owner.Login,
		repo:        env.Repository.Name,
		installID:   env.Installation.ID,
		senderLogin: env.Sender.Login,
		senderType:  env.Sender.Type,
	}
	if base.owner == "" || base.repo == "" {
		base.reason = "unhandled"
		return base, false
	}

	switch event {
	case "issue_comment":
		if env.Action != "created" {
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			Comment struct {
				ID        int64     `json:"id"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"created_at"`
				User      User      `json:"user"`
			} `json:"comment"`
			Issue struct {
				Number      int             `json:"number"`
				PullRequest json.RawMessage `json:"pull_request"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		if len(p.Issue.PullRequest) == 0 || string(p.Issue.PullRequest) == "null" {
			base.reason = "not a pull request"
			return base, false
		}
		base.number = p.Issue.Number
		base.kind = KindIssueComment
		base.commentID = p.Comment.ID
		base.body = p.Comment.Body
		base.author = firstNonEmpty(p.Comment.User.Login, env.Sender.Login)
		base.ts = p.Comment.CreatedAt
		base.mentioned = s.mentioned(p.Comment.Body)
		if base.number == 0 || base.commentID == 0 {
			base.reason = "unhandled"
			return base, false
		}
		return base, true

	case "pull_request_review_comment":
		if env.Action != "created" {
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			Comment struct {
				ID        int64     `json:"id"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"created_at"`
				Path      string    `json:"path"`
				Line      int       `json:"line"`
				DiffHunk  string    `json:"diff_hunk"`
				InReplyTo int64     `json:"in_reply_to_id"`
				User      User      `json:"user"`
			} `json:"comment"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		base.number = p.PullRequest.Number
		base.kind = KindReviewComment
		base.commentID = p.Comment.ID
		base.body = p.Comment.Body
		base.author = firstNonEmpty(p.Comment.User.Login, env.Sender.Login)
		base.ts = p.Comment.CreatedAt
		base.path = p.Comment.Path
		base.line = p.Comment.Line
		base.diffHunk = p.Comment.DiffHunk
		base.inReply = p.Comment.InReplyTo != 0
		base.threadID = p.Comment.InReplyTo
		base.mentioned = s.mentioned(p.Comment.Body)
		if base.number == 0 || base.commentID == 0 {
			base.reason = "unhandled"
			return base, false
		}
		return base, true

	case "pull_request_review":
		if env.Action != "submitted" {
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			Review struct {
				ID          int64     `json:"id"`
				Body        string    `json:"body"`
				SubmittedAt time.Time `json:"submitted_at"`
				User        User      `json:"user"`
			} `json:"review"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		base.number = p.PullRequest.Number
		base.kind = KindReview
		base.commentID = p.Review.ID
		base.body = p.Review.Body
		base.author = firstNonEmpty(p.Review.User.Login, env.Sender.Login)
		base.ts = p.Review.SubmittedAt
		base.mentioned = s.mentioned(p.Review.Body)
		if base.number == 0 || base.commentID == 0 {
			base.reason = "unhandled"
			return base, false
		}
		if strings.TrimSpace(base.body) == "" {
			base.reason = "empty review"
			return base, false
		}
		return base, true

	case "pull_request":
		// An open that is already ready, or a draft marked ready, starts the
		// review. review_requested is not one: GitHub does not let you assign
		// this App as a reviewer. A draft's own open waits for that mark.
		if env.Action != "ready_for_review" && env.Action != "opened" {
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			PullRequest struct {
				Number    int       `json:"number"`
				Draft     bool      `json:"draft"`
				UpdatedAt time.Time `json:"updated_at"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		if env.Action == "opened" && p.PullRequest.Draft {
			base.reason = "draft"
			base.number = p.PullRequest.Number
			return base, false
		}
		base.number = p.PullRequest.Number
		base.kind = KindPullRequest
		base.author = env.Sender.Login
		base.ts = p.PullRequest.UpdatedAt
		base.ready = true
		if base.number == 0 {
			base.reason = "unhandled"
			return base, false
		}
		return base, true
	default:
		base.reason = "unsupported event"
		return base, false
	}
}

func (s *Source) currentFilters(ctx context.Context) (Filters, error) {
	if s.filters == nil {
		return Filters{}, nil
	}
	return s.filters(ctx)
}

// replyTriggers decides a reply in an inline review thread that carries no trigger.
func (s *Source) replyTriggers(ctx context.Context, f Filters, c classified, known bool) bool {
	switch f.replyMode() {
	case ReplyAlways:
		return false
	case ReplyAnyThread:
		return known
	default:
		return s.botThread(ctx, c)
	}
}

// maxThreadBots bounds the thread cache. Forgetting it only costs one more read.
const maxThreadBots = 4096

// botThread reports whether the App started the thread c replies in. A thread that
// cannot be read counts as a person's: the reply then needs the trigger.
func (s *Source) botThread(ctx context.Context, c classified) bool {
	if c.threadID == 0 || c.installID == 0 {
		return false
	}
	s.mu.Lock()
	bot, ok := s.threadBots[c.threadID]
	s.mu.Unlock()
	if ok {
		return bot
	}
	root, err := s.client.ReviewComment(ctx, c.installID, c.owner, c.repo, c.threadID)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the start of a review thread failed",
			"pr", PRURL(c.owner, c.repo, c.number), "comment", c.threadID, "error", err)
		return false
	}
	bot = s.isAppUser(root.User.Login, root.User.Type)
	s.mu.Lock()
	if len(s.threadBots) >= maxThreadBots {
		clear(s.threadBots)
	}
	s.threadBots[c.threadID] = bot
	s.mu.Unlock()
	return bot
}

func (s *Source) mentioned(body string) bool {
	return s.mentionRE != nil && s.mentionRE.MatchString(body)
}

func (s *Source) stripMention(text string) string {
	if s.mentionRE == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(s.mentionRE.ReplaceAllString(text, "$1"))
}

func (s *Source) isAppUser(login, typ string) bool {
	if s.slug == "" {
		return false
	}
	want := s.slug + "[bot]"
	if strings.EqualFold(login, want) {
		return true
	}
	return strings.EqualFold(typ, "Bot") && strings.EqualFold(login, s.slug)
}

func compileMention(slug string) *regexp.Regexp {
	if slug == "" {
		return nil
	}
	// @slug or @slug[bot], not @slug-extra. The trailing group is kept so stripMention
	// can leave the following whitespace/punctuation in place via $1.
	return regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(slug) + `(?:\[bot\])?([^A-Za-z0-9_-]|$)`)
}

func pullInstruction(author, prURL, action string) string {
	who := firstNonEmpty(author, "someone")
	if action == "opened" {
		return who + " opened " + prURL + " for review"
	}
	return who + " marked " + prURL + " ready for review"
}

// followUpInstruction tells a later turn to review the delta since the last
// review. commit is the head that review was submitted against. head is the
// pull request head now. The workspace snapshot is already restored by the
// time the turn starts.
func followUpInstruction(note, commit, head string) string {
	var b strings.Builder
	unchanged := commit != "" && commit == head
	if unchanged {
		b.WriteString("This pull request was already reviewed, and its head is still ")
		b.WriteString(commit)
		b.WriteString(". There is no new diff. Repeat the previous verdict in one line and stop. Do not add inline comments.")
	} else {
		b.WriteString("This pull request was already reviewed. Review only the commits and files changed since that review. Do not review the rest of the pull request again. If that delta fixes the earlier findings and adds nothing that should block a merge, the verdict is **Approve**.")
		if commit != "" {
			b.WriteString("\nThe last review was on commit ")
			b.WriteString(commit)
			b.WriteString(".")
			if head != "" {
				b.WriteString(" The head is now ")
				b.WriteString(head)
				b.WriteString(".")
			}
		}
	}
	b.WriteString("\nThe workspace snapshot from the last turn is restored. Use it, including .podium/review.json when it is there.")
	note = strings.TrimSpace(note)
	if note != "" {
		b.WriteString("\n\n")
		b.WriteString(note)
	}
	return b.String()
}

// reviewBaseline is the commit the stored review covered and the pull request
// head now. Either is empty when GitHub did not return it. Both empty means
// this session has no review yet.
func (s *Source) reviewBaseline(ctx context.Context, c classified) (commit, head string) {
	id, err := s.reviewID(ctx, SourceKey(c.owner, c.repo, c.number))
	if err != nil || id == 0 || c.installID == 0 {
		return "", ""
	}
	reviews, err := s.client.ListReviews(ctx, c.installID, c.owner, c.repo, c.number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the last reviewed commit failed",
			"pr", PRURL(c.owner, c.repo, c.number), "error", err)
		return "", ""
	}
	for _, rev := range reviews {
		if rev.ID == id {
			commit = rev.CommitID
			break
		}
	}
	if commit == "" {
		return "", ""
	}
	info, err := s.client.Pull(ctx, c.installID, c.owner, c.repo, c.number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the pull request head failed",
			"pr", PRURL(c.owner, c.repo, c.number), "error", err)
		return commit, ""
	}
	return commit, info.SHA
}

func reviewInstruction(author, prURL, body string) string {
	head := firstNonEmpty(author, "someone") + " asked for a review of " + prURL
	body = strings.TrimSpace(body)
	if body == "" {
		return head
	}
	return head + "\n\n" + body
}

func reviewReplyInstruction(path string, line int, hunk, body string) string {
	loc := path
	if path != "" && line > 0 {
		loc = path + ":" + strconv.Itoa(line)
	}
	var b strings.Builder
	if loc != "" {
		b.WriteString("On ")
		b.WriteString(loc)
		b.WriteString(":\n")
	}
	if q := firstQuote(hunk); q != "" {
		b.WriteString("> ")
		b.WriteString(q)
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	return strings.TrimSpace(b.String())
}

func firstQuote(hunk string) string {
	for _, line := range strings.Split(hunk, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			return strings.TrimSpace(strings.TrimPrefix(line, "+"))
		}
	}
	return ""
}
