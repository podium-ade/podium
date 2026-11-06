// Package desktop streams a running task's desktop to a person's browser.
//
// A desktop playbook's task runs voxl on an exposed port named "desktop": a virtual display
// streamed as H.264 over one WebSocket. This package is the only way a browser reaches it. It
// is served on an origin of its own (PODIUM_DESKTOP_URL), never the UI's: voxl's web client is
// someone else's JavaScript, and on the UI's origin it could call Podium's API as the person
// watching. The UI's session cookie is host-only, so the desktop origin never sees it.
//
// The same task's terminal — ttyd on the exposed port "terminal", a shell in the workspace —
// is served the same way, with a link of its own.
//
// A person gets in with a link, not a cookie: TaskService.CreateDesktopTicket mints
// /t/<task>/<token>/ for a member who may see the task, one per port, and everything voxl or
// ttyd loads is relative to that path, so every request carries the token. A cookie would have to cross from the UI's
// site into an iframe on another, which third-party cookie blocking refuses. The token is
// signed by this process, names the task, the port and the login, and expires; the task must still be
// running for any request it makes to be answered.
package desktop

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenTTL is how long a desktop link works. A viewer that outlives it is closed when it
// next connects and the panel asks for a new one; a turn's desktop rarely lives longer.
const TokenTTL = 12 * time.Hour

// claims is what a token says. Short keys: it is in every request's path.
type claims struct {
	Task    string `json:"t"`
	Port    string `json:"p"`
	Login   string `json:"l"`
	Expires int64  `json:"e"`
}

// signer signs and checks tokens with a key that lives and dies with this process. podium-server
// is a single process, and a restart only means the panel fetches a new link.
type signer struct {
	key []byte
	now func() time.Time
}

func newSigner() (*signer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("desktop: a signing key: %w", err)
	}
	return &signer{key: key, now: time.Now}, nil
}

var errBadToken = errors.New("desktop: the link is not valid")

func (s *signer) sign(task, port, login string) (string, time.Time, error) {
	exp := s.now().Add(TokenTTL).Truncate(time.Second)
	body, err := json.Marshal(claims{Task: task, Port: port, Login: login, Expires: exp.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + s.mac(payload), exp, nil
}

// verify returns the token's claims if this process signed it, for this task, and it has not
// expired.
func (s *signer) verify(token, task string) (claims, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return claims{}, errBadToken
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return claims{}, errBadToken
	}
	var c claims
	if err := json.Unmarshal(body, &c); err != nil || c.Task != task || !servable(c.Port) {
		return claims{}, errBadToken
	}
	if !s.now().Before(time.Unix(c.Expires, 0)) {
		return claims{}, errBadToken
	}
	return c, nil
}

func (s *signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
