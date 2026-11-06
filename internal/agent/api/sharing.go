package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// Phase 5 of docs/agent.md: a chat is shared the way a document is.
//
// The rule for who may do what is store.ChatRole (0035_chat_sharing.sql's chat_role), and
// every handler here asks it rather than reasoning about rows. The owner is chats.login and
// is the only one who may change who else has access; an editor may write the chat (post,
// tag Podium, rename it, retry its deliveries, stop its turns); a viewer may read it.

// ChatAuditor writes the sharing decisions somebody made to the control plane's audit_log
// (podium-server), which is where every other "who did what" already lives. Optional: a
// conductor without a control plane, or a test, leaves it nil and nothing is written.
type ChatAuditor interface {
	AuditChat(ctx context.Context, actor, action, chatID string, details map[string]any) error
}

// Audit actions. They are all "chat." so the control plane can refuse anything else from
// the conductor's credential.
const (
	AuditChatShare         = "chat.share"
	AuditChatUnshare       = "chat.unshare"
	AuditChatGeneralAccess = "chat.general_access"
	AuditChatLinkReset     = "chat.link_reset"
	AuditChatJoin          = "chat.join"
	AuditChatLeave         = "chat.leave"
	AuditChatDelete        = "chat.delete"
)

// auditTimeout bounds one audit write. An audit that cannot be written is logged and the
// action stands: the person did what they did, and refusing them after the fact because a
// second service was slow would be worse than a missing row, which the log line records.
const auditTimeout = 5 * time.Second

func (s *AgentService) auditChat(ctx context.Context, actor, action, chatID string, details map[string]any) {
	if s.auditor == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := s.auditor.AuditChat(actx, actor, action, chatID, details); err != nil {
		s.logger.WarnContext(ctx, "writing a chat audit entry failed; the action stands",
			"action", action, "chat_id", chatID, "actor", actor, "error", err)
	}
}

// chatRole is the caller's role on a chat. The dev token owns everything it can see; the
// owner is answered from the row without a query, because that is every web chat's case.
// typedLogin is a login somebody typed, in the spelling podium-server gave that person (0040):
// "Dana@acme.com" for the dana@acme.com who signed in. Somebody who never signed in keeps what
// was typed, trimmed, and is refused where a signed-in person is required.
func (s *AgentService) typedLogin(ctx context.Context, typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	login, ok, err := s.store.CanonicalWebLogin(ctx, typed)
	if err != nil || !ok {
		return typed, err
	}
	return login, nil
}

func (s *AgentService) chatRole(ctx context.Context, c store.Chat, login string) (string, error) {
	if SeesAll(ctx) {
		return store.RoleOwner, nil
	}
	if login == "" || login == "unknown" || c.DeletedAt != nil {
		// A deleted mirrored chat (0036) is nobody's, its owner included.
		return "", nil
	}
	if c.Login != "" && c.Login == login {
		return store.RoleOwner, nil
	}
	role, err := s.store.ChatRole(ctx, c.ID, login)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return role, err
}

// openChat is chatRole for somebody opening a chat, which is the one moment general access
// turns into a row of its own. A valid link token, or members access, writes the opener a
// chat_access row with the chat's general_role — so the chat is in their lists from then on,
// and stays there if general access is narrowed later (a document works the same way).
func (s *AgentService) openChat(ctx context.Context, c store.Chat, login, linkToken string) (string, error) {
	role, err := s.chatRole(ctx, c, login)
	if err != nil || SeesAll(ctx) || role == store.RoleOwner {
		return role, err
	}
	via := ""
	switch {
	case role == "" && validLinkToken(c, linkToken):
		via = store.GrantedByLink
	case role != "":
		// A role with no row behind it in the caller's lists is members access: they can open
		// it but it is listed under Workspace. Opening it is what moves it into their own.
		listed, err := s.store.ChatVisibleTo(ctx, c.ID, login)
		if err != nil {
			return role, err
		}
		if listed {
			return role, nil
		}
		via = store.GrantedByMembers
	default:
		return "", nil
	}
	joined, err := s.store.JoinChat(ctx, c.ID, login, c.GeneralRole, via)
	if err != nil {
		return role, err
	}
	if joined {
		s.auditChat(ctx, login, AuditChatJoin, c.ID, map[string]any{"via": via, "role": c.GeneralRole})
	}
	return s.chatRole(ctx, c, login)
}

