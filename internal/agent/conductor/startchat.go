package conductor

// Opening a chat from a turn: "starting a triage on this session — click here to join".
//
// A turn's start_chat tool makes a new web chat and hands back the link to it. The chat is
// the turn's person's, exactly as if they had opened it themselves: they own it, they can
// delete it, and its turns run as whoever speaks in it (gitcred.go). The assistant writes
// its first message — the brief the new conversation starts from — and the chat says which
// conversation it came from and links back. It can be shared on the way out, with named
// Podium people, with every member, or with anyone holding the link, which is the same
// sharing the Share dialog does (phase 5) and is audited the same way.
//
// What it cannot do is more than a person could. Nobody in Podium behind the turn — an
// automatic review, a ticket the Linear poller saw, an anonymous review command — means nobody
// to own a chat, and the tool says so. A Linear agent session's turn runs as the person who
// asked, and opens a chat as anybody's turn does. Sharing names Podium people only. And a turn opens at most
// MaxChatsStartedPerTurn chats, counted in the database so every conductor agrees.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// MaxChatsStartedPerTurn bounds how many chats one turn may open. A triage, a follow-up,
	// a side thread; more than that from one answer is a loop, not a plan.
	MaxChatsStartedPerTurn = 3
	// MaxStartChatShares bounds how many people one start_chat names. Members or link
	// access is the tool for "everybody".
	MaxStartChatShares = 20
	// maxStartChatOpening bounds the first message. It is a brief, not a transcript: the
	// new chat links back to the conversation it came from.
	maxStartChatOpening = 16 * 1024
	// startChatTitleRunes matches the chat's own cap on a title taken from a first message.
	startChatTitleRunes = 48
)

// Audit actions for a chat a turn opened. chat.share and chat.general_access are the Share
// dialog's own actions, so a reader of the audit log sees one kind of row whoever shared.
const (
	auditChatStart         = "chat.start"
	auditChatShare         = "chat.share"
	auditChatGeneralAccess = "chat.general_access"
)

// ErrNoChatOwner is a turn nobody in Podium started: there is nobody to own a chat it opens.
var ErrNoChatOwner = errors.New("a new chat needs a Podium person to own it, and nobody in " +
	"Podium started this turn; ask from a conversation a Podium user started")

// ErrTooManyChats is a turn that has already opened MaxChatsStartedPerTurn chats.
var ErrTooManyChats = fmt.Errorf("this turn has already opened %d chats, which is as many as one turn may",
	MaxChatsStartedPerTurn)

// ErrInvalidStartChat is a start_chat whose arguments are wrong. The message says which, in
// words the model can correct.
var ErrInvalidStartChat = errors.New("start_chat: invalid arguments")

type invalidStartChat struct{ error }

func (invalidStartChat) Is(target error) bool { return target == ErrInvalidStartChat }

func invalid(err error) error { return invalidStartChat{err} }

func invalidf(format string, args ...any) error { return invalid(fmt.Errorf(format, args...)) }

// StartChatRequest is what start_chat asks for.
type StartChatRequest struct {
	// Title names the new chat. Required.
	Title string
	// Opening is the assistant's first message in it: what the conversation is for and what
	// it starts from. Required.
	Opening string
	// ShareWith is Podium people to share it with, by login (an email address).
	ShareWith []string
	// Access is general access: store.AccessRestricted (the default), AccessMembers or
	// AccessLink.
	Access string
	// Role is what sharing grants: store.RoleEditor (the default) or RoleViewer.
	Role string
}

// StartedChat is the chat start_chat opened.
type StartedChat struct {
	Chat store.Chat
	// URL is the link to it as a person reaches it, carrying the link token when the chat is
	// shared by link. A path ("/agent/chat/<id>") when this conductor knows no UI URL.
	URL string
	// SharedWith is who was given access by name, in the spelling Podium has for them.
	SharedWith []string
}

// chatPublisher is the web chat, as opening a chat needs it: the deep link, the multiplayer
// notice, and the frame that tells a watching screen a chat changed. chat.Source implements
// it; it is reached through Options.Mirror, which is that same source.
type chatPublisher interface {
	URL(chatID string) string
	Multiplayer(ctx context.Context, chatID string, force bool, who string)
	ChatChanged(ctx context.Context, chatID string)
}

