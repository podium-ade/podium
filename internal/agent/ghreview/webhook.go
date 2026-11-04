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
	"github.com/podium-ade/podium/internal/agent/profiles"
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
	c.delivery = delivery
	if !ok {
		reason := c.reason
		if reason == "" {
			reason = "unhandled"
		}
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", reason)
		return
	}
	if c.pushed {
		s.handlePush(ctx, event, delivery, c)
		return
	}
	if s.isAppUser(c.senderLogin, c.senderType) {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "own delivery")
		return
	}
	if c.change != "" {
		// An edit or a delete updates what was recorded. It is never a turn, whoever made
		// it and whatever it now says (decision 34), so no filter applies.
		s.emit(ctx, s.inbound(c, c.body))
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "recorded", c.change)
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
		// The filters decide what starts a turn, not what was said (phase 2): what a filtered
		// actor wrote on a pull request Podium holds is still part of its record.
		s.recordOnly(ctx, c)
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
	// A review command is its own trigger: `/review` names no mention, and it is the one
	// request somebody outside Podium may make, so it must reach the conductor's gate.
	cmd, isCommand := ParseReviewCommand(c.body, s.mentionRE)
	if c.ready {
		isCommand = false
	}
	triggered := c.ready || c.mentioned || isCommand || (phrases != nil && phrases.MatchString(c.body))
	if !triggered && c.inReply {
		triggered = s.replyTriggers(ctx, f, c, known)
	}
	if !triggered && known && f.ConversationReplies && c.kind == KindIssueComment {
		triggered = s.answersTheApp(ctx, c)
	}
	if !triggered {
		s.recordOnly(ctx, c)
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "not mentioned")
		return
	}

	if len(f.ExcludedBranches) > 0 {
		if c.base == "" && c.installID != 0 {
			// An issue_comment does not carry the base branch.
			info, err := s.client.Pull(ctx, c.installID, c.owner, c.repo, c.number)
			if err != nil {
				s.logger.WarnContext(ctx, "github: reading the pull request base branch failed",
					"pr", PRURL(c.owner, c.repo, c.number), "error", err)
			}
			c.base = info.Base
		}
		if c.base == "" {
			// Without the base an exclusion could not be honoured, so nothing starts.
			s.recordOnly(ctx, c)
			s.logWebhook(ctx, slog.LevelWarn, event, delivery, c, "ignored", "base branch unavailable")
			return
		}
		if f.branchExcluded(c.base) {
			s.recordOnly(ctx, c)
			s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "excluded branch")
			return
		}
	}

	text := s.stripMention(c.body)
	if phrases != nil {
		text = strings.TrimSpace(phrases.ReplaceAllString(text, "$1$2"))
	}
	// A pull request opened or marked ready is the review playbook's, and nobody asked
	// anything: the conductor runs it directly. Anything a person wrote is the assistant's,
	// told who wrote it where, and where the last review stopped.
	commit, head := "", ""
	if known {
		commit, head = s.reviewBaseline(ctx, c)
	}
	if !c.ready {
		c.role, c.locked = s.speakerRole(ctx, c)
	}
	if c.ready {
		text = pullInstruction(c.author, PRURL(c.owner, c.repo, c.number), c.action)
		if known {
			text = followUpInstruction(text, commit, head)
		}
	} else {
		text = commentInstruction(c, text, commit, head)
	}
	request := ""
	if isCommand {
		request = s.reviewRequest(ctx, c, cmd, commit, head)
	}
	ev := s.inbound(c, text)
	ev.Mentioned = c.mentioned || isCommand
	ev.ReviewRequest = request
	ev.Automatic = c.ready
	ev.Role = c.role
	if c.ready {
		// Nobody said anything: the record keeps the instruction, which says what happened.
		ev.RecordText = ""
	}
	s.emit(ctx, ev)
	s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "started", "")
}

