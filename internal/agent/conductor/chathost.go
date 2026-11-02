package conductor

// The assistant on the chat host.
//
// With a chat host (internal/agent/chathost) a conversation's turn is not a runtime process:
// it is a session on one long-lived opencode 2 service, in a location of the conversation's
// own. The location names three things on this conductor, each behind a token that belongs to
// the conversation alone:
//
//   - the model gateway (chathost_gateway.go): opencode's model requests come here, and go on to
//     the provider with the credential this conductor holds — never one opencode was given;
//   - the tools (chathost_mcp.go): delegation, the chat's title and the pull-request reader,
//     answered for the turn the conversation is running;
//   - nothing else. The service's own config denies opencode's shell, files, network and its
//     tools for moving a session between locations.
//
// Everything a host turn's runtime did that is not running the model is still done the same
// way: the brief is built the same, the prompt is the runtime's own (rendered without running
// a turn), and the answer, the accounting and the title land on the same turnRun.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/store"
)

// ChatHost is what the conductor uses of a chat host. *chathost.Host is one.
type ChatHost interface {
	Open(ctx context.Context, loc chathost.Location) error
	Close(name string) error
	Answer(ctx context.Context, t chathost.Turn) (chathost.Result, error)
	Steer(ctx context.Context, location, sessionID, text string) error
}

// Paths the chat host reaches this conductor on, under Options.ChatHostURL.
const (
	chatGatewayPath = "/chat-host/gateway/"
	chatMCPPath     = "/chat-host/mcp/"
)

// DefaultChatHostMaxTurns is Options.ChatHostMaxTurns's default. The chat host keeps
// chathost.MaxInFlight requests open; a turn has one model request open at a time, and a tool
// call besides when it delegates.
const DefaultChatHostMaxTurns = chathost.MaxInFlight / 2

// chatIdle is how long a conversation nobody has written to keeps its location.
const chatIdle = 30 * time.Minute

// chatConv is a conversation on the chat host.
type chatConv struct {
	sessionID string
	// token is in every URL the conversation's location names. Whoever holds it is acting as
	// this conversation, so it is never logged and dies with the location.
	token    string
	location string
	// provider and upstream are where the gateway sends this conversation's model requests.
	provider string
	upstream string
	// turn is the turn running now; nil between turns, when the gateway and the tools refuse.
	turn     *chatTurn
	lastUsed time.Time
	// src and ref are where its history is read from; summarised is set once the summary
	// job has looked at it since its last turn. See summary.go.
	src        Source
	ref        string
	summarised bool
}

// chatTurn is what the tools and the gateway act for while one turn runs.
type chatTurn struct {
	id string
	// delegationToken is the turn's authority to delegate, exactly as a host turn's.
	delegationToken string
	// title is what the turn named the chat, through the title tool.
	title string
	// login is the person whose own model key the gateway may spend, if they set one.
	login string
	// session is the turn's opencode session, once it has one; open is true from then until
	// the turn closes, while a message for the conversation is steered into it rather than
	// waiting for the next turn. steering counts steers on their way, delivered those that
	// arrived since the turn last asked whether to close (chatClosing).
	session             string
	open                bool
	steering, delivered int
}

type chatState struct {
	mu      sync.Mutex
	byID    map[string]*chatConv
	byToken map[string]*chatConv
	// perUser is each person's slots: a buffered channel of ChatHostMaxPerUser.
	perUser map[string]chan struct{}
}

func (s *chatState) init() {
	if s.byID == nil {
		s.byID = map[string]*chatConv{}
		s.byToken = map[string]*chatConv{}
	}
}

// chatConvFor is the conversation's chat-host state, made on its first turn.
func (c *Conductor) chatConvFor(sessionID string) (*chatConv, error) {
	c.chat.mu.Lock()
	defer c.chat.mu.Unlock()
	c.chat.init()
	if cv := c.chat.byID[sessionID]; cv != nil {
		cv.lastUsed = time.Now()
		return cv, nil
	}
	token, err := chathost.NewToken()
	if err != nil {
		return nil, err
	}
	cv := &chatConv{sessionID: sessionID, token: token, location: chatLocationName(sessionID), lastUsed: time.Now()}
	c.chat.byID[sessionID] = cv
	c.chat.byToken[token] = cv
	return cv, nil
}

// chatByToken is the conversation a token names, and the turn it is running.
func (c *Conductor) chatByToken(token string) (*chatConv, *chatTurn) {
	c.chat.mu.Lock()
	defer c.chat.mu.Unlock()
	cv := c.chat.byToken[token]
	if cv == nil {
		return nil, nil
	}
	return cv, cv.turn
}

func chatLocationName(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return "conv-" + b.String()
}

