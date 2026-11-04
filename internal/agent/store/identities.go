package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// The account providers an identity can be proven for. See 0031_identities.sql.
const (
	ProviderWeb    = "web"
	ProviderSlack  = "slack"
	ProviderGitHub = "github"
)

// Where a participant was first seen. JoinedBackfill is a migration's guess from a name,
// which later phases treat as read-only.
const (
	JoinedWeb      = "web"
	JoinedSlack    = "slack"
	JoinedGitHub   = "github"
	JoinedBackfill = "backfill"
)

// Identity is one account proven to be one Podium login.
type Identity struct {
	Login       string
	Provider    string
	ExternalID  string
	Handle      string
	DisplayName string
	AvatarURL   string
	VerifiedAt  time.Time
}

// SlackExternalID is the identities key of a Slack user: the workspace and the user, because a
// user id is only unique inside one workspace.
func SlackExternalID(teamID, userID string) string { return teamID + "/" + userID }

// UpsertIdentity records that an account is a login. The newest proof wins.
func (s *Store) UpsertIdentity(ctx context.Context, id Identity) error {
	switch {
	case strings.TrimSpace(id.Login) == "":
		return errors.New("upsert identity: a login is required")
	case id.Provider != ProviderWeb && id.Provider != ProviderSlack && id.Provider != ProviderGitHub:
		return fmt.Errorf("upsert identity: %q is not a provider", id.Provider)
	case strings.TrimSpace(id.ExternalID) == "":
		return errors.New("upsert identity: an external id is required")
	}
	at := id.VerifiedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.q.UpsertIdentity(ctx, db.UpsertIdentityParams{
		Login:       id.Login,
		Provider:    id.Provider,
		ExternalID:  id.ExternalID,
		Handle:      id.Handle,
		DisplayName: id.DisplayName,
		AvatarUrl:   id.AvatarURL,
		VerifiedAt:  at,
	}); err != nil {
		return fmt.Errorf("upsert %s identity of %s: %w", id.Provider, id.Login, err)
	}
	return nil
}

// IdentityLogin is the login an account was proven to be, or ErrNotFound.
func (s *Store) IdentityLogin(ctx context.Context, provider, externalID string) (string, error) {
	login, err := s.q.IdentityLogin(ctx, db.IdentityLoginParams{Provider: provider, ExternalID: externalID})
	if noRows(err) {
		return "", fmt.Errorf("%w: %s identity %s", ErrNotFound, provider, externalID)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s identity %s: %w", provider, externalID, err)
	}
	return login, nil
}

// HasWebIdentity reports whether this login has ever signed in to Podium.
func (s *Store) HasWebIdentity(ctx context.Context, login string) (bool, error) {
	ok, err := s.q.HasWebIdentity(ctx, login)
	if err != nil {
		return false, fmt.Errorf("look up the web identity of %s: %w", login, err)
	}
	return ok, nil
}

// DeleteIdentitiesOf removes every account of one provider that belongs to login.
func (s *Store) DeleteIdentitiesOf(ctx context.Context, provider, login string) error {
	if err := s.q.DeleteIdentitiesOf(ctx, db.DeleteIdentitiesOfParams{Provider: provider, Login: login}); err != nil {
		return fmt.Errorf("delete the %s identities of %s: %w", provider, login, err)
	}
	return nil
}

// IdentitiesOf returns every account of these logins.
func (s *Store) IdentitiesOf(ctx context.Context, logins []string) ([]Identity, error) {
	if len(logins) == 0 {
		return nil, nil
	}
	rows, err := s.q.IdentitiesOfLogins(ctx, logins)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		out = append(out, Identity{
			Login: r.Login, Provider: r.Provider, ExternalID: r.ExternalID, Handle: r.Handle,
			DisplayName: r.DisplayName, AvatarURL: r.AvatarUrl, VerifiedAt: r.VerifiedAt.UTC(),
		})
	}
	return out, nil
}

