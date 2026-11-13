package linear

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// WebhookPath is the agent app's route on the webhook listener. The URL entered in Linear
// is the public address that reaches the listen address, with this path.
const WebhookPath = "/webhooks/linear"

// MaxWebhookBytes is the largest delivery read. An agent session event carries the issue,
// the comment and Linear's prompt context, which is kilobytes; a megabyte is what stops a
// delivery filling memory without refusing a real one.
const MaxWebhookBytes = 1 << 20

// WebhookWindow is how far a delivery's webhookTimestamp may be from this host's clock.
// Linear's own guidance is a minute, and a signed delivery older than that is a replay.
const WebhookWindow = 60 * time.Second

const (
	headerDelivery  = "Linear-Delivery"
	headerEvent     = "Linear-Event"
	headerSignature = "Linear-Signature"
)

// The webhook payload types and actions this source reads.
const (
	TypeAgentSession     = "AgentSessionEvent"
	TypeAppNotification  = "AppUserNotification"
	TypePermissionChange = "PermissionChange"
	TypeOAuthApp         = "OAuthApp"

	ActionCreated  = "created"
	ActionPrompted = "prompted"
	ActionRevoked  = "revoked"

	// SignalStop is a prompt asking the agent to stop what it is doing.
	SignalStop = "stop"
)

// webhookPayload is a delivery, as much of it as this source reads. It is typed rather than
// a map so the fields later work needs — the speaker's email for identity, the session's
// source metadata for routing — are named once, here.
type webhookPayload struct {
	Type      string    `json:"type"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"createdAt"`
	// OrganizationID is the workspace and AppUserID the app user the delivery is for. An app
	// installed in several workspaces signs every delivery with one secret, so these are
	// what tell this conductor's workspace from the others.
	OrganizationID string `json:"organizationId"`
	AppUserID      string `json:"appUserId"`
	OAuthClientID  string `json:"oauthClientId"`
	// WebhookTimestamp is when Linear sent the delivery, in milliseconds since the epoch.
	WebhookTimestamp int64  `json:"webhookTimestamp"`
	WebhookID        string `json:"webhookId"`

	AgentSession  *agentSession  `json:"agentSession"`
	AgentActivity *agentActivity `json:"agentActivity"`
	// PromptContext is Linear's own rendering of the issue and the thread for an agent. It
	// is kept for reading, not handed to a turn: the transcript carries the issue.
	PromptContext string `json:"promptContext"`
}

// agentSession is the session a delivery is about.
type agentSession struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Status string `json:"status"`
	// SourceMetadata is null for a delegation and says what started the session otherwise:
	// {"type": "comment", …} for an @mention.
	SourceMetadata *sessionSource  `json:"sourceMetadata"`
	Creator        *payloadUser    `json:"creator"`
	Issue          *payloadIssue   `json:"issue"`
	Comment        *payloadComment `json:"comment"`
}

// sessionSource is what started a session that was not a delegation.
type sessionSource struct {
	Type                 string `json:"type"`
	AgentSessionMetadata *struct {
		SourceCommentID string `json:"sourceCommentId"`
	} `json:"agentSessionMetadata"`
}

// agentActivity is the prompt a delivery carries: what the person said, and who they are.
type agentActivity struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	SourceCommentID string    `json:"sourceCommentId"`
	// Signal is "stop" on a prompt that asks the agent to stop, and empty otherwise.
	Signal  string          `json:"signal"`
	Content activityContent `json:"content"`
	// User is the speaker. A follow-up can come from somebody other than the session's
	// creator, so who is asking is read from here on every prompt.
	User *payloadUser `json:"user"`
}

type activityContent struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

// payloadUser is a person as a webhook names one. Email is in the payload for every prompt.
type payloadUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	URL   string `json:"url"`
}

type payloadIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	TeamID      string `json:"teamId"`
	Team        *struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
}

