package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// A turn that pushes as the person who asked never holds that person's token. It redeems its
// capability, like an App turn, for a token GitHub scoped to the playbook's repositories.
// A non-expiring user token yields a non-expiring scoped one, so the conductor gives each
// its own lifetime and revokes it: when that lifetime passes, and when the turn ends.
//
// Every token issued is also kept in the secret store under the turn's id, so a turn that
// outlives a conductor restart still has its tokens revoked when it finishes.

// userTokenTTL is how long one scoped token is handed out before it is replaced, and
// userTokenRenewBefore when the replacement starts. The margin must stay above the
// runtime's own (gitcred.ts renewBeforeMs, ten minutes), for the reason github.renewBefore
// gives.
const (
	userTokenTTL         = time.Hour
	userTokenRenewBefore = 15 * time.Minute
)

// userTokenSet is the scoped tokens one turn has been issued and not yet revoked.
type userTokenSet struct {
	mu      sync.Mutex
	current github.Token
	issued  []github.Token
	// closed is a turn whose tokens were revoked. The turn's row may still read running for
	// a moment after, and a mint in that moment must not issue a token nothing will revoke.
	closed bool
	// loaded is whether issued includes what an earlier process recorded for this turn.
	loaded bool
}

func (c *Conductor) userTokenSet(turnID string, create bool) *userTokenSet {
	c.userTokensMu.Lock()
	defer c.userTokensMu.Unlock()
	set := c.userTokens[turnID]
	if set == nil && create {
		if c.userTokens == nil {
			c.userTokens = map[string]*userTokenSet{}
		}
		set = &userTokenSet{}
		c.userTokens[turnID] = set
	}
	return set
}

// mintUserToken answers a capability that names a person.
func (c *Conductor) mintUserToken(ctx context.Context, turnID string, scope GitScope) (GitCredential, error) {
	acct, err := c.store.GitHubAccount(ctx, scope.Login)
	if errors.Is(err, store.ErrNotFound) {
		return GitCredential{}, errors.New("the person this turn is for has disconnected GitHub")
	}
	if err != nil {
		return GitCredential{}, err
	}
	persona := githubPersonaOf(acct)
	credential := func(tok github.Token) GitCredential {
		return GitCredential{
			Token: tok.Value, Username: TokenUsername, ExpiresAt: tok.ExpiresAt,
			Identity: github.Identity{Name: persona.Name, Email: persona.Email},
		}
	}

	set := c.userTokenSet(turnID, true)
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return GitCredential{}, ErrBadCapability
	}
	if !set.loaded {
		recorded, err := c.recordedUserTokens(ctx, turnID)
		if err != nil {
			return GitCredential{}, err
		}
		set.issued, set.loaded = append(recorded, set.issued...), true
	}
	now := time.Now()
	if set.current.Value != "" && now.Before(set.current.ExpiresAt.Add(-userTokenRenewBefore)) {
		return credential(set.current), nil
	}

	tokens, err := c.userTokenClient(ctx)
	if err != nil {
		return GitCredential{}, err
	}
	userToken, err := c.podium.ReadSecret(ctx, scope.Login, profiles.GitHubTokenSecret)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return GitCredential{}, errors.New("the person this turn is for has disconnected GitHub")
	}
	if err != nil {
		return GitCredential{}, err
	}
	tok, err := tokens.Scope(ctx, string(userToken), scope.Owner, scope.Repos)
	zeroBytes(userToken)
	if errors.Is(err, github.ErrNotFound) {
		return GitCredential{}, errors.New("GitHub no longer accepts this person's connection; " +
			"they should reconnect it under Settings → Account")
	}
	if err != nil {
		return GitCredential{}, err
	}
	expires := now.Add(userTokenTTL)
	if !tok.ExpiresAt.IsZero() && tok.ExpiresAt.Before(expires) {
		expires = tok.ExpiresAt
	}
	tok.ExpiresAt = expires

	set.issued = append(set.issued, tok)
	if err := c.saveUserTokens(ctx, turnID, set.issued); err != nil {
		// A token nothing could revoke after a restart is not handed out.
		set.issued = set.issued[:len(set.issued)-1]
		if rerr := tokens.Revoke(ctx, tok.Value); rerr != nil {
			c.logger.ErrorContext(ctx, "revoking a scoped github token that could not be recorded failed",
				"turn_id", turnID, "error", rerr)
		}
		return GitCredential{}, err
	}
	set.current = tok
	c.revokeExpired(ctx, turnID, set, tokens, now)

	c.logger.InfoContext(ctx, "minted a scoped github token for a person's turn",
		"turn_id", turnID, "login", scope.Login, "owner", scope.Owner, "repos", scope.Repos,
		"expires_at", tok.ExpiresAt)
	return credential(tok), nil
}