// reapChats closes the locations of conversations nobody has written to for chatIdle. Their
// tokens die with them; a later message opens the location again with a new one.
func (c *Conductor) reapChats(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.summarise(ctx)
		c.chat.mu.Lock()
		var idle []*chatConv
		for id, cv := range c.chat.byID {
			if cv.turn == nil && time.Since(cv.lastUsed) > chatIdle {
				idle = append(idle, cv)
				delete(c.chat.byID, id)
				delete(c.chat.byToken, cv.token)
			}
		}
		c.chat.mu.Unlock()
		for _, cv := range idle {
			if err := c.chatHost.Close(cv.location); err != nil {
				c.logger.WarnContext(ctx, "closing an idle conversation on the chat host failed",
					"session_id", cv.sessionID, "error", err)
			}
		}
	}
}

// chatRun is one assistant turn on the chat host. It has the same parts as a hostRun.
type chatRun struct {
	h *hostRun
}

func (cr chatRun) run(ctx context.Context) {
	h := cr.h
	r := h.r
	c := r.c

	c.acquire(ctx, turnLease(r.turn.ID))
	defer c.release(ctx, turnLease(r.turn.ID))
	if h.token != "" {
		defer c.revokeTurnToken(h.token)
	}

	// The person's slot first, then the host's: a person at their limit waits without
	// holding one of the host's.
	releaseUser, ok := c.acquireUserSlot(ctx, h.who)
	if !ok {
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	defer releaseUser()
	release, ok := c.acquireChatSlot(ctx)
	if !ok {
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	defer release()

	runCtx, cancel := context.WithTimeout(ctx, r.job.assistantTimeout())
	defer cancel()
	c.registerHost(r.ref, cancel)
	defer c.unregisterHost(r.ref)

	// A person spending their own credential needs no global one.
	if _, err := c.host.Credential(runCtx, h.provider.ID); err != nil && h.credOwner == "" {
		c.logger.ErrorContext(ctx, "a chat-host turn has no credential to spend",
			"turn_id", r.turn.ID, "provider", h.provider.ID, "error", err)
		h.giveUp(ctx, hostNoCredential)
		return
	}
	cv, err := c.chatConvFor(r.sess.ID)
	if err != nil {
		c.logger.ErrorContext(ctx, "preparing a conversation on the chat host failed", "turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	loc, err := c.chatLocation(runCtx, cv, h)
	if err != nil {
		var stale mcpTokenStaleError
		if errors.As(err, &stale) {
			h.giveUp(ctx, fmt.Sprintf(hostMCPTokenStale, stale.server))
			return
		}
		c.logger.ErrorContext(ctx, "preparing a conversation's location failed", "turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	system, err := c.chatRender(runCtx, h.encoded)
	if err != nil {
		c.logger.ErrorContext(ctx, "rendering a chat-host turn's prompt failed", "turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}

	login := h.login
	if cv.provider == claudeAgentProvider {
		// The sign-in being spent is credOwner's; the gateway must not fall back to anybody
		// else's.
		login = h.credOwner
	}
	turn := &chatTurn{id: r.turn.ID, delegationToken: h.token, login: login}
	c.chat.mu.Lock()
	cv.turn = turn
	c.chat.mu.Unlock()
	defer func() {
		c.chat.mu.Lock()
		cv.turn = nil
		cv.lastUsed = time.Now()
		c.chat.mu.Unlock()
	}()

	if err := c.chatHost.Open(runCtx, loc); err != nil {
		c.logger.ErrorContext(ctx, "opening a conversation on the chat host failed", "turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	c.logger.InfoContext(ctx, "chat-host turn started", "turn_id", r.turn.ID, "session_id", r.sess.ID,
		"source", r.src.Kind(), "provider", cv.provider)

	model := chathost.Model{Provider: h.provider.ID, ID: h.model, Variant: h.effort}
	if cv.provider == claudeAgentProvider {
		// Claude Code picks its own effort; a Podium effort level is not a claude-agent variant.
		model = chathost.Model{Provider: claudeAgentProvider, ID: h.model}
	}
	res, err := c.chatHost.Answer(runCtx, chathost.Turn{
		Location: cv.location,
		Model:    model,
		System:   system,
		Prompt:   h.instruction,
		Started: func(session string) {
			c.chat.mu.Lock()
			turn.session, turn.open = session, true
			c.chat.mu.Unlock()
		},
		Closing: func() bool { return c.chatClosing(turn) },
	})
	// Whatever happened, nothing more is steered into it.
	c.chat.mu.Lock()
	turn.open = false
	c.chat.mu.Unlock()
	stopped := runCtx.Err() != nil
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	done := ctx
	if stopped {
		var stop context.CancelFunc
		done, stop = context.WithTimeout(context.WithoutCancel(ctx), hostRecordGrace)
		defer stop()
	}
	if err != nil && !stopped {
		c.logger.ErrorContext(done, "a chat-host turn failed", "turn_id", r.turn.ID, "error", err)
	}

	r.noteFirstMessage()
	answers := res.Answers
	if len(answers) == 0 && res.Text != "" {
		answers = []string{res.Text}
	}
	// One answer per message: the one that started the turn, then each steered in.
	for _, a := range answers {
		r.deliver(done, OutFinal, a, nil)
	}
	steps := res.Steps
	cost := res.CostUSD
	r.acct = &accounting{NumTurns: &steps, TotalCostUSD: &cost}
	r.acctFrom = "chat-host"
	r.flushProgress(done)

	status := store.TurnSucceeded
	switch {
	case timedOut:
		status = store.TurnFailed
		if !r.said() {
			c.post(done, r.src, r.ref, Outbound{Type: OutFailure, Text: fmt.Sprintf(hostTimedOut, r.job.assistantTimeout())})
		}
	case stopped:
		status = store.TurnCancelled
	case err != nil || res.Outcome == "failed":
		status = store.TurnFailed
		c.logger.WarnContext(done, "a chat-host turn did not succeed", "turn_id", r.turn.ID,
			"outcome", res.Outcome, "reason", res.Error)
	}
	if !r.said() {
		c.post(done, r.src, r.ref, Outbound{Type: OutFailure, Text: hostSaidNothing})
	}
	if status == store.TurnSucceeded && turn.title != "" {
		if namer, ok := r.src.(autoTitler); ok {
			if err := namer.SetAutoTitle(done, r.ref, turn.title); err != nil {
				c.logger.WarnContext(done, "naming the chat from the turn failed", "ref", r.ref, "error", err)
			}
		}
	}
	c.sayAnnouncementsFor(done, r.turn.ID)
	c.noteChatTurn(r.sess.ID, r.src, r.ref)
	r.finish(done, status)
}

// chatLocation is the location a turn of cv runs in: its model endpoint and its tools, every
// URL carrying the conversation's token.
func (c *Conductor) chatLocation(ctx context.Context, cv *chatConv, h *hostRun) (chathost.Location, error) {
	base := strings.TrimSuffix(c.chatHostURL, "/")
	upstream := h.provider.BaseURL
	if upstream == "" && h.provider.ID == "anthropic" && c.anthropicBaseURL != "" {
		upstream = strings.TrimSuffix(c.anthropicBaseURL, "/") + "/v1"
	}
	if upstream == "" {
		upstream = defaultUpstream(h.provider.ID)
	}
	provider := chathost.Provider{
		ID:      h.provider.ID,
		BaseURL: base + chatGatewayPath + cv.token,
		// The gateway takes the conversation from the URL and replaces this.
		APIKey: "podium",
	}
	if c.claudeAgentTurn(h) {
		// A Claude subscription: Claude Code itself runs the model, through opencode's
		// claude-agent provider, and reaches Anthropic through the gateway like any other
		// provider. Claude Code appends /v1/... to its base URL, so the upstream is the host.
		upstream = strings.TrimSuffix(strings.TrimSuffix(upstream, "/"), "/v1")
		provider = chathost.Provider{
			ID:        claudeAgentProvider,
			Builtin:   true,
			BaseURL:   provider.BaseURL,
			AuthToken: "podium",
			// Declared, so the model can be picked before Claude Code has reported its list.
			Models: []string{h.model},
		}
	}
	c.chat.mu.Lock()
	cv.provider, cv.upstream = provider.ID, upstream
	c.chat.mu.Unlock()

	loc := chathost.Location{
		Name:      cv.location,
		Providers: []chathost.Provider{provider},
	}
	mcpURL := func(server string) string { return base + chatMCPPath + cv.token + "/" + server }
	if h.token != "" {
		loc.MCP = append(loc.MCP, chathost.MCPServer{Name: chatDelegateServer, URL: mcpURL(chatDelegateServer)})
	}
	loc.MCP = append(loc.MCP, chathost.MCPServer{Name: chatTitleServer, URL: mcpURL(chatTitleServer)})
	if h.pullRequest {
		loc.MCP = append(loc.MCP, chathost.MCPServer{Name: PullRequestMCPName, URL: mcpURL(PullRequestMCPName)})
	}
	if h.memoryURL != "" && c.host.MemoryAPIKey != "" {
		loc.MCP = append(loc.MCP, chathost.MCPServer{Name: "memory", URL: h.memoryURL,
			Headers: map[string]string{"Authorization": "Bearer " + c.host.MemoryAPIKey}})
	}
	for _, srv := range h.servers {
		headers := map[string]string{}
		if cfg, err := mcp.ParseConfig(srv.Config); err == nil {
			if hs, ok := cfg["headers"].(map[string]any); ok {
				for k, v := range hs {
					if s, ok := v.(string); ok && !strings.EqualFold(k, "authorization") {
						headers[k] = s
					}
				}
			}
		}
		if srv.TokenSecretVersion != 0 {
			token, err := c.mcpToken(ctx, srv)
			if err != nil {
				return chathost.Location{}, err
			}
			headers["Authorization"] = "Bearer " + token
		}
		loc.MCP = append(loc.MCP, chathost.MCPServer{Name: srv.Name, URL: srv.URL, Headers: headers})
	}
	return loc, nil
}

// chatSteer writes a message into the conversation's running chat-host turn, which answers it
// as part of the same turn. False when there is no turn open to take it — none running, one
// not started yet, or one already closing — and the message waits for the next turn as it
// always has.
func (c *Conductor) chatSteer(ctx context.Context, sessionID, text string) bool {
	if c.chatHost == nil {
		return false
	}
	c.chat.mu.Lock()
	cv := c.chat.byID[sessionID]
	if cv == nil || cv.turn == nil || !cv.turn.open || cv.turn.session == "" {
		c.chat.mu.Unlock()
		return false
	}
	turn := cv.turn
	turn.steering++
	session, location := turn.session, cv.location
	c.chat.mu.Unlock()

	err := c.chatHost.Steer(ctx, location, session, text)

	c.chat.mu.Lock()
	turn.steering--
	if err == nil {
		turn.delivered++
	}
	c.chat.mu.Unlock()
	if err != nil {
		c.logger.WarnContext(ctx, "steering a message into a chat-host turn failed; it waits for the next turn",
			"session_id", sessionID, "error", err)
		return false
	}
	return true
}

// chatClosing is asked each time a turn's session goes idle. A steer on its way or delivered
// since it was last asked keeps the turn going, to answer it; otherwise the turn closes, and
// chatSteer sends nothing more into it.
func (c *Conductor) chatClosing(turn *chatTurn) bool {
	c.chat.mu.Lock()
	defer c.chat.mu.Unlock()
	if turn.steering > 0 || turn.delivered > 0 {
		turn.delivered = 0
		return true
	}
	turn.open = false
	return false
}

// claudeAgentProvider is opencode 2's provider that runs Claude Code with a Claude login.
const claudeAgentProvider = "claude-agent"

// claudeAgentTurn is whether h spends a Claude subscription sign-in: an Anthropic turn whose
// person signed in with Claude, on a chat host that can run Claude Code.
func (c *Conductor) claudeAgentTurn(h *hostRun) bool {
	return c.chatHostClaude && h.provider != nil && h.provider.ID == "anthropic" && c.claudeSignIn(h.credOwner)
}

// credentialProvider is whose credentials a gateway provider spends: claude-agent is
// Anthropic's.
func credentialProvider(provider string) string {
	if provider == claudeAgentProvider {
		return "anthropic"
	}
	return provider
}

// defaultUpstream is a provider's API root when the conductor has no opinion about it: the one
// opencode itself would use.
func defaultUpstream(provider string) string {
	switch provider {
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "openai":
		return "https://api.openai.com/v1"
	case "xai":
		return "https://api.x.ai/v1"
	case "google":
		return "https://generativelanguage.googleapis.com/v1beta"
	}
	return ""
}

// DefaultChatHostMaxPerUser is Options.ChatHostMaxPerUser's default.
const DefaultChatHostMaxPerUser = 4

// acquireUserSlot bounds one person's turns running at once, so one person with many
// conversations cannot hold every slot the host has. A turn past the limit waits for one of
// that person's own to finish.
func (c *Conductor) acquireUserSlot(ctx context.Context, who string) (func(), bool) {
	c.chat.mu.Lock()
	if c.chat.perUser == nil {
		c.chat.perUser = map[string]chan struct{}{}
	}
	slots := c.chat.perUser[who]
	if slots == nil {
		slots = make(chan struct{}, max(c.chatPerUser, 1))
		c.chat.perUser[who] = slots
	}
	c.chat.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// acquireChatSlot bounds how many chat-host turns run at once. The chat host itself holds
// hundreds; the bound is the model provider's rate limit, and a queue here is a clearer
// failure than a wall of 429s.
func (c *Conductor) acquireChatSlot(ctx context.Context) (func(), bool) {
	select {
	case c.chatSlots <- struct{}{}:
		return func() { <-c.chatSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}
