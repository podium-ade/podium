package ghreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
	r.Body = http.MaxBytesReader(w, r.Body, MaxWebhookBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.webhookSecret, body, r.Header.Get(headerSignature)) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get(headerEvent)
	delivery := r.Header.Get(headerDelivery)
	if event == "ping" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if delivery != "" && !s.firstSighting(delivery) {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.handleDelivery(r.Context(), event, body)
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

func (s *Source) handleDelivery(ctx context.Context, event string, body []byte) {
	c, ok := s.classify(event, body)
	if !ok {
		return
	}
	if s.isAppUser(c.senderLogin, c.senderType) {
		return
	}

	key := SourceKey(c.owner, c.repo, c.number)
	_, known := s.session(ctx, key)
	if !c.mentioned && !(c.inReply && known) {
		return
	}
	if !known && !c.mentioned {
		return
	}

	text := strings.TrimSpace(s.stripMention(c.body))
	switch {
	case !known:
		text = reviewInstruction(c.author, PRURL(c.owner, c.repo, c.number), text)
	case c.inReply:
		text = reviewReplyInstruction(c.path, c.line, c.diffHunk, text)
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
	mentioned   bool
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
		owner:       env.Repository.Owner.Login,
		repo:        env.Repository.Name,
		installID:   env.Installation.ID,
		senderLogin: env.Sender.Login,
		senderType:  env.Sender.Type,
	}
	if base.owner == "" || base.repo == "" {
		return classified{}, false
	}

	switch event {
	case "issue_comment":
		if env.Action != "created" {
			return classified{}, false
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
			return classified{}, false
		}
		if len(p.Issue.PullRequest) == 0 || string(p.Issue.PullRequest) == "null" {
			return classified{}, false
		}
		base.number = p.Issue.Number
		base.kind = KindIssueComment
		base.commentID = p.Comment.ID
		base.body = p.Comment.Body
		base.author = firstNonEmpty(p.Comment.User.Login, env.Sender.Login)
		base.ts = p.Comment.CreatedAt
		base.mentioned = s.mentioned(p.Comment.Body)
		return base, base.number > 0 && base.commentID > 0

	case "pull_request_review_comment":
		if env.Action != "created" {
			return classified{}, false
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
			return classified{}, false
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
		base.mentioned = s.mentioned(p.Comment.Body)
		return base, base.number > 0 && base.commentID > 0

	case "pull_request_review":
		if env.Action != "submitted" {
			return classified{}, false
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
			return classified{}, false
		}
		base.number = p.PullRequest.Number
		base.kind = KindReview
		base.commentID = p.Review.ID
		base.body = p.Review.Body
		base.author = firstNonEmpty(p.Review.User.Login, env.Sender.Login)
		base.ts = p.Review.SubmittedAt
		base.mentioned = s.mentioned(p.Review.Body)
		return base, base.number > 0 && base.commentID > 0 && strings.TrimSpace(p.Review.Body) != ""
	default:
		return classified{}, false
	}
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
