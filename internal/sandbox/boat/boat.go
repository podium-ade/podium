// Package boat is the boat.dev sandbox provider: each sandbox is a VM with Docker, made
// from boat's base image or from a named snapshot, driven over https://boat.dev/api/v1.
package boat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

// DefaultAPIURL is boat's API base.
const DefaultAPIURL = "https://boat.dev/api/v1"

// smallFileLimit is the largest file sent inline; boat's JSON file API takes 5 MiB decoded.
const smallFileLimit = 4 << 20

func init() {
	sandbox.Register("boat", func(cfg map[string]any) (sandbox.Provider, error) {
		var c Config
		raw, _ := json.Marshal(cfg)
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("boat config: %w", err)
		}
		return New(c)
	})
}

// Config is the node's `sandbox.boat` block.
type Config struct {
	// APIKeyFile holds the API key. SENSITIVE: the key is never logged.
	APIKeyFile string `json:"api_key_file"`
	// Secret is the API key itself, as the control plane delivers it. SENSITIVE.
	Secret string `json:"secret"`
	APIURL string `json:"api_url"`
	// Org bills sandboxes to an organization instead of the key's owner.
	Org string `json:"org"`
	// DefaultType is the machine size when a task asks for no resources.
	DefaultType string `json:"default_type"`
}

// Provider talks to boat.
type Provider struct {
	key, base, org, defType string
	hc                      *http.Client
}

// New reads the key and returns a provider.
func New(c Config) (*Provider, error) {
	key := strings.TrimSpace(c.Secret)
	if key == "" {
		if c.APIKeyFile == "" {
			return nil, errors.New("boat: no API key: set api_key_file, or configure boat in the UI")
		}
		raw, err := os.ReadFile(c.APIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("boat: read api key: %w", err)
		}
		key = strings.TrimSpace(string(raw))
		if key == "" {
			return nil, fmt.Errorf("boat: %s is empty", c.APIKeyFile)
		}
	}
	if c.APIURL == "" {
		c.APIURL = DefaultAPIURL
	}
	return &Provider{key: key, base: strings.TrimRight(c.APIURL, "/"), org: c.Org, defType: c.DefaultType,
		hc: &http.Client{Timeout: 11 * time.Minute}}, nil
}

// Name implements sandbox.Provider.
func (p *Provider) Name() string { return "boat" }

// Capabilities implements sandbox.Provider.
func (p *Provider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{Snapshot: true, Docker: true, Root: true, Tmpfs: true, ExposeLoopback: true}
}

// machine sizes, smallest first.
var types = []struct {
	name string
	cpu  float64
	mem  int
}{{"small", 2, 4096}, {"default", 4, 8192}, {"large", 8, 16384}, {"xlarge", 16, 32768}}

func (p *Provider) machineType(cpu float64, mem int) string {
	if cpu <= 0 && mem <= 0 {
		return p.defType
	}
	for _, t := range types {
		if cpu <= t.cpu && mem <= t.mem {
			return t.name
		}
	}
	return types[len(types)-1].name
}

type sandboxInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error any    `json:"error"`
}

// Create implements sandbox.Provider.
func (p *Provider) Create(ctx context.Context, req sandbox.CreateRequest) (string, error) {
	if req.Image != "" {
		return "", &sandbox.Error{Op: "create", Code: "unsupported", Err: errors.New("boat cannot boot an OCI image")}
	}
	body := map[string]any{
		"noEnv":      true,
		"snapshots":  false,
		"monitoring": false,
	}
	if t := p.machineType(req.CPU, req.MemoryMB); t != "" && req.Snapshot == "" {
		body["type"] = t
	}
	if req.TTL > 0 {
		body["ttlSeconds"] = int(req.TTL.Seconds())
	}
	if req.Snapshot != "" {
		body["from"] = req.Snapshot
	}
	var out struct {
		Sandbox sandboxInfo `json:"sandbox"`
	}
	hdr := http.Header{}
	if req.Key != "" {
		hdr.Set("Idempotency-Key", req.Key)
	}
	if err := p.do(ctx, "create", http.MethodPost, "/sandboxes", hdr, body, &out); err != nil {
		return "", err
	}
	return out.Sandbox.ID, nil
}

