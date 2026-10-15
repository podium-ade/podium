// Package modal is the Modal sandbox provider: each sandbox is a Modal Sandbox in one App,
// made from a registry image or a filesystem snapshot, driven with Modal's Go SDK.
package modal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/modal-labs/modal-client/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/podium-ade/podium/internal/sandbox"
)

const (
	// DefaultApp is the Modal App sandboxes are made in; it is created if missing.
	DefaultApp = "podium-sandbox"
	// DefaultImage is the base for a sandbox with neither Image nor Snapshot; DefaultVMImage
	// is the one under the vm runtime, which has the Docker engine.
	DefaultImage = "ubuntu:24.04"
	// DefaultVMImage is DefaultImage under the vm runtime.
	DefaultVMImage = "docker:29-dind"
	// MaxTTL is Modal's longest sandbox timeout.
	MaxTTL = 24 * time.Hour
	// defaultTTL is Modal's own default timeout.
	defaultTTL = 5 * time.Minute
	// ttlFile holds "<created> <deadline>" in Unix seconds; the entrypoint exits, and so
	// ends the sandbox, once the deadline passes. Modal cannot move a running sandbox's
	// timeout, so the timeout is set to MaxTTL and ExtendTTL moves this deadline instead.
	ttlFile = "/tmp/.podium-ttl"
)

func init() {
	sandbox.Register("modal", func(cfg map[string]any) (sandbox.Provider, error) {
		var c Config
		raw, _ := json.Marshal(cfg)
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("modal config: %w", err)
		}
		return New(c)
	})
}

// Config is the node's `sandbox.modal` block.
type Config struct {
	App         string `json:"app"`
	Environment string `json:"environment"`
	// TokenID and TokenSecretFile authenticate; empty uses MODAL_TOKEN_ID/MODAL_TOKEN_SECRET
	// or the ~/.modal.toml profile. SENSITIVE: the secret is never logged.
	TokenID         string `json:"token_id"`
	TokenSecretFile string `json:"token_secret_file"`
	// CPU and MemoryMB are the request when a task asks for no resources.
	CPU      float64 `json:"cpu"`
	MemoryMB int     `json:"memory_mb"`
	// Runtime is "gvisor" (the default) or "vm". Only vm sandboxes can mount a tmpfs or run
	// Docker; when one's image has dockerd, the sandbox starts it.
	Runtime string `json:"runtime"`
	Image   string `json:"image"`
}

// Provider talks to Modal.
type Provider struct {
	c   *sdk.Client
	cfg Config

	mu  sync.Mutex
	app *sdk.App
	sbs map[string]*sdk.Sandbox
}

// New checks the config and connects to Modal.
func New(c Config) (*Provider, error) {
	if c.Runtime == "" {
		c.Runtime = string(sdk.SandboxRuntimeGVisor)
	}
	switch sdk.SandboxRuntime(c.Runtime) {
	case sdk.SandboxRuntimeGVisor, sdk.SandboxRuntimeVM:
	default:
		return nil, fmt.Errorf("modal: runtime %q is not gvisor or vm", c.Runtime)
	}
	if c.CPU < 0 || c.MemoryMB < 0 {
		return nil, errors.New("modal: cpu and memory_mb must not be negative")
	}
	if c.App == "" {
		c.App = DefaultApp
	}
	if c.Image == "" {
		c.Image = DefaultImage
		if c.vm() {
			c.Image = DefaultVMImage
		}
	}
	params := &sdk.ClientParams{Environment: c.Environment}
	if c.TokenID != "" || c.TokenSecretFile != "" {
		if c.TokenID == "" || c.TokenSecretFile == "" {
			return nil, errors.New("modal: token_id and token_secret_file go together")
		}
		raw, err := os.ReadFile(c.TokenSecretFile)
		if err != nil {
			return nil, fmt.Errorf("modal: read token secret: %w", err)
		}
		params.TokenID, params.TokenSecret = c.TokenID, strings.TrimSpace(string(raw))
		if params.TokenSecret == "" {
			return nil, fmt.Errorf("modal: %s is empty", c.TokenSecretFile)
		}
	}
	client, err := sdk.NewClientWithOptions(params)
	if err != nil {
		return nil, fmt.Errorf("modal: %w", err)
	}
	return &Provider{c: client, cfg: c, sbs: map[string]*sdk.Sandbox{}}, nil
}

