// Package auth is Google Workspace sign-in and the instance claim that hangs off it.
//
// It is not a transport. The local and tailnet listeners still name the caller the way they
// always have; this package adds a session cookie (Identify) and, when Google OAuth is
// configured, restricts humans on an unclaimed instance to WhoAmI and Claim. The local
// token and node identity stay fully privileged for the CLI and workers.
package auth

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

// CookieName is the session cookie set after a Google callback. HttpOnly, SameSite=Lax.
const CookieName = "podium_session"

// Paths the browser hits without a session. Mounted outside the identity middleware.
const (
	StatusPath   = "/auth/status"
	StartPath    = "/auth/google/start"
	CallbackPath = "/auth/google/callback"
	LogoutPath   = "/auth/logout"
	// PicturePath is the signed-in user's Google avatar, proxied same-origin so
	// tracking protection cannot hide it.
	PicturePath = "/auth/picture"
)

const (
	stateTTL     = 10 * time.Minute
	sessionTTL   = store.DefaultSessionTTL
	cookieMaxAge = int(sessionTTL / time.Second)
)

// Google is the OAuth client. Zero ClientID means Google sign-in is off and none of the
// /auth/google routes do anything useful.
type Google struct {
	ClientID     string
	ClientSecret string
	// PublicURL, if set, is the origin used for the OAuth redirect URI. Empty means the
	// start request's own origin, so a single client can have both localhost and a
	// production URI registered.
	PublicURL   string
	HTTPClient  *http.Client
	AuthURL     string
	TokenURL    string
	UserInfoURL string
}

// Enabled reports whether both halves of the OAuth client are set.
func (g Google) Enabled() bool {
	return g.ClientID != "" && g.ClientSecret != ""
}

func (g Google) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return http.DefaultClient
}

func (g Google) authURL() string {
	if g.AuthURL != "" {
		return g.AuthURL
	}
	return "https://accounts.google.com/o/oauth2/v2/auth"
}

func (g Google) tokenURL() string {
	if g.TokenURL != "" {
		return g.TokenURL
	}
	return "https://oauth2.googleapis.com/token"
}

func (g Google) userInfoURL() string {
	if g.UserInfoURL != "" {
		return g.UserInfoURL
	}
	return "https://openidconnect.googleapis.com/v1/userinfo"
}

func originOf(r *http.Request, publicURL string) string {
	req := requestOrigin(r)
	pub := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	// A production PublicURL always wins, including when the operator is hitting a
	// loopback tunnel: the Google client is registered for the public origin.
	if pub != "" && !loopbackOrigin(pub) {
		return pub
	}
	// Google treats localhost and 127.0.0.1 as different redirect URIs. When both
	// the browser and PublicURL are loopback, follow the request so a client that
	// registered both matches whichever the operator opened.
	if loopbackOrigin(req) {
		return req
	}
	if pub != "" {
		return pub
	}
	return req
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func loopbackOrigin(raw string) bool {
	if !strings.Contains(raw, "://") {
		return loopbackHost(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return loopbackHost(u.Host)
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = strings.Trim(hostport, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func redirectURI(origin string) string {
	return strings.TrimRight(origin, "/") + CallbackPath
}

func cookieSecure(r *http.Request, publicURL string) bool {
	if strings.HasPrefix(strings.ToLower(publicURL), "https://") {
		return true
	}
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, publicURL, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r, publicURL),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request, publicURL string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r, publicURL),
	})
}

// KindUser identity from a session. DisplayName is filled in by the caller from the user row.
func sessionIdentity(r *http.Request, login, displayName string) transport.Identity {
	return transport.Identity{
		Kind:        transport.KindUser,
		Login:       login,
		DisplayName: displayName,
		RemoteAddr:  r.RemoteAddr,
	}
}