// WaitReady implements sandbox.Provider.
func (p *Provider) WaitReady(ctx context.Context, id string) error {
	for {
		var out struct {
			Sandbox sandboxInfo `json:"sandbox"`
		}
		if err := p.do(ctx, "get", http.MethodGet, "/sandboxes/"+id, nil, nil, &out); err != nil {
			return err
		}
		switch out.Sandbox.State {
		case "ready", "idle", "running":
			return nil
		case "error", "cancelled", "archived", "archiving":
			return &sandbox.Error{Op: "wait", Code: out.Sandbox.State, Err: fmt.Errorf("sandbox %s: %v", id, out.Sandbox.Error)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// PutFile implements sandbox.Provider.
func (p *Provider) PutFile(ctx context.Context, id, path string, r io.Reader, size int64) error {
	if size >= 0 && size <= smallFileLimit {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		return p.do(ctx, "put file", http.MethodPut, "/sandboxes/"+id+"/files", nil, map[string]any{
			"path": path, "content": base64.StdEncoding.EncodeToString(b), "encoding": "base64",
		}, nil)
	}
	var up struct {
		URL string `json:"url"`
	}
	if err := p.do(ctx, "upload url", http.MethodPost, "/sandboxes/"+id+"/files/upload-url", nil,
		map[string]any{"path": path, "ttlSeconds": 900}, &up); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, up.URL, r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := p.hc.Do(req)
	if err != nil {
		return &sandbox.Error{Op: "upload", Code: "transport", Retryable: true, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &sandbox.Error{Op: "upload", Code: resp.Status, Err: errors.New(string(b))}
	}
	return nil
}

// Exec implements sandbox.Provider.
func (p *Provider) Exec(ctx context.Context, id, cmd string, detached bool) (sandbox.ExecResult, error) {
	body := map[string]any{"command": cmd}
	if detached {
		body["detached"] = true
	} else {
		body["timeoutSeconds"] = 600
	}
	var out struct {
		ExitCode *int   `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		TimedOut bool   `json:"timedOut"`
	}
	if err := p.do(ctx, "exec", http.MethodPost, "/sandboxes/"+id+"/commands", nil, body, &out); err != nil {
		return sandbox.ExecResult{}, err
	}
	res := sandbox.ExecResult{Stdout: out.Stdout, Stderr: out.Stderr}
	if out.ExitCode != nil {
		res.ExitCode = *out.ExitCode
	}
	if out.TimedOut {
		return res, &sandbox.Error{Op: "exec", Code: "timed_out"}
	}
	return res, nil
}

// Expose implements sandbox.Provider.
func (p *Provider) Expose(ctx context.Context, id string, port int) (sandbox.Endpoint, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := p.do(ctx, "expose", http.MethodPost, "/sandboxes/"+id+"/host", nil,
		map[string]any{"port": port, "localhost": true, "public": false}, &out); err != nil {
		return sandbox.Endpoint{}, err
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	token := u.Query().Get("_token")
	u.RawQuery = ""
	ep := sandbox.Endpoint{URL: u.String(), BrowserURL: out.URL}
	if token != "" {
		ep.Cookies = []*http.Cookie{{Name: "_port_auth", Value: token}}
	}
	return ep, nil
}

// ExtendTTL implements sandbox.Provider.
func (p *Provider) ExtendTTL(ctx context.Context, id string, d time.Duration) error {
	return p.do(ctx, "extend", http.MethodPatch, "/sandboxes/"+id, nil, map[string]any{"ttlSeconds": int(d.Seconds())}, nil)
}

// Delete implements sandbox.Provider.
func (p *Provider) Delete(ctx context.Context, id string) error {
	hdr := http.Header{}
	hdr.Set("X-Ascii-Confirm-Delete", id)
	err := p.do(ctx, "delete", http.MethodDelete, "/sandboxes/"+id, hdr, nil, nil)
	var pe *sandbox.Error
	if errors.As(err, &pe) && pe.Code == "not_found" {
		return nil
	}
	return err
}

// Limits implements sandbox.Provider.
func (p *Provider) Limits(ctx context.Context) (sandbox.Limits, error) {
	var out struct {
		CanStart           bool `json:"canStart"`
		ActiveSandboxes    int  `json:"activeSandboxes"`
		MaxActiveSandboxes int  `json:"maxActiveSandboxes"`
		Starts             struct {
			Unlimited bool `json:"unlimited"`
			Minute    struct {
				Remaining int `json:"remaining"`
			} `json:"minute"`
		} `json:"starts"`
	}
	if err := p.do(ctx, "limits", http.MethodGet, "/limits", nil, nil, &out); err != nil {
		return sandbox.Limits{}, err
	}
	l := sandbox.Limits{MaxSandboxes: out.MaxActiveSandboxes, FreeSandboxes: out.MaxActiveSandboxes - out.ActiveSandboxes, StartsLeft: out.Starts.Minute.Remaining}
	if out.Starts.Unlimited {
		l.StartsLeft = -1
	}
	if !out.CanStart {
		l.StartsLeft = 0
	}
	return l, nil
}

// apiError is boat's error body.
type apiError struct {
	Code              string  `json:"code"`
	Message           string  `json:"message"`
	RetryAfterSeconds float64 `json:"retryAfterSeconds"`
	RetryAfter        float64 `json:"retryAfter"`
	Window            string  `json:"window"`
	Error             struct {
		Details struct {
			Retryable         *bool   `json:"retryable"`
			RetryAfterSeconds float64 `json:"retryAfterSeconds"`
		} `json:"details"`
	} `json:"error"`
}

// retryableCodes are the failures boat documents as safe to repeat. Both spellings of
// "starting" are in its docs.
var retryableCodes = map[string]bool{
	"rate_limited": true, "IP_RATE_LIMIT_EXCEEDED": true, "no_ready_machine": true, "out_of_capacity": true,
	"sandbox_starting": true, "boat_starting": true, "idempotency_in_progress": true,
}

// do sends one request, retrying what boat says is safe to retry until ctx ends.
func (p *Provider) do(ctx context.Context, op, method, path string, hdr http.Header, in, out any) error {
	for {
		err := p.once(ctx, op, method, path, hdr, in, out)
		wait, retry := sandbox.IsRetryable(err)
		if !retry {
			return err
		}
		if wait <= 0 {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(wait):
		}
	}
}

func (p *Provider) once(ctx context.Context, op, method, path string, hdr http.Header, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.org != "" {
		req.Header.Set("X-Boat-Org", p.org)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Only requests that cannot have done anything twice are retried blind; a create
		// is safe because it carries an idempotency key.
		safe := method == http.MethodGet || method == http.MethodDelete || hdr.Get("Idempotency-Key") != ""
		return &sandbox.Error{Op: op, Code: "transport", Retryable: safe, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &sandbox.Error{Op: op, Code: "transport", Err: err}
	}
	if resp.StatusCode/100 == 2 {
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	var ae apiError
	_ = json.Unmarshal(raw, &ae)
	return toError(op, resp.StatusCode, ae)
}

func toError(op string, status int, ae apiError) *sandbox.Error {
	e := &sandbox.Error{Op: op, Code: ae.Code, Err: fmt.Errorf("http %d: %s", status, ae.Message)}
	if e.Code == "" {
		e.Code = http.StatusText(status)
	}
	e.Retryable = retryableCodes[ae.Code] || status == http.StatusServiceUnavailable
	if r := ae.Error.Details.Retryable; r != nil && *r {
		e.Retryable = true
	}
	secs := ae.RetryAfterSeconds
	if secs == 0 {
		secs = ae.RetryAfter
	}
	if secs == 0 {
		secs = ae.Error.Details.RetryAfterSeconds
	}
	if secs == 0 && ae.Code == "rate_limited" {
		// boat sends no Retry-After; a full per-minute window clears within a minute.
		secs = 60
	}
	e.RetryAfter = time.Duration(secs * float64(time.Second))
	return e
}