// chatAuditor is the control plane's audit log, as podium.Client offers it.
type chatAuditor interface {
	AuditChat(ctx context.Context, actor, action, chatID string, details map[string]any) error
}

// StartChat opens a chat for the turn token holds. Everything is checked before anything is
// written, so a refused call leaves nothing behind; the writes after that are the chat, its
// first message, and its sharing, and a failure among the sharing is reported with the chat
// it left, rather than as if nothing had happened.
func (c *Conductor) StartChat(ctx context.Context, token string, req StartChatRequest) (StartedChat, error) {
	g, ok := c.grantFor(ctx, token)
	if !ok {
		return StartedChat{}, ErrNoTurn
	}
	if c.store == nil {
		return StartedChat{}, errors.New("this conductor has no database, so it cannot open a chat")
	}
	owner := turnOwner(g.login)
	if owner == "" {
		return StartedChat{}, ErrNoChatOwner
	}
	title := strings.Join(strings.Fields(req.Title), " ")
	if title == "" {
		return StartedChat{}, invalid(errors.New("title is required"))
	}
	title = truncateRunes(title, startChatTitleRunes)
	opening := strings.TrimSpace(req.Opening)
	switch {
	case opening == "":
		return StartedChat{}, invalid(errors.New("opening is required: it is the first message of the new chat"))
	case len(opening) > maxStartChatOpening:
		return StartedChat{}, invalidf("opening is %d bytes; keep it under %d — the new chat links back to this one",
			len(opening), maxStartChatOpening)
	}
	access := strings.ToLower(strings.TrimSpace(req.Access))
	switch access {
	case "":
		access = store.AccessRestricted
	case store.AccessRestricted, store.AccessMembers, store.AccessLink:
	default:
		return StartedChat{}, invalidf("access %q is not one of restricted, members or link", req.Access)
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	switch role {
	case "":
		role = store.RoleEditor
	case store.RoleEditor, store.RoleViewer:
	default:
		return StartedChat{}, invalidf("role %q is not one of editor or viewer", req.Role)
	}
	share, err := c.startChatShares(ctx, owner, req.ShareWith)
	if err != nil {
		return StartedChat{}, err
	}
	n, err := c.store.CountChatsStartedByTurn(ctx, g.turnID)
	if err != nil {
		return StartedChat{}, err
	}
	if n >= MaxChatsStartedPerTurn {
		return StartedChat{}, ErrTooManyChats
	}
	if err := c.checkRoutineChatCap(ctx, g.turnID); err != nil {
		return StartedChat{}, err
	}

	// Where it came from: the chat this turn's conversation is, web or mirrored. A
	// conversation with no chat row (a dev source, a ticket the Linear poller saw) has nowhere
	// to link back to.
	from := c.startedFrom(ctx, g)

	chat, err := c.store.CreateChat(ctx, owner, title)
	if err != nil {
		return StartedChat{}, fmt.Errorf("opening the chat failed: %w", err)
	}
	if chat, err = c.store.SetChatStartedFrom(ctx, chat.ID, from.ID, g.turnID); err != nil {
		return StartedChat{}, fmt.Errorf("opening the chat failed: %w", err)
	}
	c.audit(ctx, owner, auditChatStart, chat.ID, map[string]any{
		"turn_id": g.turnID, "from_chat": from.ID, "access": access, "shares": len(share),
	})
	pub, _ := c.mirror.(chatPublisher)
	if _, err := c.store.AppendChatMessage(ctx, store.ChatMessage{
		ChatID: chat.ID,
		Role:   store.RoleAssistant,
		Text:   opening + c.startedFromLine(ctx, owner, from, pub),
		TS:     time.Now().UTC(),
	}); err != nil {
		return StartedChat{Chat: chat}, fmt.Errorf("the chat %s was opened but its first message could not be written: %w", chat.ID, err)
	}

	out := StartedChat{Chat: chat}
	for _, login := range share {
		if _, err := c.store.ShareChat(ctx, chat.ID, login, role, owner); err != nil {
			out.URL = chatLink(pub, chat)
			return out, fmt.Errorf("the chat was opened (%s) but sharing it with %s failed: %w", out.URL, login, err)
		}
		out.SharedWith = append(out.SharedWith, login)
		c.audit(ctx, owner, auditChatShare, chat.ID, map[string]any{
			"login": login, "role": role, "first": true, "via": "assistant", "turn_id": g.turnID,
		})
	}
	if access != store.AccessRestricted {
		if chat, err = c.store.SetChatGeneralAccess(ctx, chat.ID, access, role); err == nil && access == store.AccessLink {
			chat, err = c.store.RotateChatLinkToken(ctx, chat.ID)
		}
		if err != nil {
			out.URL = chatLink(pub, out.Chat)
			return out, fmt.Errorf("the chat was opened (%s) but giving it %s access failed: %w", out.URL, access, err)
		}
		out.Chat = chat
		c.audit(ctx, owner, auditChatGeneralAccess, chat.ID, map[string]any{
			"from":   map[string]any{"access": store.AccessRestricted, "role": store.RoleEditor},
			"access": access, "role": role, "via": "assistant", "turn_id": g.turnID,
		})
	}
	if pub != nil && (len(share) > 0 || access != store.AccessRestricted) {
		pub.Multiplayer(ctx, chat.ID, true, "")
		pub.ChatChanged(ctx, chat.ID)
	}
	if fresh, err := c.store.GetChat(ctx, chat.ID); err == nil {
		out.Chat = fresh
	}
	out.URL = chatLink(pub, out.Chat)
	c.logger.InfoContext(ctx, "a turn opened a chat", "turn_id", g.turnID, "chat_id", chat.ID,
		"owner", owner, "from_chat", from.ID, "access", access, "shared_with", len(out.SharedWith))
	return out, nil
}

// startChatShares resolves the logins a start_chat names to the people Podium has, dropping
// the owner and repeats. Somebody who has never signed in is refused by name, before
// anything is written.
func (c *Conductor) startChatShares(ctx context.Context, owner string, typed []string) ([]string, error) {
	if len(typed) > MaxStartChatShares {
		return nil, invalidf("share_with names %d people; at most %d — use members or link access for more",
			len(typed), MaxStartChatShares)
	}
	var out, unknown []string
	seen := map[string]bool{strings.ToLower(owner): true}
	for _, t := range typed {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		login, ok, err := c.store.CanonicalWebLogin(ctx, t)
		if err != nil {
			return nil, err
		}
		if !ok {
			unknown = append(unknown, t)
			continue
		}
		if seen[strings.ToLower(login)] {
			continue
		}
		seen[strings.ToLower(login)] = true
		out = append(out, login)
	}
	if len(unknown) > 0 {
		return nil, invalidf("%s %s not signed in to Podium, so nothing was opened; share only with Podium people "+
			"(their email address), or use link access", strings.Join(unknown, ", "), plural(len(unknown), "has", "have"))
	}
	return out, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// startedFrom is the chat a turn's conversation is: its own web chat, or the chat mirroring
// its Slack thread, pull request or Linear agent session. The zero Chat when it has none.
func (c *Conductor) startedFrom(ctx context.Context, g turnGrant) store.Chat {
	sess, err := c.store.GetSession(ctx, g.sessionID)
	if err != nil || sess.SourceKey == "" {
		return store.Chat{}
	}
	chat, err := c.store.ChatBySourceKey(ctx, sess.SourceKey)
	if err != nil || chat.DeletedAt != nil {
		return store.Chat{}
	}
	return chat
}

// startedFromLine is the line under the opening that says who opened the chat and from
// where. The origin's title is left out: the new chat may be shared with people who cannot
// see the conversation it came from, and a title can say more than its link does.
func (c *Conductor) startedFromLine(ctx context.Context, owner string, from store.Chat, pub chatPublisher) string {
	name, _ := c.requestedBy(ctx, owner)
	if name == "" {
		name = owner
	}
	if from.ID == "" {
		return fmt.Sprintf("\n\n_Opened for %s._", name)
	}
	// Never with the origin's link token: that would hand everybody in the new chat a way into
	// the old one. Whoever can already see it follows the link; nobody else gets in by it.
	return fmt.Sprintf("\n\n_Opened for %s from [another conversation](%s)._", name, chatPath(pub, from.ID))
}

// chatLink is the link to a chat as a person reaches it: with its link token when it is
// shared by link, so the link works for somebody who has not been named.
func chatLink(pub chatPublisher, chat store.Chat) string {
	link := chatPath(pub, chat.ID)
	if chat.GeneralAccess == store.AccessLink && chat.LinkToken != "" {
		link += "?k=" + url.QueryEscape(chat.LinkToken)
	}
	return link
}

// chatPath is the plain link to a chat, which only works for somebody who already has access.
func chatPath(pub chatPublisher, chatID string) string {
	if pub != nil {
		if link := pub.URL(chatID); link != "" {
			return link
		}
	}
	return "/agent/chat/" + url.PathEscape(chatID)
}

// audit writes one row to the control plane's audit log. Like the Share dialog's, a row that
// cannot be written is logged and the action stands.
func (c *Conductor) audit(ctx context.Context, actor, action, chatID string, details map[string]any) {
	if c.podium == nil {
		return
	}
	var a chatAuditor = c.podium
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.AuditChat(actx, actor, action, chatID, details); err != nil {
		c.logger.WarnContext(ctx, "writing a chat audit entry failed; the action stands",
			"action", action, "chat_id", chatID, "actor", actor, "error", err)
	}
}

// StartChatTool is start_chat as an MCP tool. The runtime's own server (agent/runtime/src/
// mcp.ts) offers it in the same words, because the prompt talks about it the same way.
var StartChatTool = chatTool{
	Name: "start_chat",
	Description: "Open a NEW Podium chat for a piece of work that deserves its own conversation — a triage, " +
		"an incident, a design discussion — and invite people into it. The chat belongs to the person " +
		"who asked you, starts with your opening message, and links back to this conversation. Returns " +
		"the link: put it in your answer as a markdown link, e.g. \"Starting a triage — [click here to " +
		"join](<url>)\". Use it only when the person asks for a separate conversation or plainly wants " +
		"others brought in; otherwise answer here. share_with is Podium people by email; access " +
		"\"members\" lets every Podium member find it, \"link\" lets anyone with the link in.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "The new chat's name. 3–6 words."},
			"opening": map[string]any{"type": "string", "description": "Your first message in the new chat: what it is " +
				"for, what is known so far, and what happens next. Markdown. It is all the new chat starts from."},
			"share_with": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Podium people to invite, by email address. Optional."},
			"access": map[string]any{"type": "string", "enum": []string{"restricted", "members", "link"},
				"description": "Who else may open it. restricted (default): only the people named. members: every " +
					"Podium member. link: anyone with the link."},
			"role": map[string]any{"type": "string", "enum": []string{"editor", "viewer"},
				"description": "What the people invited may do. editor (default) may write and ask you; viewer may read."},
		},
		"required":             []string{"title", "opening"},
		"additionalProperties": false,
	},
}

