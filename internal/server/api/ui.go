package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/podium-ade/podium/web"
)

// contentSecurityPolicy is deliberately narrow: the bundle is self-contained, Tailwind is
// compiled at build time into one stylesheet, and the only network the page makes is Connect
// calls back to this origin. React applies dynamic styles through the CSSOM, which CSP does
// not govern. CodeMirror 6 is the exception: style-mod inserts a <style> tag, and style-src
// treats that as inline. Each index.html response mints a nonce, puts it on the policy, and
// stamps it on a <meta> the editor reads so that tag is allowed. Linked stylesheets stay on
// 'self'; scripts stay on 'self' with no nonce (the tag is a static module src).
//
// img-src allows blob: because a chat message's image attachment CANNOT be an <img src>
// pointing at /artifacts/{id}: that route is behind the identity middleware and an <img>
// carries no Authorization header. The page fetches the bytes itself (connect-src 'self',
// already allowed) and shows them from a blob URL. blob: does not widen what the page can
// reach — a blob's bytes came from a request this policy already permitted — it only lets
// the page display bytes it already has. Without it the browser blocks the image and the
// chat shows an empty box, which is how this was found.
func contentSecurityPolicy(styleNonce string) string {
	styleSrc := "style-src 'self'"
	if styleNonce != "" {
		styleSrc += " 'nonce-" + styleNonce + "'"
	}
	return "default-src 'self'; " +
		"script-src 'self'; " +
		styleSrc + "; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self'; " +
		"connect-src 'self'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'none'; " +
		"object-src 'none'"
}

const cspNonceMetaName = "podium-csp-nonce"

const (
	// immutableCache is safe because Vite fingerprints every file under assets/.
	immutableCache = "public, max-age=31536000, immutable"
	// indexCache: index.html names the current bundle, so it must never be cached.
	indexCache = "no-store"
	indexFile  = "index.html"
	assetsDir  = "assets/"
)

// NewUIHandler serves the embedded single-page app with an SPA fallback: any path that is not a
// real file is answered with index.html so the client router can take it. Mount it on "/" after
// the RPC prefixes — it is the last resort, and it is deliberately unauthenticated, because the
// bundle is not a secret and the API behind it still demands a token.
//
// A binary built with `-tags noui`, or one built before `make web` ever ran, has no index.html;
// it gets a handler that says so instead of a confusing 404.
func NewUIHandler(logger *slog.Logger) http.Handler {
	assets, err := web.FS()
	if err != nil {
		logger.Warn("web ui: asset tree unavailable", "error", err)
		return missingUI()
	}
	return newUIHandler(assets, logger)
}

func newUIHandler(assets fs.FS, logger *slog.Logger) http.Handler {
	if _, err := fs.Stat(assets, indexFile); err != nil {
		if !web.Enabled {
			logger.Info("web ui: built with -tags noui, serving the API only")
		} else {
			logger.Warn("web ui: no index.html in the embedded assets; run `make web`")
		}
		return missingUI()
	}
	return &uiHandler{assets: assets}
}

func missingUI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", indexCache)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("podium-server was built without the web UI; the API is unaffected\n"))
	})
}

type uiHandler struct{ assets fs.FS }

func (h *uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." || !fs.ValidPath(name) {
		h.serveIndex(w, r)
		return
	}

	info, err := fs.Stat(h.assets, name)
	switch {
	case err == nil && !info.IsDir():
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy(""))
		w.Header().Set("Cache-Control", cacheFor(name))
		http.ServeFileFS(w, r, h.assets, name)
	case strings.HasPrefix(name, assetsDir):
		// A missing hashed asset is a real 404: answering it with index.html would hand the
		// browser HTML where it asked for JavaScript.
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy(""))
		http.NotFound(w, r)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		h.serveIndex(w, r)
	}
}

func (h *uiHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	nonce, err := newStyleNonce()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	raw, err := fs.ReadFile(h.assets, indexFile)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body := injectStyleNonce(raw, nonce)
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy(nonce))
	w.Header().Set("Cache-Control", indexCache)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func newStyleNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("csp nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func injectStyleNonce(html []byte, nonce string) []byte {
	meta := []byte(`<meta name="` + cspNonceMetaName + `" content="` + nonce + `">`)
	const head = "<head>"
	i := bytes.Index(html, []byte(head))
	if i < 0 {
		return html
	}
	i += len(head)
	out := make([]byte, 0, len(html)+len(meta)+1)
	out = append(out, html[:i]...)
	out = append(out, '\n')
	out = append(out, meta...)
	out = append(out, html[i:]...)
	return out
}

func cacheFor(name string) string {
	if strings.HasPrefix(name, assetsDir) {
		return immutableCache
	}
	return indexCache
}
