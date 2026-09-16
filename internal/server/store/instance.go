package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// GetInstance returns the claim row, or ErrNotFound when this control plane has no owner.
func (s *Store) GetInstance(ctx context.Context) (Instance, error) {
	row, err := s.q.GetInstance(ctx)
	if noRows(err) {
		return Instance{}, fmt.Errorf("instance: %w", ErrNotFound)
	}
	if err != nil {
		return Instance{}, fmt.Errorf("select instance: %w", err)
	}
	return instanceFromRow(row), nil
}

// ClaimInstance binds this control plane to hostedDomain and makes login the owner.
// Re-claiming the same domain as the same person is a no-op. A second person, or a
// different domain, is ErrAlreadyClaimed. The typed domain must match the caller's.
func (s *Store) ClaimInstance(ctx context.Context, login, hostedDomain string) (Instance, error) {
	login = strings.TrimSpace(login)
	hostedDomain = strings.ToLower(strings.TrimSpace(hostedDomain))
	if login == "" {
		return Instance{}, fmt.Errorf("claim instance: login is required")
	}
	if hostedDomain == "" {
		return Instance{}, fmt.Errorf("claim instance: hosted domain is required")
	}

	var out Instance
	err := s.inTx(ctx, func(q *db.Queries) error {
		existing, err := q.GetInstance(ctx)
		if err == nil {
			if strings.EqualFold(existing.ClaimedBy, login) && strings.EqualFold(existing.HostedDomain, hostedDomain) {
				out = instanceFromRow(existing)
				return nil
			}
			return ErrAlreadyClaimed
		}
		if !noRows(err) {
			return fmt.Errorf("select instance: %w", err)
		}

		user, err := q.GetUser(ctx, login)
		if noRows(err) {
			return fmt.Errorf("claim instance: user %s: %w", login, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("claim instance: select user %s: %w", login, err)
		}
		got := deref(user.HostedDomain)
		if got == "" {
			got = EmailDomain(user.Login)
		}
		if !strings.EqualFold(got, hostedDomain) {
			return ErrDomainMismatch
		}

		if _, err := q.SetUserRoles(ctx, db.SetUserRolesParams{
			Login: login,
			Roles: []string{RoleOwner},
		}); err != nil {
			return fmt.Errorf("claim instance: set owner role: %w", err)
		}
		row, err := q.InsertInstance(ctx, db.InsertInstanceParams{
			HostedDomain: hostedDomain,
			ClaimedBy:    login,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return ErrAlreadyClaimed
			}
			return fmt.Errorf("claim instance: insert: %w", err)
		}
		out = instanceFromRow(row)
		return nil
	})
	if err != nil {
		return Instance{}, err
	}
	return out, nil
}

func instanceFromRow(row db.Instance) Instance {
	return Instance{
		HostedDomain: row.HostedDomain,
		ClaimedBy:    row.ClaimedBy,
		ClaimedAt:    row.ClaimedAt.UTC(),
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