func (c Config) vm() bool { return c.Runtime == string(sdk.SandboxRuntimeVM) }

// Name implements sandbox.Provider.
func (p *Provider) Name() string { return "modal" }

// Capabilities implements sandbox.Provider. gVisor gives root but no CAP_SYS_ADMIN, so no mounts and no Docker; a vm
// sandbox has every capability. Exposed ports go through Modal's proxy, which reaches only
// services listening on every interface.
func (p *Provider) Capabilities() sandbox.Capabilities {
	vm := p.cfg.vm()
	return sandbox.Capabilities{NativeImage: true, Snapshot: true, Root: true, Docker: vm, Tmpfs: vm, MaxTTL: MaxTTL}
}

// sandboxName is the Modal name for a key. Modal keeps names unique among an App's running
// sandboxes, which is what makes Create idempotent.
func sandboxName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "podium-" + hex.EncodeToString(sum[:10])
}

// dockerd starts the image's Docker engine, if it has one.
const dockerd = "command -v dockerd >/dev/null && { dockerd >/var/log/podium-dockerd.log 2>&1 & }\n"

// entrypoint keeps the sandbox up until the deadline in file.
func entrypoint(file string, ttl time.Duration, docker bool) []string {
	script := fmt.Sprintf(`now=$(date +%%s); echo "$now $((now + %d))" > %s
while read -r _ d < %s && [ "$(date +%%s)" -lt "$d" ]; do sleep 2; done`, int(ttl.Seconds()), file, file)
	if docker {
		script = dockerd + script
	}
	return []string{"sh", "-c", script}
}

func (p *Provider) createParams(req sandbox.CreateRequest) (*sdk.SandboxCreateParams, error) {
	ttl := req.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return nil, &sandbox.Error{Op: "create", Code: "invalid", Err: fmt.Errorf("ttl %v is not within 1s..%v", req.TTL, MaxTTL)}
	}
	cpu, mem := req.CPU, req.MemoryMB
	if cpu <= 0 {
		cpu = p.cfg.CPU
	}
	if mem <= 0 {
		mem = p.cfg.MemoryMB
	}
	return &sdk.SandboxCreateParams{
		CPU:       cpu,
		MemoryMiB: mem,
		Runtime:   sdk.SandboxRuntime(p.cfg.Runtime),
		Timeout:   MaxTTL,
		Command:   entrypoint(ttlFile, ttl.Truncate(time.Second), p.cfg.vm()),
		Name:      sandboxName(req.Key),
		Tags:      map[string]string{"podium.key": req.Key},
	}, nil
}

func (p *Provider) getApp(ctx context.Context) (*sdk.App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.app != nil {
		return p.app, nil
	}
	app, err := p.c.Apps.FromName(ctx, p.cfg.App, &sdk.AppFromNameParams{Environment: p.cfg.Environment, CreateIfMissing: true})
	if err != nil {
		return nil, toError("app", err)
	}
	p.app = app
	return app, nil
}

// Create makes the sandbox from req.Image, or from req.Snapshot, the image ID ("im-...") that
// Sandbox.SnapshotFilesystem returned.
func (p *Provider) Create(ctx context.Context, req sandbox.CreateRequest) (string, error) {
	if req.Key == "" {
		return "", &sandbox.Error{Op: "create", Code: "invalid", Err: errors.New("no key")}
	}
	if req.Image != "" && req.Snapshot != "" {
		return "", &sandbox.Error{Op: "create", Code: "invalid", Err: errors.New("both image and snapshot")}
	}
	params, err := p.createParams(req)
	if err != nil {
		return "", err
	}
	app, err := p.getApp(ctx)
	if err != nil {
		return "", err
	}
	if id, err := p.byName(ctx, app, params.Name); id != "" || err != nil {
		return id, err
	}
	var img *sdk.Image
	switch {
	case req.Snapshot != "":
		if img, err = p.c.Images.FromID(ctx, req.Snapshot, nil); err != nil {
			return "", toError("create", err)
		}
	case req.Image != "":
		img = p.c.Images.FromRegistry(req.Image, nil)
	default:
		img = p.c.Images.FromRegistry(p.cfg.Image, nil)
	}
	sb, err := p.c.Sandboxes.Create(ctx, app, img, params)
	var exists sdk.AlreadyExistsError
	if errors.As(err, &exists) {
		return p.byName(ctx, app, params.Name)
	}
	if err != nil {
		return "", toError("create", err)
	}
	p.remember(sb)
	return sb.SandboxID, nil
}

