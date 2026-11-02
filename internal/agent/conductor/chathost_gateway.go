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
		creds := credentialProvider(provider)
		key, whose := "", "company"
		if login != "" && c.host.PersonalCredential != nil {
			k, err := c.host.PersonalCredential(r.Context(), login, creds)
			// A Claude sign-in is spent only by Claude Code (claude-agent). opencode's own
			// anthropic provider would send it from a client that is not Claude Code, so a
			// conversation on that provider spends the company key instead.
			if err == nil && k != "" && (provider == claudeAgentProvider || !claudeOAuthToken(k)) {
				key, whose = k, "personal"
			}
		}
		if key == "" {
			k, err := c.host.Credential(r.Context(), creds)
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

// claudeOAuthPrefix starts every Claude OAuth access token; an API key starts sk-ant-api.
const claudeOAuthPrefix = "sk-ant-oat"

func claudeOAuthToken(key string) bool { return strings.HasPrefix(key, claudeOAuthPrefix) }

// setProviderAuth writes key the way provider expects it, and only that way: whatever the
// client sent in the other headers (claude-agent's Claude Code sends `Bearer podium`) goes,
// so a provider never sees two credentials and refuses the wrong one.
func setProviderAuth(h http.Header, provider, key string) {
	h.Del("Authorization")
	h.Del("x-api-key")
	h.Del("x-goog-api-key")
	switch provider {
	case "anthropic", claudeAgentProvider:
		if !claudeOAuthToken(key) {
			h.Set("x-api-key", key)
			return
		}
		// A Claude sign-in is a bearer, and Anthropic wants the OAuth beta named beside
		// whatever betas Claude Code asked for.
		h.Set("Authorization", "Bearer "+key)
		betas := h.Values("anthropic-beta")
		h.Del("anthropic-beta")
		joined := strings.Join(betas, ",")
		if !strings.Contains(joined, "oauth-2025-04-20") {
			if joined != "" {
				joined += ","
			}
			joined += "oauth-2025-04-20"
		}
		h.Set("anthropic-beta", joined)
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