type payloadComment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// delegated reports whether the session was started by assigning the issue to the app,
// rather than by an @mention.
func (s *agentSession) delegated() bool { return s.SourceMetadata == nil }

// Handler is POST /webhooks/linear: signed, fresh deliveries for this app in this workspace,
// and nothing else.
func (s *AppSource) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(WebhookPath, s.serveWebhook)
	return mux
}

// serveWebhook answers within Linear's five seconds by doing only the checks here and
// leaving the work to the session's own queue. Linear retries an unanswered delivery after a
// minute, an hour and six hours, and may then turn the webhook off.
func (s *AppSource) serveWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := delivery{id: r.Header.Get(headerDelivery), event: r.Header.Get(headerEvent)}
	r.Body = http.MaxBytesReader(w, r.Body, MaxWebhookBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logDelivery(ctx, slog.LevelWarn, d, DeliveryRejected, "payload too large")
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.secret, body, r.Header.Get(headerSignature)) {
		s.logDelivery(ctx, slog.LevelWarn, d, DeliveryRejected, "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		s.logDelivery(ctx, slog.LevelWarn, d, DeliveryRejected, "not JSON")
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	d.payload = &p
	if !s.fresh(p.WebhookTimestamp) {
		// Signed, so Linear sent it, but long enough ago that it is a replay.
		s.logDelivery(ctx, slog.LevelWarn, d, DeliveryRejected, "timestamp outside the window")
		http.Error(w, "stale delivery", http.StatusUnauthorized)
		return
	}
	if d.id != "" && !s.firstSighting("delivery:"+d.id) {
		s.logDelivery(ctx, slog.LevelInfo, d, DeliveryDuplicate, "")
		w.WriteHeader(http.StatusOK)
		return
	}
	outcome, reason, level := s.handleDelivery(&p)
	s.logDelivery(ctx, level, d, outcome, reason)
	w.WriteHeader(http.StatusOK)
}

// handleDelivery decides what one checked delivery means and queues whatever it starts. It
// returns how the delivery is logged and counted.
func (s *AppSource) handleDelivery(p *webhookPayload) (outcome, reason string, level slog.Level) {
	if why := s.foreign(p); why != "" {
		return DeliveryIgnored, why, slog.LevelInfo
	}
	switch p.Type {
	case TypeAgentSession:
	case TypeAppNotification:
		// Every delegation and mention also arrives as a notification. The session event is
		// the one acted on, so this one would start every turn twice.
		return DeliveryIgnored, "notification", slog.LevelInfo
	case TypePermissionChange:
		return DeliveryLogged, "the app's team access changed", slog.LevelInfo
	case TypeOAuthApp:
		if p.Action != ActionRevoked {
			return DeliveryIgnored, "unhandled", slog.LevelInfo
		}
		s.revoke()
		return DeliveryRevoked, "Linear revoked the app; nothing starts until it is saved again", slog.LevelError
	default:
		return DeliveryIgnored, "unhandled", slog.LevelInfo
	}

	if s.Revoked() {
		return DeliveryIgnored, "revoked", slog.LevelInfo
	}
	sess, act := p.AgentSession, p.AgentActivity
	if sess == nil || sess.ID == "" || sess.Issue == nil || sess.Issue.ID == "" || act == nil || act.ID == "" {
		return DeliveryIgnored, "no session, issue or prompt", slog.LevelWarn
	}
	switch p.Action {
	case ActionCreated, ActionPrompted:
	default:
		return DeliveryIgnored, "unhandled", slog.LevelInfo
	}
	if act.Signal == SignalStop {
		// Stopping a turn is not built yet: a stop starts nothing, and the turn already
		// running finishes. See docs/linear-agent-plan.md.
		return DeliveryIgnored, "stop signal", slog.LevelInfo
	}
	if p.Action == ActionPrompted && strings.TrimSpace(act.Content.Body) == "" {
		return DeliveryIgnored, "empty prompt", slog.LevelInfo
	}
	// A retried delivery has a new delivery id and the same prompt. There is no record of
	// this conversation to dedupe it against, so the prompt's id is remembered here.
	if !s.firstSighting("activity:" + act.ID) {
		return DeliveryDuplicate, "prompt already handled", slog.LevelInfo
	}
	ev := s.inbound(p)
	created := p.Action == ActionCreated
	s.enqueue(sess.ID, func(work context.Context) {
		if created {
			// Linear marks a session unresponsive when nothing is said within ten seconds of
			// it opening. Choosing a playbook and starting a container can take longer, so
			// the source says something itself, before the conductor sees the event.
			s.acknowledge(work, sess.ID)
		}
		s.emit(work, ev)
	})
	return DeliveryAccepted, "", slog.LevelInfo
}

// foreign is why a delivery is for some other workspace or app user, or "" when it is for
// this one. An app's secret is the same in every workspace it is installed in, so the
// signature alone does not say the delivery is this conductor's. A session event always
// names both; a field another kind of delivery leaves out is not held against it.
func (s *AppSource) foreign(p *webhookPayload) string {
	if p.Type == TypeAgentSession && (p.OrganizationID == "" || p.AppUserID == "") {
		return "no workspace or app user"
	}
	if p.OrganizationID != "" && p.OrganizationID != s.organizationID {
		return "another workspace"
	}
	if p.AppUserID != "" && p.AppUserID != s.appUserID {
		return "another app user"
	}
	return ""
}

// fresh reports whether a delivery's timestamp is within WebhookWindow of now.
func (s *AppSource) fresh(ms int64) bool {
	if ms <= 0 {
		return false
	}
	skew := s.now().Sub(time.UnixMilli(ms))
	return skew <= WebhookWindow && skew >= -WebhookWindow
}

// validSignature checks Linear-Signature, a hex HMAC-SHA256 of the raw body keyed with the
// webhook's signing secret, in constant time.
func validSignature(secret string, body []byte, header string) bool {
	want, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil || len(want) == 0 || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// delivery is what a log line and a counter say about one delivery.
type delivery struct {
	id    string
	event string
	// payload is nil until the body has been verified and parsed.
	payload *webhookPayload
}

// logDelivery records one delivery and counts it. The body stays out of the log: it is what
// people wrote, and the signature header is derived from a secret.
func (s *AppSource) logDelivery(ctx context.Context, level slog.Level, d delivery, outcome, reason string) {
	typ := "unknown"
	attrs := []any{"outcome", outcome}
	if d.id != "" {
		attrs = append(attrs, "delivery", d.id)
	}
	if p := d.payload; p != nil {
		typ = deliveryType(p.Type)
		attrs = append(attrs, "type", p.Type)
		if p.Action != "" {
			attrs = append(attrs, "action", p.Action)
		}
		if p.AgentSession != nil {
			attrs = append(attrs, "session", p.AgentSession.ID)
			if p.AgentSession.Issue != nil {
				attrs = append(attrs, "issue", p.AgentSession.Issue.Identifier)
			}
		}
		if p.AgentActivity != nil && p.AgentActivity.Signal != "" {
			attrs = append(attrs, "signal", p.AgentActivity.Signal)
		}
		if p.OrganizationID != "" && p.OrganizationID != s.organizationID {
			attrs = append(attrs, "organization_id", p.OrganizationID)
		}
	} else if d.event != "" {
		// Unverified, so only logged, never counted under its own name.
		attrs = append(attrs, "event", d.event)
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	s.metrics.Deliveries.WithLabelValues(typ, outcome).Inc()
	s.logger.Log(ctx, level, "linear: webhook", attrs...)
}

// deliveryType is a payload type as a metric label: one of the types this source knows, or
// "other", so the label's values stay few whatever Linear adds.
func deliveryType(t string) string {
	switch t {
	case TypeAgentSession, TypeAppNotification, TypePermissionChange, TypeOAuthApp:
		return t
	}
	return "other"
}
