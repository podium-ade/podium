// Package chathost runs the conductor's chat host: one opencode 2 service on the conductor's
// own machine that answers every assistant conversation, instead of a runtime and a harness
// started for each turn.
//
// One conversation is one opencode LOCATION — a directory with a config of its own — and one
// turn is one opencode session in it. The location is what makes a shared process safe to
// share: its config names the conversation's own model endpoint and MCP endpoints, each
// carrying a token that belongs to that conversation alone, so a model in one conversation
// cannot spend another's credential or act as another's turn. The model never sees those
// tokens: opencode 2 offers MCP tools only as JavaScript inside its `execute` tool, with no
// filesystem, and the service's global config denies everything else — shell, files, the
// network, opencode's own session and worktree tools (see Denied).
//
// The conductor is the record. A turn's system prompt carries the transcript, exactly as a
// host turn's does, so the opencode session is thrown away when the turn ends and a chat
// host that restarts has lost nothing.
package chathost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/podium-ade/podium/internal/netlisten"
)

// Options configures a Host.
type Options struct {
	// Binary is opencode 2 (`@opencode/cli`). It is pinned: the API and the tool names this
	// package relies on are opencode's own and move between releases.
	Binary string
	// Claude is a Claude Code binary for opencode's claude-agent provider, which a location
	// uses for a person signed in with their Claude subscription. Empty leaves the provider
	// with no `claude` to run: the service's PATH is the conductor's, so set it whenever a
	// location may name claude-agent.
	Claude string
	// Dir holds the service's own home and one directory per conversation. It must be
	// private to the conductor's user: a location's config carries its conversation's tokens.
	Dir string
	// Wrap fences the service: it gets the argv and returns the one to run, such as a
	// bubblewrap sandbox. Nil runs the service as this user, unfenced.
	Wrap func(argv []string, writable []string) []string
	// Settle is how long a location waits after its MCP servers connect before a session may
	// use them. opencode publishes a location's tools to sessions a little after it reports
	// the servers connected, and a session started before then has none for its whole life.
	// Zero is DefaultSettle.
	Settle time.Duration
	// Processes is how many opencode processes the host runs. opencode runs a session's work
	// on one JavaScript thread, so one process saturates a core long before memory; a
	// conversation lives on one of them, chosen by its name. Zero is DefaultProcesses.
	Processes int
	// Denied replaces the permissions denied (see Denied). Nil is Denied; an empty slice
	// denies nothing, which only a test wants: it is how the fence is shown to hold without
	// the rules.
	Denied []string
	Logger *slog.Logger
}

// DefaultSettle is Options.Settle's default, measured against opencode 2.2.5: sessions
// created a quarter of a second after the servers connected had no tools, and one second
// after had them, every time.
const DefaultSettle = 1500 * time.Millisecond

// Denied is every permission the service's global config denies. opencode 2 checks a tool by
// these action names — `fetch` inside `execute` is checked as webfetch — and its own tools as
// `opencode_<tool>`. What is left is `execute` itself and the MCP tools a location names.
// session_move is the one that matters most: it would move a session into another
// conversation's location, and so into that conversation's credentials.
var Denied = []string{
	"shell", "edit", "write", "read", "glob", "grep", "external_directory",
	"webfetch", "websearch", "subagent", "skill", "question",
	"opencode_session_move", "opencode_session_rename",
	"opencode_worktree_create", "opencode_worktree_remove", "opencode_worktree_list",
	"opencode_schedule_create", "opencode_schedule_pause", "opencode_schedule_resume",
	"opencode_schedule_delete", "opencode_models",
	"opencode_list_mcp_resources", "opencode_read_mcp_resource",
}

// AgentName is the opencode agent every turn runs as.
const AgentName = "podium"

// DefaultProcesses is Options.Processes's default. Measured on one process: 300 turns at
// once against a model that answers in 3s took 11.6s at p95, the process at a full core.
const DefaultProcesses = 4

// MaxBooting is how many locations boot at once.
const MaxBooting = 16

// MaxInFlight is how many model and tool requests the service keeps open at once.
const MaxInFlight = 1024

// Host is the running service: Processes opencode processes, each holding its share of the
// conversations.
type Host struct {
	o      Options
	logger *slog.Logger
	procs  []*proc
	// boots bounds the locations booting at once (MaxBooting).
	boots chan struct{}
	http  *http.Client
}

// proc is one opencode process.
type proc struct {
	i    int
	http *http.Client

	mu       sync.Mutex
	url      string
	password string
	// gen counts starts. A location booted under one start is not booted under the next.
	gen     int
	up      chan struct{}
	booted  map[string]int
	running bool
}

// New checks the options. Nothing runs until Run.
func New(o Options) (*Host, error) {
	if o.Binary == "" {
		return nil, errors.New("chathost: no opencode binary")
	}
	if o.Dir == "" {
		return nil, errors.New("chathost: no directory")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	n := o.Processes
	if n <= 0 {
		n = DefaultProcesses
	}
	h := &Host{o: o, logger: o.Logger.With("component", "chat-host"),
		boots: make(chan struct{}, MaxBooting), http: &http.Client{Timeout: 0}}
	for i := range n {
		h.procs = append(h.procs, &proc{i: i, http: h.http, up: make(chan struct{}), booted: map[string]int{}})
	}
	return h, nil
}

// procFor is the process a conversation's location lives on.
func (h *Host) procFor(name string) *proc {
	f := fnv.New32a()
	_, _ = f.Write([]byte(name))
	return h.procs[f.Sum32()%uint32(len(h.procs))]
}

// LocationsDir is where conversations' directories are made.
func (h *Host) LocationsDir() string { return filepath.Join(h.o.Dir, "locations") }

func (h *Host) home(p *proc) string { return filepath.Join(h.o.Dir, fmt.Sprintf("home-%d", p.i)) }

// Run keeps the processes up until ctx ends: each started, health-checked, and started again
// when it exits, after a pause that grows while it keeps failing.
func (h *Host) Run(ctx context.Context) error {
	if err := os.MkdirAll(h.LocationsDir(), 0o700); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, p := range h.procs {
		if err := h.writeGlobalConfig(p); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.keep(ctx, p)
		}()
	}
	wg.Wait()
	return nil
}

