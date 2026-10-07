package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	return s.upsertUser(ctx, login, displayName, EmailDomain(login), "")
}

// UpsertGoogleUser records a Google Workspace identity. After the instance is claimed, a
// brand-new login from the same domain is given RoleMember; an unclaimed instance leaves
// roles empty until ClaimInstance. pictureURL is the Google avatar; empty is ignored.
func (s *Store) UpsertGoogleUser(ctx context.Context, login, displayName, hostedDomain, pictureURL string) (User, error) {
	user, err := s.upsertUser(ctx, login, displayName, hostedDomain, pictureURL)
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

func (s *Store) upsertUser(ctx context.Context, login, displayName, hostedDomain, pictureURL string) (User, error) {
	if login == "" {
		return User{}, fmt.Errorf("upsert user: login is required")
	}
	row, err := s.q.UpsertUser(ctx, db.UpsertUserParams{
		Login:        login,
		DisplayName:  ptr(displayName),
		HostedDomain: ptr(strings.ToLower(strings.TrimSpace(hostedDomain))),
		PictureUrl:   ptr(pictureURL),
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

// TouchLastSeen records that login is here, at most once a minute. Identify uses it
// for a live Google session, which does not go through UpsertUser.
func (s *Store) TouchLastSeen(ctx context.Context, login string) error {
	if login == "" {
		return nil
	}
	if err := s.q.TouchUserLastSeen(ctx, login); err != nil {
		return fmt.Errorf("touch last seen %s: %w", login, err)
	}
	return nil
}

// ListUsers returns every recorded login, most recently seen first. The list is the Users screen.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userFromRow(row))
	}
	return out, nil
}

// AssignRole replaces a login's roles with exactly one of owner, admin, member.
// Demoting the last owner is ErrLastOwner: the Users screen would have no one left
// who can promote anyone, and the instance would be stuck.
func (s *Store) AssignRole(ctx context.Context, login, role string) (User, error) {
	if login == "" {
		return User{}, fmt.Errorf("assign role: login is required")
	}
	if !ValidRole(role) {
		return User{}, fmt.Errorf("assign role %q: %w", role, ErrInvalidRole)
	}

	var out User
	err := s.inTx(ctx, func(q *db.Queries) error {
		// Lock every owner before writing so two concurrent demotions cannot leave
		// the instance with none. Promoting someone does not need that lock.
		if role != RoleOwner {
			owners, err := q.ListOwnerLoginsForUpdate(ctx)
			if err != nil {
				return fmt.Errorf("lock owners: %w", err)
			}
			if len(owners) == 1 && owners[0] == login {
				return ErrLastOwner
			}
		}
		updated, err := q.SetUserRoles(ctx, db.SetUserRolesParams{
			Login: login,
			Roles: []string{role},
		})
		if noRows(err) {
			return fmt.Errorf("user %s: %w", login, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("set roles for %s: %w", login, err)
		}
		out = userFromRow(updated)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return out, nil
}

// ValidRole reports whether role is one of the three the API accepts.
func ValidRole(role string) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleMember:
		return true
	default:
		return false
	}
}

// CanonicalRole is the highest role in the list. Empty is RoleMember: after a claim,
// a login with nothing assigned is a person, not an operator.
func CanonicalRole(roles []string) string {
	switch {
	case slices.Contains(roles, RoleOwner):
		return RoleOwner
	case slices.Contains(roles, RoleAdmin):
		return RoleAdmin
	default:
		return RoleMember
	}
}

// HasRole reports whether roles satisfy need. Owner satisfies admin and member;
// admin satisfies member; empty is member.
func HasRole(roles []string, need string) bool {
	got := CanonicalRole(roles)
	switch need {
	case RoleOwner:
		return got == RoleOwner
	case RoleAdmin:
		return got == RoleOwner || got == RoleAdmin
	case RoleMember:
		return true
	default:
		return false
	}
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
		PictureURL:   deref(row.PictureUrl),
		FirstSeenAt:  row.FirstSeenAt.UTC(),
		LastSeenAt:   row.LastSeenAt.UTC(),
	}
}
