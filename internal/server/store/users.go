package store

import (
	"context"
	"fmt"

	"github.com/alvaroibarguen/podium/internal/server/store/db"
)

// UpsertUser records a login the first time the tailnet transport sees it, and refreshes the
// display name afterwards. There is no password: identity comes from Tailscale's WhoIs, so this
// row exists to hang roles and audit off, not to authenticate anybody.
func (s *Store) UpsertUser(ctx context.Context, login, displayName string) (User, error) {
	if login == "" {
		return User{}, fmt.Errorf("upsert user: login is required")
	}
	row, err := s.q.UpsertUser(ctx, db.UpsertUserParams{Login: login, DisplayName: ptr(displayName)})
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

func userFromRow(row db.User) User {
	return User{
		Login:       row.Login,
		DisplayName: deref(row.DisplayName),
		Roles:       row.Roles,
		FirstSeenAt: row.FirstSeenAt.UTC(),
	}
}
