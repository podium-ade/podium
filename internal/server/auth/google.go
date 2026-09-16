package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

const (
	googleScope     = "openid email profile"
	userInfoMaxBody = 1 << 20
)

var (
	errNotWorkspace = errors.New("not a Google Workspace account")
	errUnverified   = errors.New("email is not verified")
)

type pendingAuth struct {
	verifier    string
	redirectURI string
	expiresAt   time.Time
}

// Flow holds in-flight OAuth states. Podium is a single process, so this lives in memory
// and a restart drops anything mid-login, which is the right answer.
type Flow struct {
	Google Google
	Store  *store.Store

	mu      sync.Mutex
	pending map[string]pendingAuth
}

func (f *Flow) putPending(state string, p pendingAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		f.pending = make(map[string]pendingAuth)
	}
	now := time.Now()
	for k, v := range f.pending {
		if now.After(v.expiresAt) {
			delete(f.pending, k)
		}
	}
	f.pending[state] = p
}

func (f *Flow) takePending(state string) (pendingAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pending[state]
	if ok {
		delete(f.pending, state)
	}
	if !ok || time.Now().After(p.expiresAt) {
		return pendingAuth{}, false
	}
	return p, true
}

func (f *Flow) startURL(r *http.Request, hostedDomain string) (string, error) {
	if !f.Google.Enabled() {
		return "", errors.New("google sign-in is not configured")
	}
	verifier, err := randomB64(32)
	if err != nil {
		return "", err
	}
	state, err := randomB64(24)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	origin := originOf(r, f.Google.PublicURL)
	redir := redirectURI(origin)
	f.putPending(state, pendingAuth{
		verifier:    verifier,
		redirectURI: redir,
		expiresAt:   time.Now().Add(stateTTL),
	})

	q := url.Values{
		"client_id":             {f.Google.ClientID},
		"redirect_uri":          {redir},
		"response_type":         {"code"},
		"scope":                 {googleScope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"online"},
		"prompt":                {"select_account"},
	}
	if hostedDomain != "" {
		q.Set("hd", hostedDomain)
	}
	return f.Google.authURL() + "?" + q.Encode(), nil
}

type googleUser struct {
	Email         string
	EmailVerified bool
	HD            string
	Name          string
	Picture       string
}

type userInfoJSON struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HD            string `json:"hd"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (f *Flow) exchange(ctx context.Context, code, verifier, redirectURI string) (googleUser, error) {
	form := url.Values{
		"client_id":     {f.Google.ClientID},
		"client_secret": {f.Google.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Google.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return googleUser{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := f.Google.httpClient().Do(req)
	if err != nil {
		return googleUser{}, fmt.Errorf("google token: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, userInfoMaxBody))
	if err != nil {
		return googleUser{}, fmt.Errorf("google token: read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return googleUser{}, fmt.Errorf("google token: HTTP %d: %s", res.StatusCode, trimForLog(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return googleUser{}, fmt.Errorf("google token: decode: %w", err)
	}
	if tok.AccessToken == "" {
		return googleUser{}, errors.New("google token: no access_token")
	}

	uiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Google.userInfoURL(), nil)
	if err != nil {
		return googleUser{}, err
	}
	uiReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uiRes, err := f.Google.httpClient().Do(uiReq)
	if err != nil {
		return googleUser{}, fmt.Errorf("google userinfo: %w", err)
	}
	defer uiRes.Body.Close()
	uiBody, err := io.ReadAll(io.LimitReader(uiRes.Body, userInfoMaxBody))
	if err != nil {
		return googleUser{}, fmt.Errorf("google userinfo: read: %w", err)
	}
	if uiRes.StatusCode != http.StatusOK {
		return googleUser{}, fmt.Errorf("google userinfo: HTTP %d: %s", uiRes.StatusCode, trimForLog(uiBody))
	}
	var info userInfoJSON
	if err := json.Unmarshal(uiBody, &info); err != nil {
		return googleUser{}, fmt.Errorf("google userinfo: decode: %w", err)
	}
	u := googleUser{
		Email:         strings.ToLower(strings.TrimSpace(info.Email)),
		EmailVerified: info.EmailVerified,
		HD:            strings.ToLower(strings.TrimSpace(info.HD)),
		Name:          strings.TrimSpace(info.Name),
		Picture:       httpsURL(info.Picture),
	}
	if u.Email == "" {
		return googleUser{}, errors.New("google userinfo: no email")
	}
	if !u.EmailVerified {
		return googleUser{}, errUnverified
	}
	if u.HD == "" || u.HD == "gmail.com" || u.HD == "googlemail.com" {
		return googleUser{}, errNotWorkspace
	}
	return u, nil
}

func httpsURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "https://") {
		return ""
	}
	return raw
}

func trimForLog(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