// validLinkToken compares in constant time. A chat that is not shared by link has no valid
// token, whatever is presented: narrowing general access is how an owner stops the link from
// working without having to reset it.
func validLinkToken(c store.Chat, token string) bool {
	if c.GeneralAccess != store.AccessLink || c.LinkToken == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.LinkToken), []byte(token)) == 1
}

// mayWriteChat is the check every write RPC that is not a send shares: NotFound for a chat
// the caller cannot see, PermissionDenied for a viewer.
func (s *AgentService) mayWriteChat(ctx context.Context, c store.Chat, login string) error {
	role, err := s.chatRole(ctx, c, login)
	if err != nil {
		return storeError(err)
	}
	switch {
	case store.MayWrite(role):
		return nil
	case store.MayRead(role):
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("chat %s: you have view access", c.ID))
	default:
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, c.ID))
	}
}

// ownedChat reads a chat and refuses anybody but its owner, for the RPCs that change who
// has access. A chat the caller cannot see is NotFound; one they can see is PermissionDenied.
func (s *AgentService) ownedChat(ctx context.Context, chatID, login string) (store.Chat, error) {
	if chatID == "" {
		return store.Chat{}, connect.NewError(connect.CodeInvalidArgument, errors.New("chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return store.Chat{}, storeError(err)
	}
	role, err := s.chatRole(ctx, row, login)
	if err != nil {
		return store.Chat{}, storeError(err)
	}
	switch role {
	case store.RoleOwner:
		return row, nil
	case "":
		return store.Chat{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	default:
		return store.Chat{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("chat %s: only its owner can change who has access", chatID))
	}
}

// sourcePrivacy is what the share dialog warns about: sharing a mirrored Slack conversation
// shows its content to people who may not be in it. What Slack said about the channel (0037)
// is the answer when the chat has it: a private channel, a DM or a group DM is private, and a
// public channel is not. A chat recorded before that falls back to the id, which says enough
// for a DM (D…) and a legacy private channel or group DM (G…); a C… channel may be either and
// is warned about as possibly private. A pull request is as visible as its repository, which
// Podium does not know — GitHub's own access is the boundary, and nobody reaches a PR's chat
// here without having been in the PR.
func sourcePrivacy(c store.Chat) (private, unknown bool) {
	if c.Origin != store.OriginSlack {
		return false, false
	}
	switch c.ChannelPrivacy {
	case store.ChannelPublic:
		return false, false
	case store.ChannelPrivate, store.ChannelIM, store.ChannelMPIM:
		return true, false
	}
	id, ok := store.SlackChannelID(c.SourceKey)
	if !ok || id == "" {
		return false, true
	}
	switch id[0] {
	case 'D', 'G':
		return true, false
	default:
		return false, true
	}
}

func (s *AgentService) sharingToProto(ctx context.Context, c store.Chat, role string) (*agentv1.ChatSharing, error) {
	grants, err := s.store.ListChatAccess(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	out := &agentv1.ChatSharing{
		ChatId:        c.ID,
		Owner:         &agentv1.ChatGrant{Login: c.Login, Role: store.RoleOwner},
		GeneralAccess: c.GeneralAccess,
		GeneralRole:   c.GeneralRole,
		MyRole:        role,
		Origin:        c.Origin,
	}
	out.PrivateSource, out.SourcePrivacyUnknown = sourcePrivacy(c)
	if c.Login != "" {
		name, avatar, err := s.store.MemberProfile(ctx, c.Login)
		if err != nil {
			return nil, err
		}
		out.Owner.Name, out.Owner.Avatar = name, avatar
	}
	// The link is the owner's to hand out. Anybody else who can open the dialog is shown who
	// has access, never the secret that grants it.
	if role == store.RoleOwner {
		out.LinkToken = c.LinkToken
	}
	for _, g := range grants {
		out.Grants = append(out.Grants, &agentv1.ChatGrant{
			Login: g.Login, Name: g.Name, Avatar: g.Avatar, Role: g.Role,
			GrantedBy: g.GrantedBy, GrantedAt: timestamppb.New(g.GrantedAt),
		})
	}
	return out, nil
}

// GetChatSharing is the share dialog, for anybody who may open the chat.
func (s *AgentService) GetChatSharing(
	ctx context.Context, req *connect.Request[agentv1.GetChatSharingRequest],
) (*connect.Response[agentv1.GetChatSharingResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetChatId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get chat sharing: chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, storeError(err)
	}
	role, err := s.chatRole(ctx, row, login)
	if err != nil {
		return nil, storeError(err)
	}
	if !store.MayRead(role) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, row.ID))
	}
	out, err := s.sharingToProto(ctx, row, role)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.GetChatSharingResponse{Sharing: out}), nil
}