// revokeExpired revokes the tokens whose lifetime has passed. The current one is never
// among them: it was just issued.
func (c *Conductor) revokeExpired(
	ctx context.Context, turnID string, set *userTokenSet, tokens github.UserTokens, now time.Time,
) {
	kept := set.issued[:0]
	revoked := false
	for _, tok := range set.issued {
		if tok.Value != set.current.Value && !now.Before(tok.ExpiresAt) {
			if err := tokens.Revoke(ctx, tok.Value); err != nil {
				c.logger.WarnContext(ctx, "revoking an expired scoped github token failed; it is "+
					"retried when the turn ends", "turn_id", turnID, "error", err)
				kept = append(kept, tok)
				continue
			}
			revoked = true
			continue
		}
		kept = append(kept, tok)
	}
	set.issued = kept
	if revoked {
		if err := c.saveUserTokens(ctx, turnID, set.issued); err != nil {
			c.logger.WarnContext(ctx, "recording a turn's scoped github tokens failed",
				"turn_id", turnID, "error", err)
		}
	}
}

// revokeUserTokens revokes every token a turn was issued. After a restart the list comes
// from the secret store rather than from memory.
func (c *Conductor) revokeUserTokens(ctx context.Context, turnID string) {
	set := c.userTokenSet(turnID, true)
	set.mu.Lock()
	values, loaded := set.issued, set.loaded
	set.issued, set.current, set.closed = nil, github.Token{}, true
	set.mu.Unlock()
	if !loaded {
		recorded, err := c.recordedUserTokens(ctx, turnID)
		if err != nil {
			c.logger.ErrorContext(ctx, "reading a turn's scoped github tokens failed; they were "+
				"not revoked", "turn_id", turnID, "error", err)
			return
		}
		values = append(recorded, values...)
	}
	if len(values) == 0 {
		return
	}

	tokens, err := c.userTokenClient(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "a turn's scoped github tokens could not be revoked", "turn_id", turnID,
			"tokens", len(values), "error", err)
		return
	}
	failed := 0
	for _, tok := range values {
		if err := tokens.Revoke(ctx, tok.Value); err != nil {
			failed++
			c.logger.ErrorContext(ctx, "revoking a scoped github token failed", "turn_id", turnID, "error", err)
		}
	}
	if failed > 0 {
		return
	}
	if err := c.podium.DeleteSecret(ctx, spec.UserGitTokenSecretPrefix+turnID); err != nil &&
		connect.CodeOf(err) != connect.CodeNotFound {
		c.logger.WarnContext(ctx, "deleting a turn's record of revoked github tokens failed",
			"turn_id", turnID, "error", err)
	}
	c.logger.InfoContext(ctx, "revoked a turn's scoped github tokens", "turn_id", turnID, "tokens", len(values))
}

// recordedUserTokens is what the secret store holds for a turn, which is what an earlier
// process issued.
func (c *Conductor) recordedUserTokens(ctx context.Context, turnID string) ([]github.Token, error) {
	raw, err := c.podium.ReadSecret(ctx, "", spec.UserGitTokenSecretPrefix+turnID)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer zeroBytes(raw)
	var out []github.Token
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("conductor: a turn's recorded github tokens do not decode: %w", err)
	}
	return out, nil
}

func (c *Conductor) saveUserTokens(ctx context.Context, turnID string, issued []github.Token) error {
	raw, err := json.Marshal(issued)
	if err != nil {
		return err
	}
	defer zeroBytes(raw)
	if _, err := c.podium.SetSecret(ctx, spec.UserGitTokenSecretPrefix+turnID, raw); err != nil {
		return fmt.Errorf("conductor: recording a turn's scoped github token: %w", err)
	}
	return nil
}

// userTokenClient is the App's OAuth client, read from the saved connection each time so a
// rotated client secret applies without a restart.
func (c *Conductor) userTokenClient(ctx context.Context) (github.UserTokens, error) {
	app, err := connections.LoadGitHub(ctx, c.store, c.podium)
	if err != nil {
		return github.UserTokens{}, err
	}
	if !app.UserAuth() {
		return github.UserTokens{}, errors.New("connecting GitHub accounts is turned off: the " +
			"GitHub App has no client id and secret under Settings → Connections")
	}
	return github.UserTokens{
		ClientID: app.ClientID, ClientSecret: app.ClientSecret, BaseURL: c.githubAPIURL,
	}, nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
