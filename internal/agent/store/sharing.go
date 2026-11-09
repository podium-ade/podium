package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// Phase 5 of docs/agent.md: a chat is shared the way a document is. The
// rules live in SQL (0035_chat_sharing.sql: chat_role, chat_visible_to, chat_unread) so that
// every read agrees; this file is the writes and the typed answers.

// Roles a login has on a chat, from chat_role. The empty string is no access at all.
const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// General access: who beyond the named grants may open a chat.
const (
	AccessRestricted = "restricted"
	AccessMembers    = "members"
	AccessLink       = "link"
)

// Why a caller can see a chat, as a list reports it.
const (
	AccessOwner       = "owner"
	AccessParticipant = "participant"
	AccessShared      = "shared"
	AccessWorkspace   = "workspace"
)

// Sections of the chat and session lists. SectionMine is the default: everything in the
// caller's own lists. SectionShared is the part of that somebody else owns. SectionWorkspace
// is what is shared with every member and not already in the caller's lists.
const (
	SectionMine      = ""
	SectionShared    = "shared"
	SectionWorkspace = "workspace"
)

// Granted-by values for a chat_access row that opening the chat wrote, rather than a person.
const (
	GrantedByLink    = "link"
	GrantedByMembers = "members"
)

// ErrNoSuchMember is sharing with a login that has never signed in to Podium. A share is
// only ever with a Podium person: an email nobody has used would be a grant to whoever
// signs in with it later, decided by nobody.
var ErrNoSuchMember = errors.New("that person has not signed in to Podium")

// ErrInvalidAccess is a role or general-access value the schema does not have.
var ErrInvalidAccess = errors.New("invalid access")

// ErrInvalidSection is a list section that does not exist.
var ErrInvalidSection = errors.New("invalid list section")

func validSection(s string) bool {
	switch s {
	case SectionMine, SectionShared, SectionWorkspace:
		return true
	}
	return false
}

// MayWrite reports whether a role may post in a chat, start a turn in it, rename it, retry
// its deliveries and stop its turns.
func MayWrite(role string) bool { return role == RoleOwner || role == RoleEditor }

// MayRead reports whether a role may open a chat at all.
func MayRead(role string) bool { return role != "" }

// ChatRole is chat_role for one chat and one login: RoleOwner, RoleEditor, RoleViewer or "".
// ErrNotFound when the chat does not exist.
func (s *Store) ChatRole(ctx context.Context, chatID, viewer string) (string, error) {
	role, err := s.q.ChatRole(ctx, db.ChatRoleParams{ID: chatID, Viewer: viewer})
	if noRows(err) {
		return "", fmt.Errorf("%w: chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return "", fmt.Errorf("read the role of %s on chat %s: %w", viewer, chatID, err)
	}
	return role, nil
}

// ChatGrant is one named grant on a chat, with what the person's own accounts say about them.
type ChatGrant struct {
	Login     string
	Role      string
	GrantedBy string
	GrantedAt time.Time
	Name      string
	Avatar    string
}

// ShareChat grants login a role on a chat, by name. Sharing again replaces the role. A first
// grant notifies them; a change of role does not. The target must have signed in to Podium.
// It reports whether this was a first grant.
func (s *Store) ShareChat(ctx context.Context, chatID, login, role, by string) (bool, error) {
	if role != RoleEditor && role != RoleViewer {
		return false, fmt.Errorf("%w: role %q", ErrInvalidAccess, role)
	}
	ok, err := s.HasWebIdentity(ctx, login)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNoSuchMember, login)
	}
	inserted, err := s.q.ShareChatWith(ctx, db.ShareChatWithParams{
		ChatID: chatID, Login: login, Role: role, GrantedBy: by, GrantedAt: time.Now().UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("share chat %s with %s: %w", chatID, login, err)
	}
	if inserted {
		if err := s.q.NotifyShare(ctx, db.NotifyShareParams{Login: login, ChatID: chatID, Actor: by}); err != nil {
			return true, fmt.Errorf("notify %s of chat %s: %w", login, chatID, err)
		}
	}
	return inserted, nil
}

// JoinChat writes the access row that opening a chat by link or through members access
// leaves behind, so it is in the opener's lists afterwards. It never changes an existing row.
// It reports whether a row was written.
func (s *Store) JoinChat(ctx context.Context, chatID, login, role, via string) (bool, error) {
	if role != RoleEditor && role != RoleViewer {
		return false, fmt.Errorf("%w: role %q", ErrInvalidAccess, role)
	}
	n, err := s.q.JoinChatAccess(ctx, db.JoinChatAccessParams{
		ChatID: chatID, Login: login, Role: role, GrantedBy: via, GrantedAt: time.Now().UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("join chat %s as %s: %w", chatID, login, err)
	}
	return n > 0, nil
}

// UnshareChat removes a named grant. It reports whether there was one. A participant keeps
// what speaking gave them: unsharing is the owner taking back what they handed out, not
// throwing somebody out of a conversation they are in.
func (s *Store) UnshareChat(ctx context.Context, chatID, login string) (bool, error) {
	n, err := s.q.RevokeChatAccess(ctx, db.RevokeChatAccessParams{ChatID: chatID, Login: login})
	if err != nil {
		return false, fmt.Errorf("unshare chat %s with %s: %w", chatID, login, err)
	}
	return n > 0, nil
}

// LeaveChat takes login out of a chat that is not theirs: their grant and their participant
// row. It reports whether anything was removed. Speaking in the chat again rejoins them.
func (s *Store) LeaveChat(ctx context.Context, chatID, login string) (bool, error) {
	n, err := s.q.LeaveChat(ctx, db.LeaveChatParams{ChatID: chatID, Login: login})
	if err != nil {
		return false, fmt.Errorf("leave chat %s as %s: %w", chatID, login, err)
	}
	return n > 0, nil
}

// ListChatAccess is every named grant on a chat, oldest first.
func (s *Store) ListChatAccess(ctx context.Context, chatID string) ([]ChatGrant, error) {
	rows, err := s.q.ListChatAccess(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list who chat %s is shared with: %w", chatID, err)
	}
	out := make([]ChatGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChatGrant{
			Login: r.Login, Role: r.Role, GrantedBy: r.GrantedBy, GrantedAt: r.GrantedAt.UTC(),
			Name: r.Name, Avatar: r.Avatar,
		})
	}
	return out, nil
}