// StartChatArgs reads start_chat's arguments.
func StartChatArgs(args map[string]any) (StartChatRequest, error) {
	var req StartChatRequest
	req.Title, _ = args["title"].(string)
	req.Opening, _ = args["opening"].(string)
	req.Access, _ = args["access"].(string)
	req.Role, _ = args["role"].(string)
	switch v := args["share_with"].(type) {
	case nil:
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return req, invalid(errors.New("share_with is a list of email addresses"))
			}
			req.ShareWith = append(req.ShareWith, s)
		}
	case string:
		req.ShareWith = append(req.ShareWith, strings.Split(v, ",")...)
	default:
		return req, invalid(errors.New("share_with is a list of email addresses"))
	}
	return req, nil
}

// DescribeStartedChat is what start_chat answers.
func DescribeStartedChat(s StartedChat) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened the chat %q.\nchat_id: %s\nurl: %s\n", s.Chat.Title, s.Chat.ID, s.URL)
	switch {
	case len(s.SharedWith) > 0:
		fmt.Fprintf(&b, "shared with: %s (each was notified)\n", strings.Join(s.SharedWith, ", "))
	default:
		b.WriteString("shared with: nobody by name\n")
	}
	fmt.Fprintf(&b, "access: %s\n\n", s.Chat.GeneralAccess)
	b.WriteString("Put the link in your answer, e.g. \"[click here to join](" + s.URL + ")\". " +
		"The new conversation continues there; do not repeat its opening here.")
	return b.String()
}
