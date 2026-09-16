package conductor

// Minting a turn's GitHub credential.
//
// A turn does not carry a GitHub token. It carries a CAPABILITY to ask for one — a string
// this conductor signed, naming the turn and the repositories that turn's playbook listed —
// and its git credential helper redeems that capability every time git asks for a password.
// So a clone at the start of a two-hour turn and a push at the end of it get two different
// tokens, each minted seconds before it is used.
//
// That is the whole reason for the indirection. A GitHub App installation token lives one
// hour and cannot be renewed, and an agent pushes at the END of its turn: a token handed to
// the container at the start would be dead exactly when it mattered.
//
// The capability is SIGNED AND NOT STORED. Three properties fall out of that and all three
// matter:
//
//   - It survives a conductor restart. Turns do — reconcile.go re-attaches to them — and a
//     token kept in a map would not, so a restart would silently cost every running turn its
//     ability to push.
//   - It cannot lie about its scope. The repository list is inside the signature, so a turn
//     that edits its own capability invalidates it rather than widening it.
//   - It is revoked by the turn ending. Mint checks the turn's status in the database, which
//     is the actual truth about whether the turn is still running, rather than a second copy
//     of it that could disagree.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// GitTokenEnv holds a turn's minting capability in its container's environment. The
// credential helper reads it at the moment git asks, exactly as the older GITHUB_TOKEN path
// did, so nothing about it reaches an argv or .git/config.
const GitTokenEnv = "PODIUM_GIT_CAPABILITY"

// mintKeyLabel domain-separates the capability's signing key from the conductor's API
// bearer it is derived from. Deriving rather than generating is deliberate: a key made at
// start-up would be a new key after every restart, which is the one property this design
// exists to avoid.
const mintKeyLabel = "podium-git-mint-v1"

var (
	// ErrNoGitApp means this conductor has no GitHub App configured, so there is nothing to
	// mint. A playbook still naming its own podium.agent.github_token is unaffected.
	ErrNoGitApp = errors.New("conductor: this conductor has no GitHub App configured")
	// ErrBadCapability is a capability that is malformed, not signed by this conductor, or
	// signed for a turn that is over. The three are one error on purpose: a caller
	// distinguishing them learns which of its guesses was closest.
	ErrBadCapability = errors.New("conductor: that git capability is not valid for a running turn")
)

// GitScope is what one capability may mint for: the account the App is installed on, and
// the repositories of that account the turn's playbook listed.
//
// It is inside the signature rather than looked up at mint time because a playbook can be
// edited mid-turn. The turn was told to work on the repositories its brief named, and those
// are the ones it should still be able to push to five minutes later.
type GitScope struct {
	Owner string   `json:"owner"`
	Repos []string `json:"repos"`
}

// GitCredential is one minted token and the identity commits made with it should carry.
type GitCredential struct {
	Token     string
	Username  string
	ExpiresAt time.Time
	Identity  github.Identity
}

// TokenUsername is the username half of an installation token's basic auth. GitHub ignores
// the value and requires the pair, and this is the one it documents.
const TokenUsername = "x-access-token"

// gitScopeOf is the scope a playbook's turns get. An empty scope means the playbook lists no
// repositories, which is not an error: nothing needs minting.
//
// Every repository must belong to ONE owner. A single installation token is scoped to a
// single installation, and the container has one credential helper: two owners would need
// two tokens and there is nowhere to put the second. A playbook that spans two accounts is
// refused here rather than half-working.
func gitScopeOf(playbook profiles.Playbook) (GitScope, error) {
	var scope GitScope
	for _, r := range playbook.Repos {
		owner, name, err := github.SplitRepoURL(r.URL)
		if err != nil {
			return GitScope{}, err
		}
		if scope.Owner == "" {
			scope.Owner = owner
		}
		if !strings.EqualFold(scope.Owner, owner) {
			return GitScope{}, fmt.Errorf("conductor: this playbook's repos span two accounts "+
				"(%s and %s), and one installation token cannot cover both: split the playbook, "+
				"or give it repositories of one owner", scope.Owner, owner)
		}
		scope.Repos = append(scope.Repos, name)
	}
	if len(scope.Repos) == 0 {
		return GitScope{}, nil
	}
	return scope, nil
}

// mintCapability signs one turn's authority to mint. The result travels as a secret; see
// provisionGitCapability.
func (c *Conductor) mintCapability(turnID string, scope GitScope) (string, error) {
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("conductor: encoding a git scope failed: %w", err)
	}
	body := turnID + "." + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + hex.EncodeToString(c.mintMAC(body)), nil
}

// parseCapability checks the signature and returns what was signed. It does NOT check that
// the turn is still running; Mint does that, against the database.
func (c *Conductor) parseCapability(capability string) (turnID string, scope GitScope, err error) {
	parts := strings.Split(capability, ".")
	if len(parts) != 3 {
		return "", GitScope{}, ErrBadCapability
	}
	mac, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", GitScope{}, ErrBadCapability
	}
	body := parts[0] + "." + parts[1]
	// Constant time: this is a signature check on a value an untrusted container supplies.
	if !hmac.Equal(mac, c.mintMAC(body)) {
		return "", GitScope{}, ErrBadCapability
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", GitScope{}, ErrBadCapability
	}
	if err := json.Unmarshal(raw, &scope); err != nil {
		return "", GitScope{}, ErrBadCapability
	}
	if parts[0] == "" || scope.Owner == "" || len(scope.Repos) == 0 {
		return "", GitScope{}, ErrBadCapability
	}
	return parts[0], scope, nil
}