// SetChatGeneralAccess changes who beyond the named grants may open a chat, and as what.
func (s *Store) SetChatGeneralAccess(ctx context.Context, chatID, access, role string) (Chat, error) {
	switch access {
	case AccessRestricted, AccessMembers, AccessLink:
	default:
		return Chat{}, fmt.Errorf("%w: general access %q", ErrInvalidAccess, access)
	}
	if role != RoleEditor && role != RoleViewer {
		return Chat{}, fmt.Errorf("%w: role %q", ErrInvalidAccess, role)
	}
	row, err := s.q.SetChatGeneralAccess(ctx, db.SetChatGeneralAccessParams{
		ID: chatID, GeneralAccess: access, GeneralRole: role,
	})
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("set general access of chat %s: %w", chatID, err)
	}
	return chatFromRow(row), nil
}

// RotateChatLinkToken gives a chat a new link secret, which is also how link sharing gets its
// first one. Anybody holding the old link can no longer use it to join; anybody who already
// joined keeps their access row.
func (s *Store) RotateChatLinkToken(ctx context.Context, chatID string) (Chat, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Chat{}, fmt.Errorf("make a link token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	row, err := s.q.SetChatLinkToken(ctx, db.SetChatLinkTokenParams{ID: chatID, LinkToken: &token})
	if noRows(err) {
		return Chat{}, fmt.Errorf("%w: chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return Chat{}, fmt.Errorf("set the link token of chat %s: %w", chatID, err)
	}
	return chatFromRow(row), nil
}

// MarkChatRead moves login's read mark in a chat forward to seq. It never moves it back.
func (s *Store) MarkChatRead(ctx context.Context, chatID, login string, seq uint64) error {
	if err := s.q.MarkChatRead(ctx, db.MarkChatReadParams{
		ChatID: chatID, Login: login, Seq: int64(seq),
	}); err != nil {
		return fmt.Errorf("mark chat %s read for %s: %w", chatID, login, err)
	}
	return nil
}

// Notification kinds.
const (
	NotifyShare   = "share"
	NotifyMention = "mention"
)

// Notification is one entry in a person's in-app inbox.
type Notification struct {
	ID        int64
	Kind      string
	ChatID    string
	ChatTitle string
	Seq       uint64
	Actor     string
	// Detail is what a routine notice says happened.
	Detail    string
	CreatedAt time.Time
	Read      bool
}

// ListNotifications is login's inbox, newest first. A notification for a chat they can no
// longer open is left out.
func (s *Store) ListNotifications(ctx context.Context, login string, unreadOnly bool, limit int) ([]Notification, error) {
	rows, err := s.q.ListNotifications(ctx, db.ListNotificationsParams{
		Login: login, UnreadOnly: unreadOnly, PageLimit: int32(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list notifications of %s: %w", login, err)
	}
	out := make([]Notification, 0, len(rows))
	for _, r := range rows {
		out = append(out, Notification{
			ID: r.ID, Kind: r.Kind, ChatID: r.ChatID, ChatTitle: r.ChatTitle, Seq: uint64(r.Seq),
			Actor: r.Actor, Detail: r.Detail, CreatedAt: r.CreatedAt.UTC(), Read: r.ReadAt != nil,
		})
	}
	return out, nil
}

// MarkNotificationsRead marks the listed notifications of login read, or every unread one when
// all is set. Ids that are somebody else's are ignored. It returns how many changed.
func (s *Store) MarkNotificationsRead(ctx context.Context, login string, ids []int64, all bool) (int64, error) {
	if ids == nil {
		ids = []int64{}
	}
	n, err := s.q.MarkNotificationsRead(ctx, db.MarkNotificationsReadParams{
		Login: login, MarkAll: all, Ids: ids,
	})
	if err != nil {
		return 0, fmt.Errorf("mark notifications of %s read: %w", login, err)
	}
	return n, nil
}

// MemberProfile is one Podium person's display name and avatar, empty when their accounts
// carry none.
func (s *Store) MemberProfile(ctx context.Context, login string) (name, avatar string, err error) {
	row, err := s.q.MemberProfile(ctx, login)
	if err != nil {
		return "", "", fmt.Errorf("read the profile of %s: %w", login, err)
	}
	return row.Name, row.Avatar, nil
}