// inbound is the event for one classified delivery, with text as what the turn is handed.
// The record keeps what the person wrote (RecordText), not the instruction built around it.
func (s *Source) inbound(c classified, text string) conductor.InboundEvent {
	speakerID := ""
	if c.authorID != 0 {
		speakerID = strconv.FormatInt(c.authorID, 10)
	}
	ev := conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  SourceKey(c.owner, c.repo, c.number),
		Ref: Ref(Parsed{
			Owner: c.owner, Repo: c.repo, Number: c.number, InstallID: c.installID,
			Kind: c.kind, CommentID: c.commentID,
		}),
		Author:          c.author,
		SpeakerProvider: conductor.SpeakerGitHub,
		SpeakerID:       speakerID,
		SpeakerAvatar:   c.authorAvatar,
		SpeakerHandle:   c.author,
		ExternalID:      c.externalID(),
		ExternalURL:     c.htmlURL,
		RecordText:      strings.TrimSpace(c.body),
		Change:          c.change,
		Text:            text,
		TS:              c.ts,
		URL:             PRURL(c.owner, c.repo, c.number),
		BriefKind:       conductor.SourceGitHub,
	}
	if c.change != "" {
		// An edit's text is the comment's new body. A change carries no instruction.
		ev.Text = strings.TrimSpace(c.body)
	}
	return ev
}

// recordOnly offers what a person wrote on a pull request, which starts nothing, as part of
// its record. The conductor keeps it only when Podium already holds that pull request.
func (s *Source) recordOnly(ctx context.Context, c classified) {
	if c.ready || c.commentID == 0 || strings.TrimSpace(c.body) == "" {
		return
	}
	ev := s.inbound(c, strings.TrimSpace(c.body))
	ev.RecordOnly = true
	s.emit(ctx, ev)
}