func (c *Conductor) mintMAC(body string) []byte {
	key := sha256.Sum256([]byte(mintKeyLabel + "\x00" + c.mintSecret))
	m := hmac.New(sha256.New, key[:])
	_, _ = m.Write([]byte(body))
	return m.Sum(nil)
}

// MintGitToken redeems a capability for a GitHub installation token.
//
// It is the whole of what a task container may ask this conductor for, and it takes no
// arguments beyond the capability: the scope is what the conductor signed, so a turn cannot
// ask for a repository its playbook never named.
func (c *Conductor) MintGitToken(ctx context.Context, capability string) (GitCredential, error) {
	if c.github == nil {
		return GitCredential{}, ErrNoGitApp
	}
	turnID, scope, err := c.parseCapability(capability)
	if err != nil {
		return GitCredential{}, err
	}
	// The unit of work's own status is the revocation list: one that has finished —
	// succeeded, failed, cancelled, timed out — can no longer mint, without anything
	// having to remember to revoke it. That is what makes this safe to leave unstored.
	running, err := c.workIsRunning(ctx, turnID)
	switch {
	case err != nil:
		return GitCredential{}, err
	case !running:
		return GitCredential{}, ErrBadCapability
	}
	token, err := c.github.Token(ctx, scope.Owner, scope.Repos)
	if err != nil {
		return GitCredential{}, err
	}
	identity, err := c.github.Identity(ctx)
	if err != nil {
		return GitCredential{}, err
	}
	c.logger.InfoContext(ctx, "minted a github token for a turn",
		"turn_id", turnID, "owner", scope.Owner, "repos", scope.Repos,
		"expires_at", token.ExpiresAt)
	return GitCredential{
		Token:     token.Value,
		Username:  TokenUsername,
		ExpiresAt: token.ExpiresAt,
		Identity:  identity,
	}, nil
}

// GitCapabilitySecret is the name of the secret one turn's capability travels in.
func GitCapabilitySecret(turnID string) string {
	return profiles.GitCapabilityPrefix + turnID
}

// provisionGitCapability signs this turn's capability and registers it as a secret, so it
// reaches the container the way every other credential does: resolved by the control plane
// at assignment and handed to the node, never written into the task spec where anything
// that can read a task could read it too.
//
// It returns the secret's name, or "" when there is nothing to provision — no App, or a
// playbook with no github.com repositories — which is not an error: that is simply a
// playbook still on the older path.
func (c *Conductor) provisionGitCapability(
	ctx context.Context, turnID string, playbook profiles.Playbook,
) (string, error) {
	if c.github == nil {
		return "", nil
	}
	scope, err := gitScopeOf(playbook)
	if err != nil {
		return "", err
	}
	if len(scope.Repos) == 0 {
		return "", nil
	}
	capability, err := c.mintCapability(turnID, scope)
	if err != nil {
		return "", err
	}
	name := GitCapabilitySecret(turnID)
	if _, err := c.podium.SetSecret(ctx, name, []byte(capability)); err != nil {
		return "", fmt.Errorf("conductor: registering this turn's git capability: %w", err)
	}
	return name, nil
}

// dropGitCapability deletes a turn's capability secret. It is called when the turn ends and
// when the task it was written for never started.
//
// A failure is logged and nothing more: the capability is already useless, because Mint
// refuses one whose turn is no longer running. What is left behind is a row, not an
// authority.
func (c *Conductor) dropGitCapability(ctx context.Context, turnID string) {
	if c.github == nil {
		return
	}
	name := GitCapabilitySecret(turnID)
	if err := c.podium.DeleteSecret(ctx, name); err != nil {
		c.logger.WarnContext(ctx, "deleting a turn's git capability secret failed; it can no "+
			"longer mint, because the turn is over", "turn_id", turnID, "secret", name, "error", err)
	}
}

// workIsRunning reports whether the unit of work a capability names is still going.
//
// Two tables, because a capability is issued to both: an ordinary turn is a row in turns,
// and a task a host turn delegated is a row in delegations, whose id is what its brief
// carries as the turn id. They share a status vocabulary, so only the lookup differs.
func (c *Conductor) workIsRunning(ctx context.Context, id string) (bool, error) {
	turn, err := c.store.GetTurn(ctx, id)
	switch {
	case err == nil:
		return turn.Status == store.TurnRunning, nil
	case !errors.Is(err, store.ErrNotFound):
		return false, fmt.Errorf("conductor: reading turn %s: %w", id, err)
	}
	dlg, err := c.store.GetDelegation(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("conductor: reading delegation %s: %w", id, err)
	}
	return dlg.Status == store.TurnRunning, nil
}