// AddChatParticipant records that login took part in a chat, and reports whether this was
// the first time.
func (s *Store) AddChatParticipant(ctx context.Context, chatID, login, via string, at time.Time) (bool, error) {
	if chatID == "" || login == "" {
		return false, errors.New("add chat participant: a chat id and a login are required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	inserted, err := s.q.AddChatParticipant(ctx, db.AddChatParticipantParams{
		ChatID: chatID, Login: login, JoinedVia: via, SeenAt: at.UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("add %s to chat %s: %w", login, chatID, err)
	}
	return inserted, nil
}

// ClaimChatOwner makes login the owner of a chat that has none, and reports whether it did.
// A chat that already has an owner keeps it.
func (s *Store) ClaimChatOwner(ctx context.Context, chatID, login string) (bool, error) {
	n, err := s.q.ClaimChatOwner(ctx, db.ClaimChatOwnerParams{ID: chatID, Login: &login})
	if err != nil {
		return false, fmt.Errorf("claim chat %s for %s: %w", chatID, login, err)
	}
	return n == 1, nil
}

// ChatVisibleTo reports whether viewer may read a chat: they own it or took part in it.
// A missing chat is ErrNotFound, so a handler can answer the same way for both.
func (s *Store) ChatVisibleTo(ctx context.Context, chatID, viewer string) (bool, error) {
	ok, err := s.q.ChatVisibleTo(ctx, db.ChatVisibleToParams{ID: chatID, Viewer: viewer})
	if noRows(err) {
		return false, fmt.Errorf("%w: chat %s", ErrNotFound, chatID)
	}
	if err != nil {
		return false, fmt.Errorf("check access to chat %s: %w", chatID, err)
	}
	return ok, nil
}

// ChatParticipant is one person in a conversation, as the UI names them. Login is empty for
// somebody who is not in Podium: they are known only by the name their messages carry.
type ChatParticipant struct {
	Login     string
	Name      string
	JoinedVia string
}

// ChatParticipants is everyone in a chat, first appearance first: the Podium people who
// took part, then the message authors who are not one of them.
//
// An author is matched to a participant by the names that participant's accounts carry — a
// Slack display name, a GitHub handle — because a message from before identities existed
// has only a name. The match only merges a name into a person already known to be in the
// chat; it never makes anybody a participant, which is what would need proof.
func (s *Store) ChatParticipants(ctx context.Context, chatID string) ([]ChatParticipant, error) {
	rows, err := s.q.ListChatParticipants(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list participants of chat %s: %w", chatID, err)
	}
	authors, err := s.q.ChatAuthors(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list authors of chat %s: %w", chatID, err)
	}
	logins := make([]string, 0, len(rows))
	for _, r := range rows {
		logins = append(logins, r.Login)
	}
	ids, err := s.IdentitiesOf(ctx, logins)
	if err != nil {
		return nil, err
	}
	names := map[string]string{} // login → best display name
	known := map[string]bool{}   // lowercased names that are a participant
	for _, id := range ids {
		for _, n := range []string{id.DisplayName, id.Handle} {
			if n = strings.TrimSpace(n); n != "" {
				known[strings.ToLower(n)] = true
				if names[id.Login] == "" {
					names[id.Login] = n
				}
			}
		}
	}
	out := make([]ChatParticipant, 0, len(rows)+len(authors))
	for _, r := range rows {
		known[strings.ToLower(r.Login)] = true
		out = append(out, ChatParticipant{
			Login: r.Login, Name: firstNonEmptyString(names[r.Login], r.Login), JoinedVia: r.JoinedVia,
		})
	}
	for _, a := range authors {
		if known[strings.ToLower(strings.TrimSpace(a))] {
			continue
		}
		known[strings.ToLower(strings.TrimSpace(a))] = true
		out = append(out, ChatParticipant{Name: a})
	}
	return out, nil
}

func firstNonEmptyString(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