// reviewRequest is the review a commenter outside Podium is allowed to have: the fixed
// instruction, with their note only if it survives sanitizing and the note checker. A note
// that does not is dropped and the review still runs — refusing the review would make the
// checker an oracle for what it catches.
func (s *Source) reviewRequest(ctx context.Context, c classified, cmd ReviewCommand, commit, head string) string {
	note := SanitizeReviewNote(cmd.Note)
	if note != "" {
		ok, err := s.noteChecker.Allowed(ctx, note)
		if err != nil || !ok {
			s.logger.InfoContext(ctx, "github: dropped the note on a review command",
				"pr", PRURL(c.owner, c.repo, c.number), "sender", c.author, "error", err)
			note = ""
		}
	}
	return reviewRequestInstruction(c.author, PRURL(c.owner, c.repo, c.number), note, commit, head)
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
	if c.base != "" {
		attrs = append(attrs, "base", c.base)
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
	// base is the branch the pull request merges into. An issue_comment leaves it empty.
	base string
	// pushed is new commits on the pull request's head branch (synchronize).
	pushed bool
	// reason is why classify rejected the delivery. Empty when it accepted it.
	reason string
	// role is the author's role on the repository, which decides the playbooks the turn is
	// offered. locked is a fork nobody here can push to; role is capped below write for it.
	role   string
	locked bool
	// authorID is the numeric GitHub id of who wrote the comment, review or pull request
	// event, and authorAvatar their picture: the account the conductor resolves to a Podium
	// login. Zero when GitHub did not say.
	authorID     int64
	authorAvatar string
	// change is an edit (conductor.ChangeEdited) or a delete (conductor.ChangeDeleted) of
	// the comment or review commentID names, and empty for something newly said.
	change string
	// htmlURL is the comment's or review's own link on GitHub.
	htmlURL string
	// delivery is the webhook's X-GitHub-Delivery id, which a redelivery keeps.
	delivery string
}

// externalID is the message's identity in the pull request's record. Comments, review
// comments and reviews are numbered independently, so the kind goes in front. An event that
// is not a comment (a pull request opened or marked ready) is named by its delivery, which
// is what a redelivery of it repeats.
func (c classified) externalID() string {
	if c.commentID != 0 {
		return c.kind + ":" + strconv.FormatInt(c.commentID, 10)
	}
	if c.delivery != "" {
		return "delivery:" + c.delivery
	}
	return ""
}

// speaker records the account a comment or review came from, when the payload carries it.
func (c *classified) speaker(u User) {
	if u.ID != 0 {
		c.authorID = u.ID
		c.authorAvatar = u.AvatarURL
	}
}

// commentAction accepts what a comment can do: be created, edited or deleted. An edit and a
// delete become a change of the recorded message; they never start anything.
func (c *classified) commentAction() bool {
	switch c.action {
	case "created":
	case "edited":
		c.change = conductor.ChangeEdited
	case "deleted":
		c.change = conductor.ChangeDeleted
	default:
		c.reason = "unsupported action"
		return false
	}
	return true
}

// changedAt is when a comment event happened: its creation, or its last update for a change.
// Zero for a change GitHub did not stamp, which the conductor reads as now.
func changedAt(change string, created, updated time.Time) time.Time {
	if change == "" {
		return created
	}
	return updated
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
		Sender User `json:"sender"`
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
		// Who spoke, by the numeric id the identities table proves. The sender is right for
		// a pull request event; a comment or review overrides it with its own user below.
		authorID:     env.Sender.ID,
		authorAvatar: env.Sender.AvatarURL,
	}
	if base.owner == "" || base.repo == "" {
		base.reason = "unhandled"
		return base, false
	}

	switch event {
	case "issue_comment":
		if !base.commentAction() {
			return base, false
		}
		var p struct {
			Comment struct {
				ID        int64     `json:"id"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"created_at"`
				UpdatedAt time.Time `json:"updated_at"`
				HTMLURL   string    `json:"html_url"`
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
		base.speaker(p.Comment.User)
		base.ts = changedAt(base.change, p.Comment.CreatedAt, p.Comment.UpdatedAt)
		base.htmlURL = p.Comment.HTMLURL
		base.mentioned = s.mentioned(p.Comment.Body)
		if base.number == 0 || base.commentID == 0 {
			base.reason = "unhandled"
			return base, false
		}
		return base, true

	case "pull_request_review_comment":
		if !base.commentAction() {
			return base, false
		}
		var p struct {
			Comment struct {
				ID        int64     `json:"id"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"created_at"`
				UpdatedAt time.Time `json:"updated_at"`
				HTMLURL   string    `json:"html_url"`
				Path      string    `json:"path"`
				Line      int       `json:"line"`
				DiffHunk  string    `json:"diff_hunk"`
				InReplyTo int64     `json:"in_reply_to_id"`
				User      User      `json:"user"`
			} `json:"comment"`
			PullRequest struct {
				Number int        `json:"number"`
				Base   branchJSON `json:"base"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		base.number = p.PullRequest.Number
		base.base = p.PullRequest.Base.Ref
		base.kind = KindReviewComment
		base.commentID = p.Comment.ID
		base.body = p.Comment.Body
		base.author = firstNonEmpty(p.Comment.User.Login, env.Sender.Login)
		base.speaker(p.Comment.User)
		base.ts = changedAt(base.change, p.Comment.CreatedAt, p.Comment.UpdatedAt)
		base.htmlURL = p.Comment.HTMLURL
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
		// A review has no delete; its body can be edited.
		switch env.Action {
		case "submitted":
		case "edited":
			base.change = conductor.ChangeEdited
		default:
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			Review struct {
				ID          int64     `json:"id"`
				Body        string    `json:"body"`
				SubmittedAt time.Time `json:"submitted_at"`
				HTMLURL     string    `json:"html_url"`
				User        User      `json:"user"`
			} `json:"review"`
			PullRequest struct {
				Number int        `json:"number"`
				Base   branchJSON `json:"base"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return base, false
		}
		base.number = p.PullRequest.Number
		base.base = p.PullRequest.Base.Ref
		base.kind = KindReview
		base.commentID = p.Review.ID
		base.body = p.Review.Body
		base.author = firstNonEmpty(p.Review.User.Login, env.Sender.Login)
		base.speaker(p.Review.User)
		base.ts = p.Review.SubmittedAt
		if base.change != "" {
			base.ts = time.Time{}
		}
		base.htmlURL = p.Review.HTMLURL
		base.mentioned = s.mentioned(p.Review.Body)
		if base.number == 0 || base.commentID == 0 {
			base.reason = "unhandled"
			return base, false
		}
		if strings.TrimSpace(base.body) == "" && base.change == "" {
			base.reason = "empty review"
			return base, false
		}
		return base, true

	case "pull_request":
		// An open that is already ready, or a draft marked ready, starts the
		// review. review_requested is not one: GitHub does not let you assign
		// this App as a reviewer. A draft's own open waits for that mark. New
		// commits are handlePush's to decide.
		if env.Action != "ready_for_review" && env.Action != "opened" && env.Action != "synchronize" {
			base.reason = "unsupported action"
			return base, false
		}
		var p struct {
			PullRequest struct {
				Number    int        `json:"number"`
				Draft     bool       `json:"draft"`
				UpdatedAt time.Time  `json:"updated_at"`
				Base      branchJSON `json:"base"`
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
		base.base = p.PullRequest.Base.Ref
		base.kind = KindPullRequest
		base.author = env.Sender.Login
		base.ts = p.PullRequest.UpdatedAt
		base.ready = env.Action != "synchronize"
		base.pushed = env.Action == "synchronize"
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

type branchJSON struct {
	Ref string `json:"ref"`
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

// roleTTL is how long a person's role on a repository is remembered. A role taken away is
// honoured within it; a delivery burst costs one read.
const roleTTL = 5 * time.Minute

type knownRole struct {
	role string
	at   time.Time
}

// speakerRole is the author's role on the pull request's repository, capped below write when
// the pull request is a fork nobody here can push to. A role that cannot be read is no role:
// the turn is then offered only what anybody may ask for.
func (s *Source) speakerRole(ctx context.Context, c classified) (role string, locked bool) {
	if c.installID == 0 || c.author == "" {
		return "", false
	}
	key := strings.ToLower(c.owner + "/" + c.repo + "/" + c.author)
	s.mu.Lock()
	seen, ok := s.roles[key]
	s.mu.Unlock()
	if ok && s.now().Sub(seen.at) < roleTTL {
		role = seen.role
	} else {
		r, err := s.client.Role(ctx, c.installID, c.owner, c.repo, c.author)
		if err != nil {
			s.logger.WarnContext(ctx, "github: reading the commenter's role failed",
				"pr", PRURL(c.owner, c.repo, c.number), "login", c.author, "error", err)
			return "", false
		}
		role = r
		s.mu.Lock()
		if len(s.roles) >= maxThreadBots {
			clear(s.roles)
		}
		s.roles[key] = knownRole{role: role, at: s.now()}
		s.mu.Unlock()
	}
	if !profiles.GitHubRoleAtLeast(role, "write") {
		return role, false
	}
	info, err := s.client.Pull(ctx, c.installID, c.owner, c.repo, c.number)
	if err != nil {
		// Whether a fork can be pushed to is unknown, so it is treated as one that cannot.
		s.logger.WarnContext(ctx, "github: reading whether the pull request can be pushed to failed",
			"pr", PRURL(c.owner, c.repo, c.number), "error", err)
		return "triage", false
	}
	if info.Locked {
		return "triage", true
	}
	return role, false
}

// maxThreadBots bounds the thread cache. Forgetting it only costs one more read.
const maxThreadBots = 4096

// botThread reports whether the App started the thread c replies in, or has replied in it.
// A thread that cannot be read counts as a person's: the reply then needs the trigger.
func (s *Source) botThread(ctx context.Context, c classified) bool {
	if c.threadID == 0 || c.installID == 0 {
		return false
	}
	s.mu.Lock()
	bot, ok := s.threadBots[c.threadID]
	s.mu.Unlock()
	if ok {
		return bot || s.joinedThread(ctx, c)
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
	return bot || s.joinedThread(ctx, c)
}

// joinedThread reports whether the App has replied in the thread c replies in, though a
// person started it. Only a yes is remembered: the App can join a thread later.
func (s *Source) joinedThread(ctx context.Context, c classified) bool {
	s.mu.Lock()
	joined := s.threadJoined[c.threadID]
	s.mu.Unlock()
	if joined {
		return true
	}
	comments, err := s.client.ListReviewComments(ctx, c.installID, c.owner, c.repo, c.number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading a review thread's replies failed",
			"pr", PRURL(c.owner, c.repo, c.number), "comment", c.threadID, "error", err)
		return false
	}
	for _, cm := range comments {
		if cm.InReplyTo == c.threadID && s.isAppUser(cm.User.Login, cm.User.Type) {
			s.mu.Lock()
			if len(s.threadJoined) >= maxThreadBots {
				clear(s.threadJoined)
			}
			s.threadJoined[c.threadID] = true
			s.mu.Unlock()
			return true
		}
	}
	return false
}

// conversationReplyWindow is how soon after the App's own comment a conversation-tab
// comment counts as the answer to it.
const conversationReplyWindow = 30 * time.Minute

// answersTheApp reports whether the conversation-tab comment c follows the App's own comment
// directly, within conversationReplyWindow of it being written or last edited. The tab has no
// threads, so "right after the App spoke" is the nearest thing to a reply.
func (s *Source) answersTheApp(ctx context.Context, c classified) bool {
	if c.installID == 0 {
		return false
	}
	comments, err := s.client.ListIssueComments(ctx, c.installID, c.owner, c.repo, c.number)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the conversation before a comment failed",
			"pr", PRURL(c.owner, c.repo, c.number), "error", err)
		return false
	}
	var prev *Comment
	for i := range comments {
		cm := &comments[i]
		if cm.ID == c.commentID || !cm.CreatedAt.Before(c.ts) {
			break
		}
		prev = cm
	}
	if prev == nil || !s.isAppUser(prev.User.Login, prev.User.Type) {
		return false
	}
	at := prev.CreatedAt
	if prev.UpdatedAt.After(at) {
		at = prev.UpdatedAt
	}
	return c.ts.Sub(at) <= conversationReplyWindow
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

// handlePush reviews again what this App pushed to a pull request it has already reviewed:
// the commits a playbook made to fix the review. Anybody else's push is left alone, as it
// always was — reviewing every push would be a review per commit. The trigger filters are not
// asked: the push is this App's own, made for a request they already let through.
func (s *Source) handlePush(ctx context.Context, event, delivery string, c classified) {
	if !s.isAppUser(c.senderLogin, c.senderType) {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "unsupported action")
		return
	}
	key := SourceKey(c.owner, c.repo, c.number)
	if _, known := s.session(ctx, key); !known {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "not reviewed")
		return
	}
	commit, head := s.reviewBaseline(ctx, c)
	if commit == "" {
		s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "ignored", "not reviewed")
		return
	}
	prURL := PRURL(c.owner, c.repo, c.number)
	s.emit(ctx, conductor.InboundEvent{
		SourceKind: Kind,
		SourceKey:  key,
		Ref: Ref(Parsed{
			Owner: c.owner, Repo: c.repo, Number: c.number, InstallID: c.installID, Kind: KindPullRequest,
		}),
		Author:    c.author,
		Text:      followUpInstruction("Podium pushed new commits to "+prURL+" to address the review.", commit, head),
		TS:        c.ts,
		URL:       prURL,
		BriefKind: conductor.SourceGitHub,
		Automatic: true,
	})
	s.logWebhook(ctx, slog.LevelInfo, event, delivery, c, "started", "")
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

// commentInstruction is a person's comment as the assistant is told it: who wrote it, where,
// and what. The commit the last review covered and the head now ride along, so a re-review
// it delegates can be told where that review stopped.
func commentInstruction(c classified, body, commit, head string) string {
	who := firstNonEmpty(c.author, "someone")
	pr := PRURL(c.owner, c.repo, c.number)
	var b strings.Builder
	switch {
	case c.inReply:
		b.WriteString(who + " replied in a review thread on " + pr + ".")
	case c.kind == KindReviewComment:
		b.WriteString(who + " commented on the diff of " + pr + ".")
	case c.kind == KindReview:
		b.WriteString(who + " submitted a review of " + pr + ".")
	default:
		b.WriteString(who + " commented on " + pr + ".")
	}
	if commit != "" {
		b.WriteString(" The last review was on commit " + commit + ".")
		if head != "" && head != commit {
			b.WriteString(" The head is now " + head + ".")
		}
		if head == commit {
			b.WriteString(" The head has not moved since.")
		}
	}
	if c.locked {
		b.WriteString(" This pull request comes from a fork that does not let maintainers push to it, so nothing here can change its code.")
	}
	b.WriteString("\n\n")
	if c.kind == KindReviewComment {
		b.WriteString(reviewReplyInstruction(c.path, c.line, c.diffHunk, body))
	} else {
		b.WriteString(strings.TrimSpace(body))
	}
	return strings.TrimSpace(b.String())
}

// slackInstruction is a Slack mention about a pull request, as the assistant is told it.
func slackInstruction(author, prURL, body string) string {
	head := firstNonEmpty(author, "someone") + " wrote in Slack about " + prURL + "."
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
