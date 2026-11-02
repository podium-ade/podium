package conductor

// Signed turn tokens.
//
// A turn token used to be 32 random bytes and an entry in this process's memory. That made it
// right for a turn that is a child of this process, and wrong for everything that is not: an
// assistant in a task outlives a restart of the conductor that started it, and with more than
// one conductor the call to delegate can land on a replica that never saw the token.
//
// So a token is now also signed, exactly as a git capability is (gitcred.go), with a key
// derived from the conductor's bearer — the same on every replica and across restarts. A
// replica that does not remember the token checks the signature, then checks the turn is still
// running in the database, which every replica shares. That check is the revocation: nothing
// has to remember to revoke a finished turn's token, and nothing can resurrect one.
//
// The in-memory map stays the fast path for the process that minted the token, and the only
// path when there is no mint secret.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// turnTokenPrefix marks a signed token, so one is never mistaken for a random one.
const turnTokenPrefix = "pt1"

// turnTokenKeyLabel domain-separates the turn token's key from the git capability's and from
// the bearer both are derived from.
const turnTokenKeyLabel = "podium-turn-token-v1"

// signedGrant is what a turn token carries. The source is named by its kind and resolved
// against the sources of whichever conductor verifies it.
type signedGrant struct {
	TurnID    string   `json:"t"`
	SessionID string   `json:"s"`
	Ref       string   `json:"r"`
	Source    string   `json:"k"`
	Playbooks []string `json:"p"`
	Login     string   `json:"l,omitempty"`
}

// signTurnToken is nonce (random, so no two tokens are alike), the grant, and their MAC.
func (c *Conductor) signTurnToken(nonce string, g turnGrant) string {
	kind := ""
	if g.src != nil {
		kind = g.src.Kind()
	}
	raw, _ := json.Marshal(signedGrant{
		TurnID: g.turnID, SessionID: g.sessionID, Ref: g.ref, Source: kind,
		Playbooks: g.playbooks, Login: g.login,
	})
	body := turnTokenPrefix + "." + nonce + "." + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + hex.EncodeToString(c.turnTokenMAC(body))
}

// verifyTurnToken answers for a signed token this process does not remember: a valid
// signature, a turn that is still running, and a source this conductor has.
func (c *Conductor) verifyTurnToken(ctx context.Context, token string) (turnGrant, bool) {
	if c.store == nil {
		return turnGrant{}, false
	}
	sg, ok := c.parseTurnToken(token)
	if !ok {
		return turnGrant{}, false
	}
	src := c.sourceOf(sg.Source)
	if src == nil {
		// Its work is announced in a conversation this conductor cannot post to.
		return turnGrant{}, false
	}
	running, err := c.workIsRunning(ctx, sg.TurnID)
	if err != nil || !running {
		return turnGrant{}, false
	}
	return turnGrant{
		turnID:    sg.TurnID,
		sessionID: sg.SessionID,
		ref:       sg.Ref,
		src:       src,
		playbooks: sg.Playbooks,
		login:     sg.Login,
	}, true
}

// parseTurnToken checks a signed token's signature and returns what was signed. It does not
// check the turn is still running; verifyTurnToken does.
func (c *Conductor) parseTurnToken(token string) (signedGrant, bool) {
	if c.mintSecret == "" {
		return signedGrant{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != turnTokenPrefix {
		return signedGrant{}, false
	}
	mac, err := hex.DecodeString(parts[3])
	if err != nil {
		return signedGrant{}, false
	}
	body := strings.Join(parts[:3], ".")
	// Constant time: the token is a value an untrusted turn supplies.
	if !hmac.Equal(mac, c.turnTokenMAC(body)) {
		return signedGrant{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return signedGrant{}, false
	}
	var sg signedGrant
	if err := json.Unmarshal(raw, &sg); err != nil || sg.TurnID == "" || sg.SessionID == "" {
		return signedGrant{}, false
	}
	return sg, true
}

func (c *Conductor) turnTokenMAC(body string) []byte {
	key := sha256.Sum256([]byte(turnTokenKeyLabel + "\x00" + c.mintSecret))
	m := hmac.New(sha256.New, key[:])
	_, _ = m.Write([]byte(body))
	return m.Sum(nil)
}
