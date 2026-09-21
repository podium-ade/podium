package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

// Handler serves /auth/*. Status is always public. The Google routes 404 when OAuth is off.
type Handler struct {
	Flow   *Flow
	Store  *store.Store
	Logger *slog.Logger
}

// NewHandler returns the /auth/* mux. flow may have Google disabled.
func NewHandler(flow *Flow, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Flow: flow, Store: flow.Store, Logger: logger}
}

// Register mounts the public /auth/* routes on mux. They sit outside the identity
// middleware: status is how the UI learns Google is on, and start/callback are the
// OAuth dance.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc(StatusPath, h.status)
	mux.HandleFunc(StartPath, h.start)
	mux.HandleFunc(CallbackPath, h.callback)
	mux.HandleFunc(LogoutPath, h.logout)
	mux.HandleFunc(PicturePath, h.picture)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := struct {
		Google       bool   `json:"google"`
		Claimed      bool   `json:"claimed"`
		HostedDomain string `json:"hosted_domain"`
	}{Google: h.Flow.Google.Enabled()}
	if h.Store != nil {
		if inst, err := h.Store.GetInstance(r.Context()); err == nil {
			out.Claimed = true
			out.HostedDomain = inst.HostedDomain
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.Flow.Google.Enabled() {
		http.NotFound(w, r)
		return
	}
	var hd string
	if h.Store != nil {
		if inst, err := h.Store.GetInstance(r.Context()); err == nil {
			hd = inst.HostedDomain
		}
	}
	loc, err := h.Flow.startURL(r, hd)
	if err != nil {
		h.Logger.WarnContext(r.Context(), "google start failed", "error", err)
		http.Error(w, "google sign-in is not available", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.Flow.Google.Enabled() {
		http.NotFound(w, r)
		return
	}
	origin := originOf(r, h.Flow.Google.PublicURL)
	fail := func(code string) {
		http.Redirect(w, r, origin+"/?auth_error="+url.QueryEscape(code), http.StatusFound)
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		if errParam == "access_denied" {
			fail("denied")
			return
		}
		fail("failed")
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		fail("failed")
		return
	}
	pending, ok := h.Flow.takePending(state)
	if !ok {
		fail("failed")
		return
	}

	user, err := h.Flow.exchange(r.Context(), code, pending.verifier, pending.redirectURI)
	if err != nil {
		h.Logger.WarnContext(r.Context(), "google callback failed", "error", err)
		switch {
		case errors.Is(err, errNotWorkspace), errors.Is(err, errUnverified):
			fail("no_workspace")
		default:
			fail("failed")
		}
		return
	}

	if h.Store == nil {
		fail("failed")
		return
	}
	if inst, err := h.Store.GetInstance(r.Context()); err == nil {
		if !strings.EqualFold(user.HD, inst.HostedDomain) {
			fail("domain")
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		h.Logger.WarnContext(r.Context(), "google callback: instance lookup", "error", err)
		fail("failed")
		return
	}

	if _, err := h.Store.UpsertGoogleUser(r.Context(), user.Email, user.Name, user.HD, user.Picture); err != nil {
		h.Logger.WarnContext(r.Context(), "google callback: upsert user", "error", err)
		if errors.Is(err, store.ErrDomainMismatch) {
			fail("domain")
			return
		}
		fail("failed")
		return
	}
	token, err := h.Store.CreateSession(r.Context(), user.Email, sessionTTL)
	if err != nil {
		h.Logger.WarnContext(r.Context(), "google callback: create session", "error", err)
		fail("failed")
		return
	}
	setSessionCookie(w, r, h.Flow.Google.PublicURL, token)
	h.Logger.InfoContext(r.Context(), "google sign-in", "login", user.Email, "hosted_domain", user.HD)
	http.Redirect(w, r, origin+"/", http.StatusFound)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if tok := sessionToken(r); tok != "" && h.Store != nil {
		if err := h.Store.DeleteSession(r.Context(), tok); err != nil {
			h.Logger.WarnContext(r.Context(), "logout: delete session", "error", err)
		}
	}
	clearSessionCookie(w, r, h.Flow.Google.PublicURL)
	if r.Method == http.MethodGet {
		http.Redirect(w, r, originOf(r, h.Flow.Google.PublicURL)+"/", http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const pictureMaxBody = 1 << 20

func (h *Handler) picture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok := sessionToken(r)
	if tok == "" || h.Store == nil {
		http.NotFound(w, r)
		return
	}
	sess, err := h.Store.GetSession(r.Context(), tok)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	user, err := h.Store.GetUser(r.Context(), sess.Login)
	if err != nil || user.PictureURL == "" {
		http.NotFound(w, r)
		return
	}
	src, err := url.Parse(user.PictureURL)
	if err != nil || src.Scheme != "https" || !googlePictureHost(src.Host) {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src.String(), nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		h.Logger.WarnContext(r.Context(), "avatar fetch failed", "error", err)
		http.Error(w, "avatar unavailable", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "avatar unavailable", http.StatusBadGateway)
		return
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "avatar unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(res.Body, pictureMaxBody))
}

func googlePictureHost(host string) bool {
	host = strings.ToLower(host)
	return host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com")
}