// byName is the running sandbox called name, or "" when there is none.
func (p *Provider) byName(ctx context.Context, app *sdk.App, name string) (string, error) {
	sb, err := p.c.Sandboxes.FromName(ctx, app.Name, name, &sdk.SandboxFromNameParams{Environment: p.cfg.Environment})
	var nf sdk.NotFoundError
	if errors.As(err, &nf) {
		return "", nil
	}
	if err != nil {
		return "", toError("create", err)
	}
	p.remember(sb)
	return sb.SandboxID, nil
}

// remember caches a sandbox handle, which holds its command connection, and returns the
// cached one.
func (p *Provider) remember(sb *sdk.Sandbox) *sdk.Sandbox {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sbs[sb.SandboxID]; ok {
		return old
	}
	p.sbs[sb.SandboxID] = sb
	return sb
}

func (p *Provider) sandbox(ctx context.Context, id string) (*sdk.Sandbox, error) {
	p.mu.Lock()
	sb := p.sbs[id]
	p.mu.Unlock()
	if sb != nil {
		return sb, nil
	}
	sb, err := p.c.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return nil, toError("get", err)
	}
	return p.remember(sb), nil
}

// readyCmd succeeds once the entrypoint has written the deadline and, where the image has
// one, the Docker engine answers.
const readyCmd = "test -s " + ttlFile + " && { ! command -v dockerd >/dev/null || docker info >/dev/null 2>&1; }"

// WaitReady implements sandbox.Provider.
func (p *Provider) WaitReady(ctx context.Context, id string) error {
	sb, err := p.sandbox(ctx, id)
	if err != nil {
		return err
	}
	probe := "test -s " + ttlFile
	if p.cfg.vm() {
		probe = readyCmd
	}
	for {
		if code, err := sb.Poll(ctx, nil); err != nil {
			return toError("wait", err)
		} else if code != nil {
			return &sandbox.Error{Op: "wait", Code: "exited", Err: fmt.Errorf("sandbox %s exited with %d", id, *code)}
		}
		if res, err := p.Exec(ctx, id, probe, false); err == nil && res.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// PutFile implements sandbox.Provider.
func (p *Provider) PutFile(ctx context.Context, id, path string, r io.Reader, _ int64) error {
	sb, err := p.sandbox(ctx, id)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := sb.Filesystem.WriteBytes(ctx, b, path, nil); err != nil {
		return toError("put file", err)
	}
	return nil
}

// Exec implements sandbox.Provider.
func (p *Provider) Exec(ctx context.Context, id, cmd string, detached bool) (sandbox.ExecResult, error) {
	sb, err := p.sandbox(ctx, id)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	argv := []string{"sh", "-c", cmd}
	if detached {
		_, err := sb.Exec(ctx, argv, &sdk.SandboxExecParams{Stdout: sdk.Ignore, Stderr: sdk.Ignore})
		return sandbox.ExecResult{}, toError("exec", err)
	}
	cp, err := sb.Exec(ctx, argv, &sdk.SandboxExecParams{Stdout: sdk.Pipe, Stderr: sdk.Pipe, Timeout: 10 * time.Minute})
	if err != nil {
		return sandbox.ExecResult{}, toError("exec", err)
	}
	var stdout, stderr bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderr, cp.Stderr)
		close(done)
	}()
	_, _ = io.Copy(&stdout, cp.Stdout)
	<-done
	code, err := cp.Wait(ctx, nil)
	res := sandbox.ExecResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
	return res, toError("exec", err)
}

// connectCookie carries a connect token. Modal also takes it as a bearer token, but the
// cookie leaves the Authorization header to the agent.
const connectCookie = "_modal_connect_token"

// Expose makes a connect token for port. Unlike a tunnel, whose URL is public to anyone
// who has it, Modal's proxy lets a request through only with the token, and the port need
// not be declared when the sandbox is made.
func (p *Provider) Expose(ctx context.Context, id string, port int) (sandbox.Endpoint, error) {
	sb, err := p.sandbox(ctx, id)
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	creds, err := sb.CreateConnectToken(ctx, &sdk.SandboxCreateConnectTokenParams{Port: port})
	if err != nil {
		return sandbox.Endpoint{}, toError("expose", err)
	}
	return sandbox.Endpoint{URL: creds.URL, Cookies: []*http.Cookie{{Name: connectCookie, Value: creds.Token}}}, nil
}