// UpdateChatSharing changes general access, its role, and the link. Owner only.
func (s *AgentService) UpdateChatSharing(
	ctx context.Context, req *connect.Request[agentv1.UpdateChatSharingRequest],
) (*connect.Response[agentv1.UpdateChatSharingResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.ownedChat(ctx, req.Msg.GetChatId(), login)
	if err != nil {
		return nil, err
	}
	access, role := req.Msg.GetGeneralAccess(), req.Msg.GetGeneralRole()
	if access == "" {
		access = row.GeneralAccess
	}
	if role == "" {
		role = row.GeneralRole
	}
	if access != row.GeneralAccess || role != row.GeneralRole {
		before := map[string]any{"access": row.GeneralAccess, "role": row.GeneralRole}
		if row, err = s.store.SetChatGeneralAccess(ctx, row.ID, access, role); err != nil {
			return nil, storeError(err)
		}
		s.auditChat(ctx, login, AuditChatGeneralAccess, row.ID, map[string]any{
			"from": before, "access": access, "role": role,
		})
	}
	// Turning link sharing on for the first time needs a link to share; resetting one is
	// asking for a new one. Either way the old token, if any, stops working.
	if req.Msg.GetRotateLink() || (row.GeneralAccess == store.AccessLink && row.LinkToken == "") {
		hadOne := row.LinkToken != ""
		if row, err = s.store.RotateChatLinkToken(ctx, row.ID); err != nil {
			return nil, storeError(err)
		}
		if hadOne {
			s.auditChat(ctx, login, AuditChatLinkReset, row.ID, nil)
		}
	}
	if row.GeneralAccess != store.AccessRestricted {
		s.chat.Multiplayer(ctx, row.ID, true, "")
	}
	s.chat.ChatChanged(ctx, row.ID)
	out, err := s.sharingToProto(ctx, row, store.RoleOwner)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.UpdateChatSharingResponse{Sharing: out}), nil
}

// ShareChat grants a Podium person a role. Owner only.
func (s *AgentService) ShareChat(
	ctx context.Context, req *connect.Request[agentv1.ShareChatRequest],
) (*connect.Response[agentv1.ShareChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.ownedChat(ctx, req.Msg.GetChatId(), login)
	if err != nil {
		return nil, err
	}
	target, err := s.typedLogin(ctx, req.Msg.GetLogin())
	if err != nil {
		return nil, storeError(err)
	}
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("share chat: login is required"))
	}
	if strings.EqualFold(target, row.Login) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("share chat: that is the chat's owner"))
	}
	first, err := s.store.ShareChat(ctx, row.ID, target, req.Msg.GetRole(), login)
	if err != nil {
		return nil, storeError(err)
	}
	s.auditChat(ctx, login, AuditChatShare, row.ID, map[string]any{
		"login": target, "role": req.Msg.GetRole(), "first": first,
	})
	s.chat.Multiplayer(ctx, row.ID, true, "")
	s.chat.ChatChanged(ctx, row.ID)
	out, err := s.sharingToProto(ctx, row, store.RoleOwner)
	if err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a chat was shared", "chat_id", row.ID, "with", target, "role", req.Msg.GetRole())
	return connect.NewResponse(&agentv1.ShareChatResponse{Sharing: out}), nil
}

