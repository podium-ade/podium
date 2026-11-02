package conductor

// The model gateway: where a chat-host conversation's model requests go. opencode is
// configured with /chat-host/gateway/<conversation token> as the provider's base URL and a
// placeholder key; this replaces the key with the credential this conductor holds for the
// provider and forwards the request, streaming the answer back as it arrives.
//
// So the chat host never holds a model credential, and the conductor sees every request a
// conversation makes: that is where a per-user credential, a rate limit or a bill per person
// goes. A request is only forwarded while its conversation is running a turn.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ChatHostGateway is the model gateway.
func (c *Conductor) ChatHostGateway() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, _ := strings.CutPrefix(r.URL.Path, chatGatewayPath)
		token, path, _ := strings.Cut(rest, "/")
		c.chat.mu.Lock()
		cv := c.chat.byToken[token]
		var provider, upstream, login string
		running := false
		if cv != nil {
			provider, upstream, running = cv.provider, cv.upstream, cv.turn != nil
			if running {
				login = cv.turn.login
			}
		}
		c.chat.mu.Unlock()
		if !running {
			http.Error(w, "no turn is running in this conversation", http.StatusUnauthorized)
			return
		}
		target, err := url.Parse(strings.TrimSuffix(upstream, "/") + "/" + path)
		if err != nil || upstream == "" {
			http.Error(w, "no endpoint for provider "+provider, http.StatusBadGateway)
			return
		}
		key, whose := "", "company"
		if login != "" && c.host.PersonalCredential != nil {
			if k, err := c.host.PersonalCredential(r.Context(), login, provider); err == nil && k != "" {
				key, whose = k, "personal"
			}
		}
		if key == "" {
			k, err := c.host.Credential(r.Context(), provider)
			if err != nil || k == "" {
				http.Error(w, "no credential is stored for "+provider, http.StatusBadGateway)
				return
			}
			key = k
		}
		if c.metrics != nil {
			c.metrics.GatewayRequests.WithLabelValues(provider, whose).Inc()
		}
		q := r.URL.Query()
		q.Del("key")
		target.RawQuery = q.Encode()
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL = target
				pr.Out.Host = target.Host
				for name := range pr.Out.Header {
					l := strings.ToLower(name)
					if strings.HasPrefix(l, "x-opencode-") || strings.HasPrefix(l, "x-session") ||
						l == "authorization" || l == "x-api-key" || l == "x-goog-api-key" ||
						l == "traceparent" || l == "b3" {
						pr.Out.Header.Del(name)
					}
				}
				setProviderAuth(pr.Out.Header, provider, key)
			},
			// Stream: a model's answer arrives a token at a time, and buffering it would turn a
			// streamed reply into one that arrives all at once at the end.
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	})
}

// setProviderAuth writes key the way provider expects it.
func setProviderAuth(h http.Header, provider, key string) {
	switch provider {
	case "anthropic":
		h.Set("x-api-key", key)
	case "google":
		h.Set("x-goog-api-key", key)
	default:
		h.Set("Authorization", "Bearer "+key)
		if provider == "openai" {
			// A ChatGPT sign-in is a JWT naming the account Codex bills, and Codex wants it
			// said; an API key is not a JWT and needs nothing more.
			if account := chatgptAccount(key); account != "" {
				h.Set("ChatGPT-Account-Id", account)
				h.Set("originator", "podium")
			}
		}
	}
}

// chatgptAccount is the account a ChatGPT access token names, as the runtime reads it.
func chatgptAccount(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return claims.Auth.Account
}