// errMaxTTL is the exit status of extendScript when the deadline would pass MaxTTL.
const errMaxTTL = 3

func extendScript(file string, d time.Duration) string {
	return fmt.Sprintf(`read -r c _ < %[1]s || exit 1; d=$(($(date +%%s) + %[2]d)); [ "$d" -le $((c + %[3]d)) ] || exit %[4]d
echo "$c $d" > %[1]s.new && mv %[1]s.new %[1]s`, file, int(d.Seconds()), int(MaxTTL.Seconds()), errMaxTTL)
}

// ExtendTTL implements sandbox.Provider.
func (p *Provider) ExtendTTL(ctx context.Context, id string, d time.Duration) error {
	if d < time.Second || d > MaxTTL {
		return &sandbox.Error{Op: "extend", Code: "invalid", Err: fmt.Errorf("ttl %v is not within 1s..%v", d, MaxTTL)}
	}
	res, err := p.Exec(ctx, id, extendScript(ttlFile, d), false)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case errMaxTTL:
		return &sandbox.Error{Op: "extend", Code: "max_ttl", Err: fmt.Errorf("modal stops a sandbox %v after it starts", MaxTTL)}
	}
	return &sandbox.Error{Op: "extend", Code: "failed", Err: fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))}
}

// Delete implements sandbox.Provider.
func (p *Provider) Delete(ctx context.Context, id string) error {
	sb, err := p.sandbox(ctx, id)
	if err != nil {
		return err
	}
	_, err = sb.Terminate(ctx, nil)
	var pe *sandbox.Error
	if err = toError("delete", err); err != nil && (!errors.As(err, &pe) || pe.Code != "not_found") {
		return err
	}
	p.mu.Lock()
	delete(p.sbs, id)
	p.mu.Unlock()
	_ = sb.Detach()
	return nil
}

// Limits is unknown: Modal has no API for the account's remaining room.
func (p *Provider) Limits(context.Context) (sandbox.Limits, error) {
	return sandbox.Limits{FreeSandboxes: -1, StartsLeft: -1}, nil
}

// toError maps an SDK or gRPC error to a *sandbox.Error; context errors pass through.
func toError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	e := &sandbox.Error{Op: op, Code: "modal", Err: err}
	var (
		notFound  sdk.NotFoundError
		exhausted sdk.ResourceExhaustedError
		internal  sdk.InternalFailure
		invalid   sdk.InvalidError
		exists    sdk.AlreadyExistsError
		conflict  sdk.ConflictError
		sbTimeout sdk.SandboxTimeoutError
		timeout   sdk.TimeoutError
		execTO    sdk.ExecTimeoutError
		closed    sdk.ClientClosedError
	)
	switch {
	case errors.As(err, &notFound):
		e.Code = "not_found"
	case errors.As(err, &exhausted):
		e.Code, e.Retryable, e.RetryAfter = "resource_exhausted", true, 10*time.Second
	case errors.As(err, &internal):
		e.Code, e.Retryable = "internal", true
	case errors.As(err, &invalid):
		e.Code = "invalid"
	case errors.As(err, &exists):
		e.Code = "already_exists"
	case errors.As(err, &conflict):
		e.Code = "conflict"
	case errors.As(err, &sbTimeout), errors.As(err, &timeout):
		e.Code, e.Retryable = "timeout", true
	case errors.As(err, &execTO):
		e.Code = "timed_out"
	case errors.As(err, &closed):
		e.Code = "closed"
	default:
		if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
			e.Code = grpcCode(s.Code())
			switch s.Code() {
			case codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.DeadlineExceeded, codes.Internal:
				e.Retryable = true
			}
			if s.Code() == codes.ResourceExhausted {
				e.RetryAfter = 10 * time.Second
			}
		}
	}
	return e
}

func grpcCode(c codes.Code) string {
	switch c {
	case codes.NotFound:
		return "not_found"
	case codes.ResourceExhausted:
		return "resource_exhausted"
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return "invalid"
	case codes.Unauthenticated, codes.PermissionDenied:
		return "unauthorized"
	case codes.AlreadyExists:
		return "already_exists"
	}
	return strings.ToLower(c.String())
}