// UnshareChat removes a named grant. Owner only. Somebody who has spoken in the chat stays a
// participant: unsharing takes back what the owner handed out, not a seat in a conversation.
func (s *AgentService) UnshareChat(
	ctx context.Context, req *connect.Request[agentv1.UnshareChatRequest],
) (*connect.Response[agentv1.UnshareChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.ownedChat(ctx, req.Msg.GetChatId(), login)
	if err != nil {
		return nil, err
	}
	target, err := s.typedLogin(ctx, req.Msg.GetLogin())
	if err != nil {
		return nil, storeError(err)
	}
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unshare chat: login is required"))
	}
	removed, err := s.store.UnshareChat(ctx, row.ID, target)
	if err != nil {
		return nil, storeError(err)
	}
	if removed {
		s.auditChat(ctx, login, AuditChatUnshare, row.ID, map[string]any{"login": target})
		s.chat.ChatChanged(ctx, row.ID)
	}
	out, err := s.sharingToProto(ctx, row, store.RoleOwner)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.UnshareChatResponse{Sharing: out}), nil
}

// LeaveChat takes the caller out of somebody else's chat.
func (s *AgentService) LeaveChat(
	ctx context.Context, req *connect.Request[agentv1.LeaveChatRequest],
) (*connect.Response[agentv1.LeaveChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetChatId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("leave chat: chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, storeError(err)
	}
	if row.Login == login {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("leave chat: you own this chat; delete it instead"))
	}
	removed, err := s.store.LeaveChat(ctx, row.ID, login)
	if err != nil {
		return nil, storeError(err)
	}
	if !removed {
		// Nothing of theirs was in it: a chat they can only open through members access, or
		// one they cannot see. Neither is something to leave.
		role, err := s.chatRole(ctx, row, login)
		if err != nil {
			return nil, storeError(err)
		}
		if role == "" {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, row.ID))
		}
		return connect.NewResponse(&agentv1.LeaveChatResponse{}), nil
	}
	s.auditChat(ctx, login, AuditChatLeave, row.ID, nil)
	s.chat.ChatChanged(ctx, row.ID)
	return connect.NewResponse(&agentv1.LeaveChatResponse{}), nil
}

// MarkChatRead moves the caller's read mark forward.
func (s *AgentService) MarkChatRead(
	ctx context.Context, req *connect.Request[agentv1.MarkChatReadRequest],
) (*connect.Response[agentv1.MarkChatReadResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetChatId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("mark chat read: chat_id is required"))
	}
	// The dev token has no per-person read state: it is not a person.
	if SeesAll(ctx) {
		return connect.NewResponse(&agentv1.MarkChatReadResponse{}), nil
	}
	row, err := s.store.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, storeError(err)
	}
	role, err := s.chatRole(ctx, row, login)
	if err != nil {
		return nil, storeError(err)
	}
	if !store.MayRead(role) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, row.ID))
	}
	if err := s.store.MarkChatRead(ctx, row.ID, login, req.Msg.GetSeq()); err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.MarkChatReadResponse{}), nil
}

// ListNotifications is the caller's inbox.
func (s *AgentService) ListNotifications(
	ctx context.Context, req *connect.Request[agentv1.ListNotificationsRequest],
) (*connect.Response[agentv1.ListNotificationsResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListNotifications(ctx, login, req.Msg.GetUnreadOnly(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Notification, 0, len(rows))
	for _, n := range rows {
		out = append(out, &agentv1.Notification{
			Id: n.ID, Kind: n.Kind, ChatId: n.ChatID, ChatTitle: n.ChatTitle, Seq: n.Seq,
			Actor: n.Actor, CreatedAt: timestamppb.New(n.CreatedAt), Read: n.Read,
		})
	}
	return connect.NewResponse(&agentv1.ListNotificationsResponse{Notifications: out}), nil
}

// MarkNotificationsRead marks some or all of the caller's notifications read.
func (s *AgentService) MarkNotificationsRead(
	ctx context.Context, req *connect.Request[agentv1.MarkNotificationsReadRequest],
) (*connect.Response[agentv1.MarkNotificationsReadResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.MarkNotificationsRead(ctx, login, req.Msg.GetIds(), req.Msg.GetAll()); err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.MarkNotificationsReadResponse{}), nil
}
