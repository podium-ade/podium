package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// UpsertUser records a login the first time it is seen, and refreshes the display name
// afterwards. There is no password: identity comes from Tailscale's WhoIs or a Google
// session, so this row exists to hang roles and audit off, not to authenticate anybody.
//
// hostedDomain is recorded on first write only (a later Google hd does not overwrite a
// tailnet-inferred one, and vice versa). An empty value is ignored.
func (s *Store) UpsertUser(ctx context.Context, login, displayName string) (User, error) {
	return s.upsertUser(ctx, login, displayName, EmailDomain(login))
}

// UpsertGoogleUser records a Google Workspace identity. After the instance is claimed, a
// brand-new login from the same domain is given RoleMember; an unclaimed instance leaves
// roles empty until ClaimInstance.
func (s *Store) UpsertGoogleUser(ctx context.Context, login, displayName, hostedDomain string) (User, error) {
	user, err := s.upsertUser(ctx, login, displayName, hostedDomain)
	if err != nil {
		return User{}, err
	}
	inst, err := s.GetInstance(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return user, nil
		}
		return User{}, err
	}
	if !strings.EqualFold(hostedDomain, inst.HostedDomain) {
		return User{}, fmt.Errorf("upsert google user %s: %w", login, ErrDomainMismatch)
	}
	if len(user.Roles) == 0 {
		return s.SetUserRoles(ctx, login, []string{RoleMember})
	}
	return user, nil
}

func (s *Store) upsertUser(ctx context.Context, login, displayName, hostedDomain string) (User, error) {
	if login == "" {
		return User{}, fmt.Errorf("upsert user: login is required")
	}
	row, err := s.q.UpsertUser(ctx, db.UpsertUserParams{
		Login:        login,
		DisplayName:  ptr(displayName),
		HostedDomain: ptr(strings.ToLower(strings.TrimSpace(hostedDomain))),
	})
	if err != nil {
		return User{}, fmt.Errorf("upsert user %s: %w", login, err)
	}
	return userFromRow(row), nil
}

// GetUser returns one user, or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, login string) (User, error) {
	row, err := s.q.GetUser(ctx, login)
	if noRows(err) {
		return User{}, fmt.Errorf("user %s: %w", login, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("select user %s: %w", login, err)
	}
	return userFromRow(row), nil
}

// SetUserRoles replaces the role list on a user. Empty is allowed (an unclaimed human).
func (s *Store) SetUserRoles(ctx context.Context, login string, roles []string) (User, error) {
	if login == "" {
		return User{}, fmt.Errorf("set user roles: login is required")
	}
	if roles == nil {
		roles = []string{}
	}
	row, err := s.q.SetUserRoles(ctx, db.SetUserRolesParams{Login: login, Roles: roles})
	if noRows(err) {
		return User{}, fmt.Errorf("user %s: %w", login, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("set roles for %s: %w", login, err)
	}
	return userFromRow(row), nil
}

// UserDomain is the Workspace (or email) domain this login would claim or must match.
// HostedDomain wins when set; otherwise the email domain. Empty means they cannot claim.
func UserDomain(u User) string {
	if u.HostedDomain != "" {
		return u.HostedDomain
	}
	return EmailDomain(u.Login)
}

// EmailDomain returns the lowercased domain of an email login, or empty when there isn't
// one we would let bind an instance. Consumer Gmail is rejected: there is no Workspace to
// claim, and binding to gmail.com would let anyone with a Gmail account in.
func EmailDomain(login string) string {
	_, domain, ok := strings.Cut(login, "@")
	if !ok {
		return ""
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || !strings.Contains(domain, ".") {
		return ""
	}
	switch domain {
	case "gmail.com", "googlemail.com":
		return ""
	}
	return domain
}

func userFromRow(row db.User) User {
	return User{
		Login:        row.Login,
		DisplayName:  deref(row.DisplayName),
		Roles:        row.Roles,
		HostedDomain: deref(row.HostedDomain),
		FirstSeenAt:  row.FirstSeenAt.UTC(),
	}
}