func (h *Host) keep(ctx context.Context, p *proc) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := h.runOnce(ctx, p)
		if ctx.Err() != nil {
			return
		}
		h.logger.WarnContext(ctx, "a chat host process stopped; starting it again", "process", p.i, "error", err, "in", backoff)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (h *Host) runOnce(ctx context.Context, p *proc) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	password, err := randomToken()
	if err != nil {
		return err
	}
	argv := []string{h.o.Binary, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	if h.o.Wrap != nil {
		argv = h.o.Wrap(argv, []string{h.o.Dir})
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the configured binary
	cmd.Dir = h.LocationsDir()
	home := h.home(p)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"OPENCODE_SERVER_PASSWORD=" + password,
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_FREE_MODELS=1",
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		// Where claude-agent finds Claude Code. It inherits this environment and nothing
		// else, so no credential of the conductor's reaches it: its model requests go to the
		// gateway, which a location's config names, with a placeholder the gateway replaces.
		"OPENCODE_CLAUDE_PATH=" + h.o.Claude,
		// Claude Code's telemetry, error reports and update checks are not model requests and
		// have no business leaving the conductor's machine.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1",
		// opencode 2 is a Bun binary, and Bun keeps at most 256 HTTP requests in flight by
		// default: every conversation's model requests go to the same gateway, so past 256
		// turns at once they queued. Measured: 400 at once took 4.6s at p95 against a model
		// that answers in 3s with this, 7.7s for 500 without.
		"BUN_CONFIG_MAX_HTTP_REQUESTS=" + strconv.Itoa(MaxInFlight),
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	logFile, err := os.OpenFile(filepath.Join(h.o.Dir, fmt.Sprintf("service-%d.log", p.i)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", h.o.Binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	url := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := h.awaitHealthy(ctx, url, password, exited); err != nil {
		_ = cmd.Cancel()
		<-exited
		return err
	}
	p.mu.Lock()
	p.url, p.password, p.running = url, password, true
	p.gen++
	close(p.up)
	p.mu.Unlock()
	h.logger.InfoContext(ctx, "chat host process up", "process", p.i, "url", url, "pid", cmd.Process.Pid)

	err = <-exited
	p.mu.Lock()
	p.running = false
	p.up = make(chan struct{})
	p.mu.Unlock()
	return err
}

func (h *Host) awaitHealthy(ctx context.Context, url, password string, exited <-chan error) error {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("the chat host exited while starting: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/global/health", nil)
		req.SetBasicAuth("opencode", password)
		resp, err := h.http.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
	return errors.New("the chat host did not answer its health check within a minute")
}

// Ready waits for every process to be up.
func (h *Host) Ready(ctx context.Context) error {
	for _, p := range h.procs {
		p.mu.Lock()
		up := p.up
		p.mu.Unlock()
		select {
		case <-up:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// writeGlobalConfig is the service's own config: what every location inherits. It names no
// provider and no MCP server — those are a conversation's — and denies every tool but
// `execute` and the MCP tools a location adds.
func (h *Host) writeGlobalConfig(p *proc) error {
	dir := filepath.Join(h.home(p), ".config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	denied := Denied
	if h.o.Denied != nil {
		denied = h.o.Denied
	}
	perms := make([]map[string]string, 0, len(denied))
	for _, a := range denied {
		perms = append(perms, map[string]string{"action": a, "resource": "*", "effect": "deny"})
	}
	doc := map[string]any{
		"$schema":     "https://opencode.ai/config.json",
		"permissions": perms,
		"share":       "disabled",
		// opencode names a new session with a model call of its own. Every turn here is a
		// new session, and the conductor names the chat itself, so that call would be paid
		// for on every turn and read by nobody.
		"agents": map[string]any{
			"title": map[string]any{"disabled": true},
			// The agent every turn runs as. opencode's default is a coding agent whose prompt
			// is about files, worktrees and edits; the conductor's prompt, set per turn as the
			// session's instructions, is the whole of what this one is told.
			AgentName: map[string]any{
				"mode":        "primary",
				"description": "A Podium assistant turn.",
				"system":      "You are an assistant. Your operating instructions follow.",
			},
		},
		// A location is a conversation; one nobody has written to for a while gives its
		// memory back, and is booted again by its next turn.
		"experimental": map[string]any{"location_idle_timeout": "30m"},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0o600)
}

func (p *proc) conn() (url, password string, gen int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return "", "", 0, fmt.Errorf("chat host process %d is not running", p.i)
	}
	return p.url, p.password, p.gen, nil
}

func freePort() (int, error) {
	l, err := netlisten.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewToken is a conversation's token: unguessable, URL-safe.
func NewToken() (string, error) { return randomToken() }
