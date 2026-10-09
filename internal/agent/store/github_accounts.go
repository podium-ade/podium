package store

import (
	"context"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// GitHubAccount is the GitHub account one Podium login connected. The token is that
// login's personal secret, not a field here.
type GitHubAccount struct {
	Login       string
	GitHubID    int64
	GitHubLogin string
	Name        string
	ConnectedAt time.Time
	// NeedsReconnect is an account whose refresh token GitHub refused.
	NeedsReconnect bool
}

// GitHubAccount reads one login's account. ErrNotFound means none is connected.
func (s *Store) GitHubAccount(ctx context.Context, login string) (GitHubAccount, error) {
	row, err := s.q.GetGitHubAccount(ctx, login)
	if noRows(err) {
		return GitHubAccount{}, fmt.Errorf("%w: github account for %s", ErrNotFound, login)
	}
	if err != nil {
		return GitHubAccount{}, fmt.Errorf("get github account for %s: %w", login, err)
	}
	return GitHubAccount{
		Login: row.Login, GitHubID: row.GithubID, GitHubLogin: row.GithubLogin,
		Name: row.Name, ConnectedAt: row.ConnectedAt, NeedsReconnect: row.NeedsReconnect,
	}, nil
}

// MarkGitHubAccountNeedsReconnect records that GitHub refused the account's refresh token.
func (s *Store) MarkGitHubAccountNeedsReconnect(ctx context.Context, login string) error {
	if err := s.q.SetGitHubAccountNeedsReconnect(ctx, login); err != nil {
		return fmt.Errorf("mark github account for %s: %w", login, err)
	}
	return nil
}

// PutGitHubAccount records a connection, replacing an earlier one.
func (s *Store) PutGitHubAccount(ctx context.Context, a GitHubAccount) error {
	if err := s.q.UpsertGitHubAccount(ctx, db.UpsertGitHubAccountParams{
		Login: a.Login, GithubID: a.GitHubID, GithubLogin: a.GitHubLogin,
		Name: a.Name, ConnectedAt: a.ConnectedAt,
	}); err != nil {
		return fmt.Errorf("put github account for %s: %w", a.Login, err)
	}
	return nil
}

// DeleteGitHubAccount removes a connection. One that was never made is not an error.
func (s *Store) DeleteGitHubAccount(ctx context.Context, login string) error {
	if err := s.q.DeleteGitHubAccount(ctx, login); err != nil {
		return fmt.Errorf("delete github account for %s: %w", login, err)
	}
	return nil
}
