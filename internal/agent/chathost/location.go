package chathost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provider is the model endpoint a conversation's location names.
type Provider struct {
	// ID is opencode's provider id: anthropic, openai, xai, google, or any other.
	ID string
	// Package is the opencode 2 provider implementation, @opencode/ai/providers/<name>. Empty
	// is the one named after ID.
	Package string
	// BaseURL is where its requests go: the conductor's gateway for this conversation.
	BaseURL string
	// APIKey is what opencode sends as the provider's credential. The gateway replaces it.
	APIKey string
	// Models are extra models to declare, for a provider opencode's catalog does not know.
	Models []string
}

// MCPServer is a remote MCP server a conversation's location connects to.
type MCPServer struct {
	Name    string
	URL     string
	Headers map[string]string
}

// Location is one conversation's place on the host.
type Location struct {
	// Name is the directory's name under LocationsDir: one per conversation.
	Name      string
	Providers []Provider
	MCP       []MCPServer
}

// Dir is the location's directory.
func (h *Host) Dir(name string) string { return filepath.Join(h.LocationsDir(), name) }

// Open writes the location's config and boots it: every MCP server connected, and then
// Settle, so a session started afterwards has every tool. A location already booted under
// the running service is only rewritten when its config changed.
func (h *Host) Open(ctx context.Context, loc Location) error {
	if loc.Name == "" || strings.ContainsAny(loc.Name, "/\\") || loc.Name == "." || loc.Name == ".." {
		return fmt.Errorf("chathost: location name %q", loc.Name)
	}
	dir := h.Dir(loc.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := locationConfig(loc)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "opencode.json")
	old, _ := os.ReadFile(path) //nolint:gosec // our own directory
	changed := !bytes.Equal(old, raw)
	if changed {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
	}
	p := h.procFor(loc.Name)
	_, _, gen, err := p.conn()
	if err != nil {
		return err
	}
	p.mu.Lock()
	wasBooted := p.booted[loc.Name] == gen
	p.mu.Unlock()
	booted := wasBooted && !changed
	if booted {
		return nil
	}
	// A location boots its providers and connects its MCP servers, and opencode does that a
	// handful at a time: three hundred conversations opening at once each waited out the
	// others' boots and timed out. So boots queue here, and each one's timeout counts only its
	// own boot.
	select {
	case h.boots <- struct{}{}:
		defer func() { <-h.boots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// A changed config is reloaded whether or not this host thinks the location is booted.
	// Close forgets a location and removes its directory, but opencode keeps the location
	// alive, and its file watcher reloads it with no config at all — no MCP servers. Writing
	// the file again is not seen by that watcher, so without a reload a reopened conversation
	// waited out awaitMCP for tools it would never get.
	if changed {
		if err := p.reloadLocation(ctx, dir); err != nil {
			return err
		}
	}
	if err := p.awaitMCP(ctx, dir, len(loc.MCP)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(cmpDuration(h.o.Settle, DefaultSettle)):
	}
	p.mu.Lock()
	p.booted[loc.Name] = gen
	p.mu.Unlock()
	return nil
}

// Close forgets a location and removes its directory, and with it the conversation's tokens.
func (h *Host) Close(name string) error {
	p := h.procFor(name)
	p.mu.Lock()
	delete(p.booted, name)
	p.mu.Unlock()
	return os.RemoveAll(h.Dir(name))
}

func locationConfig(loc Location) ([]byte, error) {
	providers := map[string]any{}
	for _, p := range loc.Providers {
		entry := map[string]any{
			"package":  cmpString(p.Package, "@opencode/ai/providers/"+p.ID),
			"settings": map[string]any{"baseURL": p.BaseURL, "apiKey": p.APIKey},
		}
		if len(p.Models) > 0 {
			models := map[string]any{}
			for _, m := range p.Models {
				models[m] = map[string]any{"name": m}
			}
			entry["models"] = models
		}
		providers[p.ID] = entry
	}
	servers := map[string]any{}
	for _, s := range loc.MCP {
		entry := map[string]any{"type": "remote", "url": s.URL, "enabled": true}
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
		}
		servers[s.Name] = entry
	}
	doc := map[string]any{"$schema": "https://opencode.ai/config.json", "providers": providers}
	if len(servers) > 0 {
		doc["mcp"] = map[string]any{"servers": servers}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// awaitMCP waits for want MCP servers of the location at dir to report connected.
func (p *proc) awaitMCP(ctx context.Context, dir string, want int) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		var out struct {
			Data []struct {
				Name   string `json:"name"`
				Status struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				} `json:"status"`
			} `json:"data"`
		}
		if err := p.call(ctx, http.MethodGet, "/api/mcp?"+locationQuery(dir), nil, &out); err != nil {
			return err
		}
		connected := 0
		var failed []string
		for _, s := range out.Data {
			switch s.Status.Status {
			case "connected":
				connected++
			case "failed":
				failed = append(failed, s.Name+": "+s.Status.Error)
			}
		}
		if connected >= want {
			return nil
		}
		if len(failed) > 0 {
			return fmt.Errorf("chathost: MCP servers failed to connect: %s", strings.Join(failed, "; "))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("chathost: %d of %d MCP servers connected after 30s", connected, want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *proc) reloadLocation(ctx context.Context, dir string) error {
	return p.call(ctx, http.MethodPost, "/api/location/reload?"+locationQuery(dir), map[string]any{}, nil)
}

func locationQuery(dir string) string {
	return "location%5Bdirectory%5D=" + url.QueryEscape(dir)
}

// Model is what a turn runs on.
type Model struct {
	Provider string
	ID       string
	// Variant is the provider's reasoning effort, when it has one.
	Variant string
}

// Turn is one assistant turn.
type Turn struct {
	// Location is the conversation's, already opened.
	Location string
	Model    Model
	// System is the whole system prompt, transcript included.
	System string
	// Prompt is the message that started the turn.
	Prompt string
	// Started is told the session's id as soon as there is one, so a message that arrives
	// while the turn runs can be steered into it (Steer).
	Started func(sessionID string)
	// Closing is asked each time the session goes idle whether to end the turn. True waits
	// again: a message was steered in since it was last asked, and opencode answers it — even
	// one that arrived after the session went idle, which starts a new run. False ends the
	// turn, and the caller must steer nothing into it after. Nil ends it at the first idle.
	Closing func() bool
}

// Result is how a turn ended.
type Result struct {
	// Text is the turn's last answer.
	Text string
	// Answers are the turn's answers in order, one per message it was given: the prompt's,
	// and one for each message steered in. Text is the last of them.
	Answers []string
	// Outcome is opencode's: succeeded, failed, interrupted.
	Outcome string
	// Error is opencode's reason, when the turn failed.
	Error   string
	Steps   int
	CostUSD float64
}

// Answer runs a turn in a session of its own and deletes the session afterwards: the
// conductor's transcript is the conversation's record, not opencode's.
func (h *Host) Answer(ctx context.Context, t Turn) (Result, error) {
	dir := h.Dir(t.Location)
	p := h.procFor(t.Location)
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	model := map[string]any{"id": t.Model.ID, "providerID": t.Model.Provider}
	if t.Model.Variant != "" {
		model["variant"] = t.Model.Variant
	}
	if err := p.call(ctx, http.MethodPost, "/api/session", map[string]any{
		"model":    model,
		"agent":    AgentName,
		"location": map[string]any{"directory": dir},
	}, &created); err != nil {
		return Result{}, err
	}
	id := created.Data.ID
	// Deleted once the turn is over, off its path: on a context of its own, since the turn's
	// may be the one that was cancelled, and in the background, since nobody waits on it.
	defer func() {
		go func() {
			done, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			_ = p.call(done, http.MethodDelete, "/api/session/"+id, nil, nil)
		}()
	}()
	if err := p.call(ctx, http.MethodPut, "/api/experimental/session/"+id+"/instructions/entries/podium",
		map[string]any{"value": t.System}, nil); err != nil {
		return Result{}, err
	}
	if t.Started != nil {
		t.Started(id)
	}
	if err := p.call(ctx, http.MethodPost, "/api/session/"+id+"/prompt", map[string]any{"text": t.Prompt}, nil); err != nil {
		return Result{}, err
	}
	for {
		waited := make(chan error, 1)
		go func() {
			waited <- p.call(ctx, http.MethodPost, "/api/experimental/session/"+id+"/wait", map[string]any{}, nil)
		}()
		select {
		case err := <-waited:
			if err != nil {
				return Result{}, err
			}
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = p.call(stop, http.MethodPost, "/api/session/"+id+"/interrupt", map[string]any{}, nil)
			return p.result(stop, id, "interrupted")
		}
		if t.Closing == nil || !t.Closing() {
			return p.result(ctx, id, "")
		}
		// A steer may still be on its way: give it a moment to land before waiting again.
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Steer writes a message into a running turn of the conversation at location: the model reads
// it at its next step.
func (h *Host) Steer(ctx context.Context, location, sessionID, text string) error {
	p := h.procFor(location)
	return p.call(ctx, http.MethodPost, "/api/session/"+sessionID+"/prompt",
		map[string]any{"text": text, "delivery": "steer"}, nil)
}

func (p *proc) result(ctx context.Context, id, outcome string) (Result, error) {
	// The messages alone: each step's cost is on its message and the outcome on the idle
	// that ended the turn, so the session itself is not read.
	var msgs struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := p.call(ctx, http.MethodGet, "/api/session/"+id+"/message", nil, &msgs); err != nil {
		return Result{}, err
	}
	res := Result{Outcome: outcome, Answers: answersOf(msgs.Data)}
	// Newest first.
	for _, raw := range msgs.Data {
		var m struct {
			Type    string  `json:"type"`
			Outcome string  `json:"outcome"`
			Error   any     `json:"error"`
			Cost    float64 `json:"cost"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Type {
		case "assistant":
			res.Steps++
			res.CostUSD += m.Cost
			if res.Text == "" {
				var parts []string
				for _, c := range m.Content {
					if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
						parts = append(parts, c.Text)
					}
				}
				res.Text = strings.Join(parts, "\n\n")
			}
		case "idle", "error":
			if res.Outcome == "" {
				res.Outcome = m.Outcome
			}
			if m.Error != nil && res.Error == "" {
				b, _ := json.Marshal(m.Error)
				res.Error = string(b)
			}
		}
	}
	return res, nil
}

// answersOf is a turn's answers, oldest first: the last text the model wrote after each
// message it was given. msgs is newest first, as opencode lists them.
func answersOf(msgs []json.RawMessage) []string {
	var answers []string
	last := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		var m struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(msgs[i], &m) != nil {
			continue
		}
		switch m.Type {
		case "user":
			if last != "" {
				answers = append(answers, last)
			}
			last = ""
		case "assistant":
			var parts []string
			for _, c := range m.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					parts = append(parts, c.Text)
				}
			}
			if len(parts) > 0 {
				last = strings.Join(parts, "\n\n")
			}
		}
	}
	if last != "" {
		answers = append(answers, last)
	}
	return answers
}

// startingWait bounds how long call retries a request the service refused because the
// location's service was still booting.
const startingWait = 30 * time.Second

// call is one request to the service. A non-2xx answer is an error carrying its body.
//
// opencode boots a location's service on its first request, and until it is up answers
// 503 {"code":"service_starting"} — after its health check already passed. A request refused
// that way was not processed, so it is sent again until the service is up.
func (p *proc) call(ctx context.Context, method, path string, body, out any) error {
	deadline := time.Now().Add(startingWait)
	for {
		err := p.callOnce(ctx, method, path, body, out)
		var starting errStarting
		if !errors.As(err, &starting) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// errStarting is a request refused while the location's service boots.
type errStarting struct{ error }

func (p *proc) callOnce(ctx context.Context, method, path string, body, out any) error {
	base, password, _, err := p.conn()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		err := fmt.Errorf("chathost: %s %s: %s: %s", method, strings.SplitN(path, "?", 2)[0], resp.Status, bytes.TrimSpace(raw))
		if resp.StatusCode == http.StatusServiceUnavailable && bytes.Contains(raw, []byte(`"service_starting"`)) {
			return errStarting{err}
		}
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.Join(fmt.Errorf("chathost: %s %s: decode", method, path), err)
	}
	return nil
}

func cmpString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func cmpDuration(a, b time.Duration) time.Duration {
	if a > 0 {
		return a
	}
	return b
}
